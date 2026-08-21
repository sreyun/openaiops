package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// TestPBKDF2SHA256Vector checks the hand-rolled PBKDF2 against published
// PBKDF2-HMAC-SHA256 known-answer vectors (P="password", S="salt").
func TestPBKDF2SHA256Vector(t *testing.T) {
	cases := []struct {
		iter int
		want string
	}{
		{1, "120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b"},
		{2, "ae4d0c95af6b46d32d0adff928f06dd02a303f8ef3c251dfd6e2d85a95474c43"},
	}
	for _, c := range cases {
		got := hex.EncodeToString(pbkdf2SHA256([]byte("password"), []byte("salt"), c.iter, 32))
		if got != c.want {
			t.Errorf("pbkdf2 iter=%d = %s, want %s", c.iter, got, c.want)
		}
	}
}

// TestPasswordHashRoundTripAndLegacy verifies the new PBKDF2 format round-trips
// and that legacy salted-SHA256 hashes still verify (seamless migration).
func TestPasswordHashRoundTripAndLegacy(t *testing.T) {
	salt := "abcd1234abcd1234"
	h := hashPassword("s3cret", salt)
	if !strings.HasPrefix(h, "pbkdf2$sha256$") {
		t.Fatalf("new hash not pbkdf2 format: %s", h)
	}
	if !verifyPassword("s3cret", salt, h) {
		t.Error("verifyPassword rejected correct password (pbkdf2)")
	}
	if verifyPassword("wrong", salt, h) {
		t.Error("verifyPassword accepted wrong password (pbkdf2)")
	}
	// A legacy single-round salted SHA-256 hash must still verify.
	sum := sha256.Sum256([]byte(salt + ":" + "s3cret"))
	legacy := hex.EncodeToString(sum[:])
	if !verifyPassword("s3cret", salt, legacy) {
		t.Error("verifyPassword rejected correct password (legacy)")
	}
	if !isLegacyHash(legacy) || isLegacyHash(h) {
		t.Error("isLegacyHash misclassified a hash")
	}
}

// TestSessionTokensStoredHashed ensures the persisted/in-memory session map is
// keyed by a hash, never the raw cookie token that a leaked DB could replay.
func TestSessionTokensStoredHashed(t *testing.T) {
	a := NewAuth(newTestConfigStore(t))
	tok := a.issueSession("admin")
	if _, raw := a.sessions[tok]; raw {
		t.Fatal("session map is keyed by the RAW token (leaked DB would be replayable)")
	}
	if _, hashed := a.sessions[sessionKey(tok)]; !hashed {
		t.Fatal("session not stored under its hashed key")
	}
	if !a.validate(tok) {
		t.Error("validate rejected a freshly issued token")
	}
}

// newTestConfigStore builds a ConfigStore backed by a temp file so save() works
// without polluting the repo. The default config creates an admin/admin account.
func newTestConfigStore(t *testing.T) *ConfigStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test_config.json")
	cs, err := NewConfigStore(path, nil)
	if err != nil {
		t.Fatalf("NewConfigStore failed: %v", err)
	}
	return cs
}

func TestNewAuth(t *testing.T) {
	cfg := newTestConfigStore(t)
	a := NewAuth(cfg)
	if a == nil {
		t.Fatal("NewAuth returned nil")
	}
	if a.cfg != cfg {
		t.Error("Auth.cfg not set")
	}
	if len(a.sessions) != 0 {
		t.Errorf("new auth should have no sessions, got %d", len(a.sessions))
	}
}

func TestCheckPassword(t *testing.T) {
	cfg := newTestConfigStore(t)
	a := NewAuth(cfg)
	// default account is admin/admin
	t.Run("correct password", func(t *testing.T) {
		acc, ok := a.CheckPassword("admin", "admin")
		if !ok {
			t.Fatal("CheckPassword rejected correct credentials")
		}
		if acc.Username != "admin" {
			t.Errorf("unexpected username: %s", acc.Username)
		}
	})
	t.Run("incorrect password", func(t *testing.T) {
		_, ok := a.CheckPassword("admin", "wrong")
		if ok {
			t.Error("CheckPassword accepted wrong password")
		}
	})
	t.Run("unknown user", func(t *testing.T) {
		_, ok := a.CheckPassword("ghost", "whatever")
		if ok {
			t.Error("CheckPassword accepted unknown user")
		}
	})
}

