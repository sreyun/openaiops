package main

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"aiops-monitor/shared"
)

// decodeAgentReport mirrors the server side: reports above 512 bytes arrive
// gzip-compressed (serverTarget.send), so a test handler must un-gzip first.
func decodeAgentReport(t *testing.T, r *http.Request) shared.Report {
	t.Helper()
	var body io.Reader = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Fatalf("gzip: %v", err)
		}
		defer zr.Close()
		body = zr
	}
	var rep shared.Report
	if err := json.NewDecoder(body).Decode(&rep); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	return rep
}

// Multi-panel deployments: each server independently claims a machine by
// fingerprint, so two panels can legitimately know the same host by different
// ids (classic trigger: the agent was reinstalled and got a fresh random id,
// panel A stored the new one while panel B rebound to its pre-existing record).
// reconcileIdentity deliberately refuses to adopt one panel's id globally, so
// every per-target request has to carry that panel's own id — otherwise
// UpsertAuthenticated (strict lookup by host_id) answers 403 forever and the
// host sits permanently offline on that panel, terminal/desktop included.

func newTestTarget(server string) *serverTarget {
	return &serverTarget{
		server: server,
		httpc:  &http.Client{Timeout: 5 * time.Second},
		bo:     newBackoff(time.Millisecond, 10*time.Millisecond),
		cb:     newCircuitBreaker(8, time.Second),
	}
}

func TestRegisterAdoptsPerTargetCanonicalHostID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"host_id": "panel-b-id"})
	}))
	defer srv.Close()

	tgt := newTestTarget(srv.URL)
	if !tgt.register(shared.Report{HostID: "local-id", Fingerprint: "fp"}) {
		t.Fatal("register failed")
	}
	if got := tgt.hostIDOr("local-id"); got != "panel-b-id" {
		t.Fatalf("hostIDOr = %q, want the canonical id returned by this panel", got)
	}
	// A different panel that never answered keeps the local id.
	other := newTestTarget("http://unused.invalid")
	if got := other.hostIDOr("local-id"); got != "local-id" {
		t.Fatalf("unregistered target hostIDOr = %q, want local fallback", got)
	}
}

// A register response we cannot decode must not wipe an id we are already
// reporting under (that would resurrect the 403 loop on the next cycle).
func TestRegisterKeepsCanonicalHostIDOnUndecodableBody(t *testing.T) {
	var withID bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if withID {
			_ = json.NewEncoder(w).Encode(map[string]string{"host_id": "panel-b-id"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not-json"))
	}))
	defer srv.Close()

	tgt := newTestTarget(srv.URL)
	withID = true
	tgt.register(shared.Report{HostID: "local-id", Fingerprint: "fp"})
	withID = false
	tgt.register(shared.Report{HostID: "local-id", Fingerprint: "fp"})
	if got := tgt.hostIDOr("local-id"); got != "panel-b-id" {
		t.Fatalf("hostIDOr = %q, want the previously learned canonical id", got)
	}
}

// The report body must carry this panel's id, not the agent's local one.
func TestSendUsesPerTargetHostID(t *testing.T) {
	var mu sync.Mutex
	var gotHostID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/agent/register" {
			_ = json.NewEncoder(w).Encode(map[string]string{"host_id": "panel-b-id"})
			return
		}
		rep := decodeAgentReport(t, r)
		mu.Lock()
		gotHostID = rep.HostID
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tgt := newTestTarget(srv.URL)
	tgt.register(shared.Report{HostID: "local-id", Fingerprint: "fp"})
	if err := tgt.send(shared.Report{HostID: "local-id", Fingerprint: "fp"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotHostID != "panel-b-id" {
		t.Fatalf("report carried host_id=%q, want panel-b-id (403 loop otherwise)", gotHostID)
	}
}

// Host.ServerURL is what the server hands back as the /dl base for self-update.
// Reporting servers[0] to every panel would make panel B push an upgrade that
// downloads from panel A — the wrong artifact, and unreachable when the panels
// sit on different networks. In relay mode it must be the relay's own URL.
func TestSendReportsPerTargetServerURL(t *testing.T) {
	var mu sync.Mutex
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rep := decodeAgentReport(t, r)
		mu.Lock()
		got = rep.ServerURL
		mu.Unlock()
	}))
	defer srv.Close()

	tgt := newTestTarget(srv.URL + "/")
	// identity.ServerURL is built from servers[0]; this target is a different one.
	if err := tgt.send(shared.Report{HostID: "id", ServerURL: "https://panel-a.example"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got != srv.URL {
		t.Fatalf("report carried server_url=%q, want this target's own %q", got, srv.URL)
	}
}

// Hyper-V / container inventory must remap host_id per panel the same way
// hardware/netflow already do. A shared local-id body is rejected forever by
// forwardFingerprintOKByHost when the panel rebound the fingerprint to an older id.
func TestHyperVAndContainerReportsUsePerTargetHostID(t *testing.T) {
	var mu sync.Mutex
	got := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		switch r.URL.Path {
		case "/api/v1/agent/hyperv":
			var rep shared.HyperVReport
			if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
				t.Errorf("hyperv decode: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			mu.Lock()
			got["hyperv"] = rep.HostID
			mu.Unlock()
		case "/api/v1/agent/containers":
			var rep shared.ContainerReport
			if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
				t.Errorf("containers decode: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			mu.Lock()
			got["containers"] = rep.HostID
			mu.Unlock()
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tgt := newTestTarget(srv.URL)
	tgt.regMu.Lock()
	tgt.canonicalHostID = "panel-b-id"
	tgt.regMu.Unlock()

	a := &Agent{
		identity: shared.Report{HostID: "local-id", Fingerprint: "fp"},
		targets:  []*serverTarget{tgt},
	}
	a.postHyperVReport(shared.HyperVReport{HostID: "local-id", Guests: nil})
	a.postContainerReport(shared.ContainerReport{HostID: "local-id"})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := got["hyperv"] != "" && got["containers"] != ""
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if got["hyperv"] != "panel-b-id" {
		t.Fatalf("hyperv host_id=%q, want panel-b-id", got["hyperv"])
	}
	if got["containers"] != "panel-b-id" {
		t.Fatalf("containers host_id=%q, want panel-b-id", got["containers"])
	}
}

// terminal / desktop / forward long-polls key off host_id too — a wrong id
// means the panel's session request is never picked up by this agent.
func TestWaitChannelsUsePerTargetHostID(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path] = r.URL.Query().Get("host")
		mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	tgt := newTestTarget(srv.URL)
	tgt.regMu.Lock()
	tgt.canonicalHostID = "panel-b-id"
	tgt.regMu.Unlock()

	a := &Agent{identity: shared.Report{HostID: "local-id", Fingerprint: "fp"}}
	a.termWait(tgt)
	a.deskWait(tgt)
	a.forwardWait(tgt)

	mu.Lock()
	defer mu.Unlock()
	for _, p := range []string{
		"/api/v1/agent/terminal/wait",
		"/api/v1/agent/desktop/wait",
		"/api/v1/agent/forward/wait",
	} {
		if seen[p] != "panel-b-id" {
			t.Errorf("%s waited with host=%q, want panel-b-id", p, seen[p])
		}
	}
}
