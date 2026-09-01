package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression: GET /admin/backup-config omitted include_vm / include_recordings /
// vm_days. The settings UI loads those fields into checkboxes and POSTs the
// whole BackupConfig on any save — missing keys became false and wiped scheduled
// VictoriaMetrics + recordings disaster recovery.
func TestHandleGetBackupCfgIncludesFullBackupFlags(t *testing.T) {
	dir := t.TempDir()
	cfg, err := NewConfigStore(filepath.Join(dir, "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.SetBackupCfg(BackupConfig{
		Enabled: true, DailyAt: "03:00", RetainCount: 7,
		IncludeVM: true, VMDays: 42, IncludeRecordings: true,
	}); err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: cfg}
	rr := httptest.NewRecorder()
	s.handleGetBackupCfg(rr, httptest.NewRequest(http.MethodGet, "/api/v1/admin/backup-config", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["include_vm"] != true {
		t.Fatalf("include_vm=%v want true (UI would otherwise POST false and wipe it)", got["include_vm"])
	}
	if got["include_recordings"] != true {
		t.Fatalf("include_recordings=%v want true", got["include_recordings"])
	}
	if int(got["vm_days"].(float64)) != 42 {
		t.Fatalf("vm_days=%v want 42", got["vm_days"])
	}
}

func TestCreateRecordingsBackupSkipsUnreadableWithoutCorruptingTar(t *testing.T) {
	dir := t.TempDir()
	termDir := filepath.Join(dir, "term")
	if err := os.MkdirAll(termDir, 0o750); err != nil {
		t.Fatal(err)
	}
	goodPath := filepath.Join(termDir, "ok.cast")
	if err := os.WriteFile(goodPath, []byte("session-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	gonePath := filepath.Join(termDir, "gone.cast")
	if err := os.WriteFile(gonePath, []byte("will-vanish"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(dir, "cfg.json")
	cfg, err := NewConfigStore(cfgPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	bakDir := filepath.Join(dir, "backups")
	if err := cfg.SetBackupCfg(BackupConfig{Dir: bakDir, RetainCount: 5}); err != nil {
		t.Fatal(err)
	}
	s := &Server{
		cfg:  cfg,
		term: &termManager{recDir: termDir},
	}

	// Simulate race: Walk sees gone.cast via Stat, then we remove it before Open.
	// The Walk callback opens each file itself; delete between directory read and
	// Open by removing after Walk starts — use a named pipe or chmod-denied file
	// instead so Open fails while the path still appears in Readdir.
	unreadable := filepath.Join(termDir, "locked.cast")
	if err := os.WriteFile(unreadable, []byte("secret-audit"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o600) })
	_ = os.Remove(gonePath)

	meta, err := s.createRecordingsBackup("test", "unit")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(meta.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var names []string
	var payloads []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("corrupt tar: %v", err)
		}
		names = append(names, hdr.Name)
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read %s: %v", hdr.Name, err)
		}
		if int64(len(body)) != hdr.Size {
			t.Fatalf("%s: got %d bytes want hdr.Size=%d (header/body skew)", hdr.Name, len(body), hdr.Size)
		}
		payloads = append(payloads, string(body))
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "terminal/ok.cast") {
		t.Fatalf("missing good file; names=%v", names)
	}
	for _, p := range payloads {
		if !bytes.Equal([]byte(p), []byte("session-bytes")) && p != "" {
			// Only the readable session should be present with its exact bytes.
			if p == "secret-audit" || p == "will-vanish" {
				t.Fatalf("unreadable/deleted payload leaked into archive: %q", p)
			}
		}
	}
	for i, n := range names {
		if strings.HasSuffix(n, "ok.cast") && payloads[i] != "session-bytes" {
			t.Fatalf("ok.cast payload corrupted: %q", payloads[i])
		}
	}
}