func TestLoginBruteForceProtection(t *testing.T) {
	cfg := newTestConfigStore(t)
	a := NewAuth(cfg)
	ip := "10.0.0.1"
	// loginAllowed starts true.
	if !a.loginAllowed(ip) {
		t.Fatal("loginAllowed should be true initially")
	}
	// Record failures up to the limit.
	for i := 0; i < loginMaxFailures; i++ {
		a.loginFailed(ip)
	}
	// After the limit, the IP is throttled.
	if a.loginAllowed(ip) {
		t.Errorf("loginAllowed should be false after %d failures", loginMaxFailures)
	}
}

func TestSessionValidation(t *testing.T) {
	cfg := newTestConfigStore(t)
	a := NewAuth(cfg)
	t.Run("valid token", func(t *testing.T) {
		tok := a.issueSession("admin")
		if !a.validate(tok) {
			t.Error("validate rejected a freshly issued token")
		}
	})
	t.Run("empty token", func(t *testing.T) {
		if a.validate("") {
			t.Error("validate accepted an empty token")
		}
	})
	t.Run("unknown token", func(t *testing.T) {
		if a.validate("nonexistent-token") {
			t.Error("validate accepted an unknown token")
		}
	})
	t.Run("logged out token", func(t *testing.T) {
		tok := a.issueSession("admin")
		a.Logout(tok)
		if a.validate(tok) {
			t.Error("validate accepted a logged-out token")
		}
	})
}

func TestClearSessions(t *testing.T) {
	cfg := newTestConfigStore(t)
	a := NewAuth(cfg)
	a.issueSession("admin")
	a.issueSession("admin")
	a.ClearSessions()
	if len(a.sessions) != 0 {
		t.Errorf("expected no sessions after ClearSessions, got %d", len(a.sessions))
	}
}

func TestClearUserSessions(t *testing.T) {
	cfg := newTestConfigStore(t)
	a := NewAuth(cfg)
	a.issueSession("alice")
	bobTok := a.issueSession("bob")
	a.clearUserSessions("alice")
	// alice's sessions gone, bob's untouched.
	if len(a.sessions) != 1 {
		t.Fatalf("expected 1 session remaining, got %d", len(a.sessions))
	}
	if !a.validate(bobTok) {
		t.Error("bob's session should still be valid")
	}
}

// newRoleRequest builds a request with the given method + path for RBAC checks.
func newRoleRequest(method, path string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	return req
}

