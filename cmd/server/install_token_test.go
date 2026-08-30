package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestInstallTokenRevokeAndMaxUses(t *testing.T) {
	cs := &ConfigStore{cfg: ServerConfig{InstallToken: "tok-abcdef0123456789"}}
	if !cs.ValidInstallToken("tok-abcdef0123456789") {
		t.Fatal("token should be valid initially")
	}
	cs.cfg.InstallTokenMaxUses = 1
	cs.cfg.InstallTokenUseCount = 1
	if cs.ValidInstallToken("tok-abcdef0123456789") {
		t.Fatal("token should be invalid after max uses")
	}
	cs.cfg.InstallTokenUseCount = 0
	cs.cfg.InstallTokenExpiresAt = time.Now().Add(-time.Hour).Unix()
	if cs.ValidInstallToken("tok-abcdef0123456789") {
		t.Fatal("token should be invalid when expired")
	}
	cs.cfg.InstallTokenExpiresAt = 0
	cs.cfg.InstallTokenRevoked = true
	if cs.ValidInstallToken("tok-abcdef0123456789") {
		t.Fatal("revoked token should be invalid")
	}
}

func TestTryConsumeInstallTokenUseRespectsMaxUsesAtomically(t *testing.T) {
	dir := t.TempDir()
	cs := &ConfigStore{path: filepath.Join(dir, "cfg.json"), cfg: ServerConfig{
		InstallToken:        "tok-maxuses-atomicity-012345",
		InstallTokenMaxUses: 1,
	}}
	if !cs.TryConsumeInstallTokenUse("tok-maxuses-atomicity-012345") {
		t.Fatal("first consume should succeed")
	}
	if cs.TryConsumeInstallTokenUse("tok-maxuses-atomicity-012345") {
		t.Fatal("second consume must fail once MaxUses is reached")
	}
	cs.mu.RLock()
	got := cs.cfg.InstallTokenUseCount
	cs.mu.RUnlock()
	if got != 1 {
		t.Fatalf("useCount=%d want 1", got)
	}
	// Wrong token never increments.
	if cs.TryConsumeInstallTokenUse("wrong") {
		t.Fatal("wrong token must not consume")
	}
	cs.RefundInstallTokenUse()
	cs.mu.RLock()
	got = cs.cfg.InstallTokenUseCount
	cs.mu.RUnlock()
	if got != 0 {
		t.Fatalf("after refund useCount=%d want 0", got)
	}
}


func TestMapOIDCGroupsToRole(t *testing.T) {
	c := OIDCConfig{
		GroupClaim:   "groups",
		GroupRoleMap: map[string]string{"ops-admins": RoleAdmin, "ops": RoleOperator},
		DefaultRole:  RoleViewer,
	}
	info := map[string]any{"groups": []any{"ops"}}
	if got := mapOIDCGroupsToRole(info, c); got != RoleOperator {
		t.Fatalf("got %q want operator", got)
	}
	info2 := map[string]any{"groups": []any{"other"}}
	if got := mapOIDCGroupsToRole(info2, c); got != RoleViewer {
		t.Fatalf("got %q want viewer default", got)
	}
}
