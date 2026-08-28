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

// ValidInstallToken must reject grace-period tokens when InstallTokenRevoked is set.
// Previously only the current-token branch checked the flag, so rotate→revoke left
// the leaked prior token usable for up to tokenGracePeriod (7 days).
func TestValidInstallTokenRejectsPrevWhenRevoked(t *testing.T) {
	cs := &ConfigStore{cfg: ServerConfig{
		InstallToken:       "fresh-token-0123456789abcd",
		PrevInstallToken:   "leaked-token-0123456789ab",
		PrevTokenExpiresAt: time.Now().Add(24 * time.Hour).Unix(),
	}}
	if !cs.ValidInstallToken("leaked-token-0123456789ab") {
		t.Fatal("grace token must work before revoke flag")
	}
	cs.cfg.InstallTokenRevoked = true
	if cs.ValidInstallToken("leaked-token-0123456789ab") {
		t.Fatal("grace token must be rejected when revoked")
	}
	if cs.ValidInstallToken("fresh-token-0123456789abcd") {
		t.Fatal("current token must be rejected when revoked")
	}
}

func TestRevokeInstallTokenClearsPrevGrace(t *testing.T) {
	dir := t.TempDir()
	cs, err := NewConfigStore(filepath.Join(dir, "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	cs.cfg.InstallToken = "leaked-token-0123456789ab"
	_ = cs.ResetToken()
	prev := cs.cfg.PrevInstallToken
	if prev != "leaked-token-0123456789ab" {
		t.Fatalf("expected leaked token in grace, got %q", prev)
	}
	if !cs.ValidInstallToken(prev) {
		t.Fatal("grace token must work before revoke")
	}
	if err := cs.RevokeInstallToken(); err != nil {
		t.Fatal(err)
	}
	if cs.cfg.PrevInstallToken != "" || cs.cfg.PrevTokenExpiresAt != 0 {
		t.Fatalf("revoke must clear grace slot, prev=%q exp=%d", cs.cfg.PrevInstallToken, cs.cfg.PrevTokenExpiresAt)
	}
	if cs.ValidInstallToken(prev) {
		t.Fatal("grace token must be rejected after revoke")
	}
	if cs.ValidInstallToken(cs.cfg.InstallToken) {
		t.Fatal("current token must be rejected after revoke")
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