func TestRouteAllowed(t *testing.T) {
	s := &Server{}
	cases := []struct {
		name   string
		method string
		path   string
		role   string
		want   bool
	}{
		// own-account self-service: any logged-in role
		{"viewer can logout", "POST", "/api/v1/logout", RoleViewer, true},
		{"viewer can change password", "POST", "/api/v1/password", RoleViewer, true},
		{"viewer can setup mfa", "POST", "/api/v1/mfa/setup", RoleViewer, true},
		// reads: viewer+
		{"viewer can read hosts", "GET", "/api/v1/hosts", RoleViewer, true},
		{"viewer can read alerts", "GET", "/api/v1/alerts", RoleViewer, true},
		{"viewer cannot read content audit", "GET", "/api/v1/content-audit?host=h1", RoleViewer, false},
		{"operator can read content audit", "GET", "/api/v1/content-audit?host=h1", RoleOperator, true},
		// writes (non-users): operator+
		{"viewer cannot set category", "POST", "/api/v1/hosts/h1/category", RoleViewer, false},
		{"operator can set category", "POST", "/api/v1/hosts/h1/category", RoleOperator, true},
		{"operator can delete host", "DELETE", "/api/v1/hosts/h1", RoleOperator, true},
		// user management: admin only
		{"operator cannot list users", "GET", "/api/v1/users", RoleOperator, false},
		{"admin can list users", "GET", "/api/v1/users", RoleAdmin, true},
		{"admin can create user", "POST", "/api/v1/users", RoleAdmin, true},
		{"operator cannot delete user", "DELETE", "/api/v1/users/alice", RoleOperator, false},
		{"admin can delete user", "DELETE", "/api/v1/users/alice", RoleAdmin, true},
		// terminal: operator+
		{"viewer cannot open terminal", "GET", "/api/v1/hosts/h1/terminal", RoleViewer, false},
		{"operator can open terminal", "GET", "/api/v1/hosts/h1/terminal", RoleOperator, true},
		// sensitive settings: admin only (writes)
		{"operator cannot POST config", "POST", "/api/v1/config", RoleOperator, false},
		{"admin can POST config", "POST", "/api/v1/config", RoleAdmin, true},
		{"operator can GET config", "GET", "/api/v1/config", RoleOperator, true},
		{"operator cannot test config", "POST", "/api/v1/config/test", RoleOperator, false},
		{"admin can test config", "POST", "/api/v1/config/test", RoleAdmin, true},
		{"operator cannot POST ai config", "POST", "/api/v1/ai/config", RoleOperator, false},
		{"admin can POST ai config", "POST", "/api/v1/ai/config", RoleAdmin, true},
		{"operator can GET ai config", "GET", "/api/v1/ai/config", RoleOperator, true},
		{"operator cannot ai test", "POST", "/api/v1/ai/test", RoleOperator, false},
		{"admin can ai test", "POST", "/api/v1/ai/test", RoleAdmin, true},
		{"admin can list backups", "GET", "/api/v1/admin/backups", RoleAdmin, true},
		{"operator cannot list backups", "GET", "/api/v1/admin/backups", RoleOperator, false},
		// agent fleet update: operator+; auto-update policy write: admin
		{"viewer cannot start agent update", "POST", "/api/v1/agents/update", RoleViewer, false},
		{"operator can start agent update", "POST", "/api/v1/agents/update", RoleOperator, true},
		{"operator can get auto-update policy", "GET", "/api/v1/agents/auto-update-policy", RoleOperator, true},
		{"operator cannot set auto-update policy", "POST", "/api/v1/agents/auto-update-policy", RoleOperator, false},
		{"admin can set auto-update policy", "POST", "/api/v1/agents/auto-update-policy", RoleAdmin, true},
		// SQL 工作台：只读查询（含流式）viewer+ 就行；**批量导出是数据出境**，拔高到
		// operator+ 并留痕。这条曾经踩过：新增 /query/stream、/query/export 之后，
		// 它们不再以 "/query" 结尾，直接掉进了兜底的 admin 分支——功能对普通用户全哑。
		{"viewer can run query", "POST", "/api/v1/sql/connections/c1/query", RoleViewer, true},
		{"viewer can stream query", "POST", "/api/v1/sql/connections/c1/query/stream", RoleViewer, true},
		{"viewer cannot export", "POST", "/api/v1/sql/connections/c1/query/export", RoleViewer, false},
		{"operator can export", "POST", "/api/v1/sql/connections/c1/query/export", RoleOperator, true},
		{"viewer cannot exec ddl", "POST", "/api/v1/sql/connections/c1/exec-ddl", RoleViewer, false},
		{"viewer cannot create connection", "POST", "/api/v1/sql/connections", RoleViewer, false},
		{"operator cannot create connection", "POST", "/api/v1/sql/connections", RoleOperator, false},
		// unknown role
		{"unknown role denied", "GET", "/api/v1/hosts", "weird", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := newRoleRequest(tc.method, tc.path)
			if got := s.routeAllowed(req, tc.role); got != tc.want {
				t.Errorf("routeAllowed(%s %s, role=%s) = %v, want %v",
					tc.method, tc.path, tc.role, got, tc.want)
			}
		})
	}
}

func TestValidateRequest(t *testing.T) {
	cfg := newTestConfigStore(t)
	a := NewAuth(cfg)
	t.Run("request with valid cookie", func(t *testing.T) {
		tok := a.issueSession("admin")
		req := newRoleRequest("GET", "/api/v1/hosts")
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
		if !a.ValidateRequest(req) {
			t.Error("ValidateRequest rejected a valid session cookie")
		}
	})
	t.Run("request without cookie", func(t *testing.T) {
		req := newRoleRequest("GET", "/api/v1/hosts")
		if a.ValidateRequest(req) {
			t.Error("ValidateRequest accepted a request without a cookie")
		}
	})
}
