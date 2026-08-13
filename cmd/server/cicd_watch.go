package main

// CI/CD failure watcher.
//
// CICDConnection carries two operator switches — WatchFailures ("raise an alert
// when a run goes red") and AutoIncident ("open an SRE incident so the AI
// diagnose → verify → remediate loop owns it"). Both were persisted and rendered
// as checkboxes while nothing on the server ever read them, so a console that
// promised failure alerting delivered silence. This is the missing consumer.
//
// It deliberately rides CachedCICDRuns: the console already polls the same
// connections, so a watcher tick usually costs zero upstream calls and can never
// double the pressure on a GitLab/GitHub rate limit.

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const (
	// cicdWatchRunLimit matches the console's page size so watcher and UI share
	// one cache entry instead of thrashing it with two different keys.
	cicdWatchRunLimit = 30
	// cicdWatchSeenTTL is how long a reported failure stays deduped. Runs age out
	// of the upstream list long before this, so it only bounds the map.
	cicdWatchSeenTTL = 24 * time.Hour
	cicdWatchSeenMax = 4000
)

type cicdFailureWatcher struct {
	mu sync.Mutex
	// seen: "connID/runID" → first reported at (unix).
	seen map[string]int64
	// seeded connections have had their current backlog absorbed silently.
	seeded map[string]bool
}

func newCICDFailureWatcher() *cicdFailureWatcher {
	return &cicdFailureWatcher{seen: map[string]int64{}, seeded: map[string]bool{}}
}

// markSeen returns true when this run had not been reported yet.
func (w *cicdFailureWatcher) markSeen(key string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.seen[key]; ok {
		return false
	}
	w.seen[key] = time.Now().Unix()
	if len(w.seen) > cicdWatchSeenMax {
		cutoff := time.Now().Add(-cicdWatchSeenTTL).Unix()
		for k, at := range w.seen {
			if at < cutoff {
				delete(w.seen, k)
			}
		}
	}
	return true
}

// takeSeed reports whether this connection still needs its baseline pass.
func (w *cicdFailureWatcher) takeSeed(connID string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.seeded[connID] {
		return false
	}
	w.seeded[connID] = true
	return true
}

// forget drops watcher state for a connection that was deleted or disabled, so
// re-enabling it re-seeds instead of replaying a stale backlog.
func (w *cicdFailureWatcher) forget(connID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.seeded, connID)
	prefix := connID + "/"
	for k := range w.seen {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			delete(w.seen, k)
		}
	}
}

// startCICDFailureWatcher polls watched connections until the process exits.
func (s *Server) startCICDFailureWatcher(interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for range t.C {
		s.runCICDFailureScan()
	}
}

func (s *Server) runCICDFailureScan() {
	if s == nil || s.cicdWatcher == nil || s.cfg == nil {
		return
	}
	conns := s.cfg.ListCICDConnections()
	for _, c := range conns {
		if !c.Enabled || !c.WatchFailures {
			// A connection turned off mid-flight must not replay its backlog when
			// it comes back — drop the baseline so the next pass re-seeds.
			s.cicdWatcher.forget(c.ID)
			continue
		}
		s.scanCICDConnection(c)
	}
}

func (s *Server) scanCICDConnection(c CICDConnection) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	runs, err := s.CachedCICDRuns(ctx, c, cicdWatchRunLimit)
	s.cfg.SetCICDSyncResult(c.ID, cicdErrText(err))
	if err != nil {
		slog.Debug("CI/CD 失败巡检拉取失败", "connection", c.Name, "err", err)
		return
	}

	// First pass after start / re-enable: absorb the existing red runs silently.
	// Without this, every restart would re-alert the whole recent history.
	if s.cicdWatcher.takeSeed(c.ID) {
		for _, run := range runs {
			if run.Status == CICDStatusFailed {
				s.cicdWatcher.markSeen(c.ID + "/" + run.ID)
			}
		}
		return
	}

	for _, run := range runs {
		if run.Status != CICDStatusFailed {
			continue
		}
		if !s.cicdWatcher.markSeen(c.ID + "/" + run.ID) {
			continue
		}
		s.reportCICDFailure(ctx, c, run)
	}
}

func (s *Server) reportCICDFailure(ctx context.Context, c CICDConnection, run CICDRun) {
	label := fmt.Sprintf("%s · %s #%v", c.Name, run.Ref, firstNonEmptyAny(run.Number, run.ID))
	msg := "流水线失败：" + label
	if run.Actor != "" {
		msg += "（触发人 " + run.Actor + "）"
	}
	if run.WebURL != "" {
		msg += " " + run.WebURL
	}

	s.store.AddLog(LogEntry{
		Kind: KindSystem, Level: "warning", Actor: "cicd-watch", Message: msg,
	})
	s.store.AddAlertRecord(AlertRecord{
		Key:     "cicd/" + c.ID + "/" + run.ID,
		Level:   "warning",
		Type:    "cicd_failed",
		Scope:   c.Project,
		Message: msg,
		FiredAt: time.Now().Unix(),
	})
	if s.notifier != nil {
		s.notifier.PushAdhoc("warning", "流水线失败", msg, nil)
	}
	slog.Info("CI/CD 流水线失败已告警", "connection", c.Name, "project", c.Project, "run", run.ID)

	if !c.AutoIncident {
		return
	}
	// Same dedup key the manual "失败转事件" path uses, so an operator who clicks
	// diagnose on an auto-raised incident lands on that incident instead of a twin.
	key := fmt.Sprintf("cicd/%s/%s", c.ID, run.ID)
	title := fmt.Sprintf("流水线失败：%s · %s #%v", c.Name, run.Ref, firstNonEmptyAny(run.Number, run.ID))
	incID, created := s.incidents.raise(key, title, "warning", "CI/CD", "", c.Project, "pipeline")
	if incID <= 0 || !created {
		return
	}
	// Evidence costs 1–2 extra upstream calls, so only gather it for an incident
	// that was actually created — never for a duplicate of one already open.
	s.incidents.AddEvent(incID, "note", "CI/CD", s.buildCICDEvidence(ctx, c, run))
	s.store.AddLog(LogEntry{
		Kind: KindOperation, Level: "warning", Actor: "cicd-watch",
		Message: fmt.Sprintf("流水线失败自动登记事件 #%d：%s", incID, label),
	})
}

func cicdErrText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
