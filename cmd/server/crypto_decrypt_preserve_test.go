package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDecryptFailurePreservesCiphertextOnSave reproduces the wipe: encrypt with
// a master key, restart without the key (or with a wrong key), run a dirty
// config save. Before the fix, decryptSecret blanked fields and save() persisted
// empty secrets permanently.
func TestDecryptFailurePreservesCiphertextOnSave(t *testing.T) {
	t.Setenv("AIOPS_SECRET_KEY", "correct-master-key-for-preserve-test")
	t.Setenv("AIOPS_SECRET_KEY_ID", "k1")
	t.Setenv("AIOPS_SECRET_KEYS_PREV", "")

	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.json")
	cs, err := NewConfigStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs.mu.Lock()
	cs.cfg.SMTP.Password = "smtp-real-password"
	cs.cfg.AI.APIKey = "sk-real-ai-key"
	cs.cfg.OIDC.ClientSecret = "oidc-real-secret"
	cs.cfg.MySQLConnections = []MySQLConnection{{
		ID: "db1", Name: "prod", Host: "10.0.0.1", Password: "mysql-real-pw", Enabled: true,
	}}
	cs.mu.Unlock()
	if err := cs.save(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk ServerConfig
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	if !isEncryptedSecret(onDisk.SMTP.Password) || !isEncryptedSecret(onDisk.AI.APIKey) {
		t.Fatalf("expected encrypted secrets on disk, got smtp=%q ai=%q", onDisk.SMTP.Password, onDisk.AI.APIKey)
	}
	encSMTP, encAI, encOIDC := onDisk.SMTP.Password, onDisk.AI.APIKey, onDisk.OIDC.ClientSecret
	encMySQL := ""
	if len(onDisk.MySQLConnections) == 1 {
		encMySQL = onDisk.MySQLConnections[0].Password
	}
	if !isEncryptedSecret(encMySQL) {
		t.Fatalf("expected encrypted MySQL password, got %q", encMySQL)
	}

	// Simulate restart with missing / wrong key + a dirty migration that saves.
	t.Setenv("AIOPS_SECRET_KEY", "")
	t.Setenv("AIOPS_SECRET_KEY_ID", "")
	cs2, err := NewConfigStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Force dirty save the way threshold backfill / agent_auto_update heal does.
	cs2.mu.Lock()
	cs2.cfg.AgentAutoUpdate = !cs2.cfg.AgentAutoUpdate
	cs2.mu.Unlock()
	if err := cs2.save(); err != nil {
		t.Fatal(err)
	}

	raw2, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var after ServerConfig
	if err := json.Unmarshal(raw2, &after); err != nil {
		t.Fatal(err)
	}
	if after.SMTP.Password != encSMTP {
		t.Fatalf("SMTP password wiped/changed after decrypt-fail save: got %q want ciphertext %q", after.SMTP.Password, encSMTP)
	}
	if after.AI.APIKey != encAI {
		t.Fatalf("AI API key wiped/changed after decrypt-fail save: got %q want %q", after.AI.APIKey, encAI)
	}
	if after.OIDC.ClientSecret != encOIDC {
		t.Fatalf("OIDC secret wiped/changed: got %q want %q", after.OIDC.ClientSecret, encOIDC)
	}
	if len(after.MySQLConnections) != 1 || after.MySQLConnections[0].Password != encMySQL {
		t.Fatalf("MySQL password wiped/changed: %+v", after.MySQLConnections)
	}

	// Wrong key must also preserve ciphertext (not blank).
	t.Setenv("AIOPS_SECRET_KEY", "totally-wrong-key")
	t.Setenv("AIOPS_SECRET_KEY_ID", "k1")
	if got := decryptSecret(encSMTP); got != encSMTP {
		t.Fatalf("wrong-key decrypt must keep ciphertext, got %q", got)
	}
	if !strings.HasPrefix(decryptSecret(encAI), "enc:") {
		t.Fatalf("wrong-key decrypt must keep enc: prefix, got %q", decryptSecret(encAI))
	}

	// Correct key still recovers plaintext.
	t.Setenv("AIOPS_SECRET_KEY", "correct-master-key-for-preserve-test")
	t.Setenv("AIOPS_SECRET_KEY_ID", "k1")
	if got := decryptSecret(encSMTP); got != "smtp-real-password" {
		t.Fatalf("correct key should decrypt, got %q", got)
	}
}

func TestDecryptSecretV2FailurePreservesCiphertext(t *testing.T) {
	t.Setenv("AIOPS_SECRET_KEY", "v2-preserve-key")
	t.Setenv("AIOPS_SECRET_KEY_ID", "kidA")
	plain := "keep-me"
	enc := encryptSecret(plain)
	if !strings.HasPrefix(enc, "enc:v2:kidA:") {
		t.Fatalf("expected v2 ciphertext, got %q", enc)
	}
	t.Setenv("AIOPS_SECRET_KEY", "other-key")
	t.Setenv("AIOPS_SECRET_KEY_ID", "kidB")
	if got := decryptSecret(enc); got != enc {
		t.Fatalf("v2 decrypt failure must return original ciphertext, got %q", got)
	}
}
