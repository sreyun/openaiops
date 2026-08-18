package main

import (
	"compress/gzip"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"aiops-monitor/shared"
)

// ============================================================================
// Embedded lightweight database
//
// A zero-dependency snapshot store in the spirit of Redis' RDB: the whole
// monitoring state (hosts + tiered time-series history, plugin events, the
// activity log, delete-suppressions and login sessions) is serialized as
// gzip-compressed JSON and written atomically (tmp + rename) to one file,
// aiops.db, next to the server config. An autosave loop persists only when
// the state is dirty, and a signal handler flushes on shutdown — so history
// finally survives restarts, closing the old "in-memory only" gap.
// ============================================================================

const dbVersion = 1

// dbHost mirrors Host with the unexported history tiers exported for JSON.
type dbHost struct {
	Host     Host            `json:"host"`
	HistRaw  []shared.Sample `json:"hist_raw,omitempty"`
	Hist1m   []shared.Sample `json:"hist_1m,omitempty"`
	Hist5m   []shared.Sample `json:"hist_5m,omitempty"`
	Last1mTs int64           `json:"last_1m_ts"`
	Last5mTs int64           `json:"last_5m_ts"`
}

type dbSession struct {
	User    string `json:"user"`
	Expires int64  `json:"expires"`
	// Persist the session's auth state so a server restart doesn't silently drop
	// it: terminalVerified (else the terminal password is re-prompted after every
	// restart) and restricted (else a global-MFA restricted session would escalate
	// to full access on restart).
	TerminalVerified bool `json:"terminal_verified,omitempty"`
	Restricted       bool `json:"restricted,omitempty"`
	PwChangeOnly     bool `json:"pw_change_only,omitempty"`
}

type dbSnapshot struct {
	Version     int                  `json:"version"`
	SavedAt     int64                `json:"saved_at"`
	Hosts       []dbHost             `json:"hosts"`
	Events      []storedEvent        `json:"events"`
	Activity    []LogEntry           `json:"activity"`
	Deleted     map[string]int64     `json:"deleted"`
	Sessions    map[string]dbSession `json:"sessions"`
	AlertStates map[string]string    `json:"alert_states"`
	Incidents   []Incident           `json:"incidents,omitempty"`
	Tickets     []Ticket             `json:"tickets,omitempty"`
}

// DB binds the snapshot file to the live Store and Auth state.
type DB struct {
	path      string
	store     *Store
	auth      *Auth
	incidents *incidentManager // SRE state (set after server construction; nil-safe)
	tickets   *ticketManager
}

func NewDB(path string, store *Store, auth *Auth) *DB {
	return &DB{path: path, store: store, auth: auth}
}

// BindSRE wires the incident + ticket managers so their state is persisted.
func (d *DB) BindSRE(inc *incidentManager, tk *ticketManager) {
	d.incidents = inc
	d.tickets = tk
}

// recordingsDirFor places the terminal session recordings next to the config/DB,
// so they live on the same persistent data volume.
func recordingsDirFor(cfgPath string) string {
	return filepath.Join(filepath.Dir(cfgPath), "recordings")
}

// Load restores a snapshot if one exists. Corrupt or missing files are not
// fatal — the server just starts empty, exactly like before persistence.
func (d *DB) Load() {
	f, err := os.Open(d.path)
	if err != nil {
		return // first run
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		slog.Warn(Tz("db.read_failed"), "err", err)
		return
	}
	defer gz.Close()
	var snap dbSnapshot
	if err := json.NewDecoder(gz).Decode(&snap); err != nil {
		slog.Warn(Tz("db.parse_failed"), "err", err)
		return
	}

	s := d.store
	s.mu.Lock()
	for i := range snap.Hosts {
		hh := snap.Hosts[i]
		h := hh.Host // copy
		h.histRaw = hh.HistRaw
		h.hist1m = hh.Hist1m
		h.hist5m = hh.Hist5m
		h.last1mTs = hh.Last1mTs
		h.last5mTs = hh.Last5mTs
		s.hosts[h.ID] = &h
	}
	s.events = snap.Events
	s.activity = snap.Activity
	s.alertStates = snap.AlertStates
	if s.alertStates == nil {
		s.alertStates = make(map[string]string)
	}
	if snap.Deleted != nil {
		s.deleted = snap.Deleted
	}
	s.mu.Unlock()

	d.auth.importSessions(snap.Sessions)
	if d.incidents != nil {
		d.incidents.Import(snap.Incidents)
	}
	if d.tickets != nil {
		d.tickets.Import(snap.Tickets)
	}
	slog.Info(Tz("db.restored"),
		"hosts", len(snap.Hosts),
		"events", len(snap.Events),
		"activity", len(snap.Activity),
		"sessions", len(snap.Sessions),
		"path", d.path)
}

// Save serializes the current state and writes it atomically.
func (d *DB) Save() error {
	snap := d.export()
	tmp := d.path + ".tmp"
	// 0o600: the DB holds session tokens (hashed), activity logs and host state —
	// it must not be world-readable. O_TRUNC matches the previous os.Create.
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	gz, _ := gzip.NewWriterLevel(f, gzip.BestSpeed)
	enc := json.NewEncoder(gz)
	if err := enc.Encode(snap); err != nil {
		gz.Close()
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := gz.Close(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, d.path)
}

// cloneSamples copies a history tier so the DB snapshot doesn't share the live
// slice's backing array with concurrent Upsert appends.
func cloneSamples(s []shared.Sample) []shared.Sample {
	if len(s) == 0 {
		return nil
	}
	out := make([]shared.Sample, len(s))
	copy(out, s)
	return out
}

func (d *DB) export() dbSnapshot {
	s := d.store
	s.mu.RLock()
	snap := dbSnapshot{
		Version: dbVersion,
		SavedAt: time.Now().Unix(),
		Deleted: make(map[string]int64, len(s.deleted)),
	}
	for _, h := range s.hosts {
		// Clone the history tiers: the snapshot is JSON-encoded after RUnlock, so it
		// must not share the live slices' backing arrays with concurrent Upsert
		// appends (a data race even when the read window doesn't overlap the write).
		snap.Hosts = append(snap.Hosts, dbHost{
			Host:     *h,
			HistRaw:  cloneSamples(h.histRaw),
			Hist1m:   cloneSamples(h.hist1m),
			Hist5m:   cloneSamples(h.hist5m),
			Last1mTs: h.last1mTs, Last5mTs: h.last5mTs,
		})
	}
	snap.Events = append(snap.Events, s.events...)
	snap.Activity = append(snap.Activity, s.activity...)
	for k, v := range s.deleted {
		snap.Deleted[k] = v
	}
	if s.alertStates != nil {
		snap.AlertStates = make(map[string]string, len(s.alertStates))
		for k, v := range s.alertStates {
			snap.AlertStates[k] = v
		}
	}
	s.mu.RUnlock()
	snap.Sessions = d.auth.exportSessions()
	if d.incidents != nil {
		snap.Incidents = d.incidents.Export()
	}
	if d.tickets != nil {
		snap.Tickets = d.tickets.Export()
	}
	return snap
}

// AutoSave persists periodically whenever the state changed since last save.
func (d *DB) AutoSave(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for range t.C {
		if d.consumeDirty() {
			if err := d.Save(); err != nil {
				slog.Error(Tz("db.save_failed"), "err", err)
			}
		}
	}
}

func (d *DB) consumeDirty() bool {
	s := d.store
	s.mu.Lock()
	dirty := s.dirty
	s.dirty = false
	s.mu.Unlock()
	return dirty || d.auth.consumeDirty()
}
