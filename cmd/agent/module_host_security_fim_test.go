package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFimAllowContentDiff(t *testing.T) {
	if !fimAllowContentDiff("/etc/hosts") {
		t.Fatal("hosts should allow")
	}
	if !fimAllowContentDiff("/etc/ssh/sshd_config") {
		t.Fatal("sshd_config should allow")
	}
	if fimAllowContentDiff("/etc/shadow") {
		t.Fatal("shadow must never content-diff")
	}
	if fimAllowContentDiff("/home/u/.ssh/id_rsa") {
		t.Fatal("private key must never content-diff")
	}
	if fimAllowContentDiff("/etc/ssl/private/server.pem") {
		t.Fatal("pem must never content-diff")
	}
	if fimAllowContentDiff("/home/u/.ssh/authorized_keys") {
		t.Fatal("authorized_keys is hash-only (no content)")
	}
}

func TestFimRedactDiff(t *testing.T) {
	in := "--- a/x\n+++ b/x\n-password=old\n+password=new\n Keep\n"
	out := fimRedactDiff(in)
	if strings.Contains(out, "password=old") || strings.Contains(out, "password=new") {
		t.Fatalf("secrets not redacted: %q", out)
	}
	if !strings.Contains(out, "***REDACTED***") {
		t.Fatalf("missing redaction marker: %q", out)
	}
	if !strings.Contains(out, " Keep") {
		t.Fatalf("context line lost: %q", out)
	}
}

func TestFimUnifiedDiffTruncation(t *testing.T) {
	var oldB, newB strings.Builder
	for i := 0; i < 300; i++ {
		oldB.WriteString("line-old-")
		oldB.WriteByte(byte('A' + (i % 26)))
		oldB.WriteByte('\n')
		newB.WriteString("line-new-")
		newB.WriteByte(byte('a' + (i % 26)))
		newB.WriteByte('\n')
	}
	diff, trunc := fimUnifiedDiff("/tmp/hosts", oldB.String(), newB.String())
	if !trunc {
		t.Fatal("expected truncation")
	}
	if strings.Count(diff, "\n") > fimMaxDiffLines+5 {
		t.Fatalf("diff too long: lines=%d", strings.Count(diff, "\n"))
	}
}

func TestFimMaybeTextDiffOnHashChange(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	_ = os.MkdirAll(cache, 0o750)
	f := filepath.Join(dir, "hosts")
	if err := os.WriteFile(f, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum1 := sha256.Sum256([]byte("127.0.0.1 localhost\n"))
	sha1 := hex.EncodeToString(sum1[:])
	if _, ok := fimMaybeTextDiff(cache, f, sha1); ok {
		t.Fatal("first snapshot should not emit diff")
	}
	if err := os.WriteFile(f, []byte("127.0.0.1 localhost\n10.0.0.1 evil\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum2 := sha256.Sum256([]byte("127.0.0.1 localhost\n10.0.0.1 evil\n"))
	sha2 := hex.EncodeToString(sum2[:])
	d, ok := fimMaybeTextDiff(cache, f, sha2)
	if !ok {
		t.Fatal("expected diff after change")
	}
	if d.OldSHA != sha1 || d.NewSHA != sha2 {
		t.Fatalf("sha mismatch old=%s new=%s", d.OldSHA, d.NewSHA)
	}
	if !strings.Contains(d.Diff, "+10.0.0.1 evil") {
		t.Fatalf("diff missing added line: %q", d.Diff)
	}
}

func TestCollectFIMInventoryRespectsDisableDiff(t *testing.T) {
	// Smoke: function returns without panic; inventory may be empty on Windows CI sandbox.
	inv, diffs := collectFIMInventory(false)
	if len(diffs) != 0 {
		t.Fatalf("diff disabled but got %d diffs", len(diffs))
	}
	_ = inv
}

func TestFimIsTextualRejectsBinary(t *testing.T) {
	if !fimIsTextual([]byte("127.0.0.1 localhost\n")) {
		t.Fatal("text should pass")
	}
	if fimIsTextual([]byte{0x00, 0x01, 0x02, 'a'}) {
		t.Fatal("NUL binary must be rejected")
	}
}

func TestFimTextCacheDirWritable(t *testing.T) {
	dir := t.TempDir()
	setFIMStateDir(filepath.Join(dir, "agent_state.json"))
	got := fimTextCacheDir()
	if !strings.HasPrefix(got, dir) {
		t.Fatalf("cache dir should prefer state dir, got %q want under %q", got, dir)
	}
	probe := filepath.Join(got, "ok")
	if err := os.WriteFile(probe, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
}
