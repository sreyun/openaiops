package main

import (
	"crypto/rand"
	"fmt"
	"strings"
	"time"
)

// ============================================================================
// Multi-user accounts + RBAC
//
// The dashboard supports multiple login accounts, each with a role. Roles are
// ranked; a request is allowed when the caller's rank meets the route's minimum.
//   admin    — full access, including user management
//   operator — every write/action except user management
//   viewer   — read-only (plus managing their own profile / password / MFA)
// ============================================================================

const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleViewer   = "viewer"
)

// roleRank maps a role to a privilege level (higher = more). Unknown → 0.
func roleRank(role string) int {
	switch role {
	case RoleAdmin:
		return 3
	case RoleOperator:
		return 2
	case RoleViewer:
		return 1
	default:
		return 0
	}
}

func validRole(role string) bool {
	return role == RoleAdmin || role == RoleOperator || role == RoleViewer
}

// migrateUsers upgrades a legacy single-account config to the Users list and
// enforces "at least one admin exists". Returns true if it changed anything.
func migrateUsers(c *ServerConfig) bool {
	changed := false
	if len(c.Users) == 0 {
		acc := c.Account
		if acc.Username == "" {
			acc = defaultAccount()
		}
		acc.Role = RoleAdmin
		c.Users = []AccountConfig{acc}
		changed = true
	}
	hasAdmin := false
	for i := range c.Users {
		if !validRole(c.Users[i].Role) {
			c.Users[i].Role = RoleViewer
			changed = true
		}
		if c.Users[i].Role == RoleAdmin {
			hasAdmin = true
		}
	}
	if !hasAdmin && len(c.Users) > 0 {
		c.Users[0].Role = RoleAdmin
		changed = true
	}
	// Drop the deprecated single-account mirror so credentials live in one place.
	if c.Account.Username != "" {
		c.Account = AccountConfig{}
		changed = true
	}
	return changed
}

// ---- per-user accessors on ConfigStore ----

// UsersList returns a copy of all users. Secret fields are included; any caller
// serializing to the browser MUST strip salt/hash/mfa_secret first.
func (cs *ConfigStore) UsersList() []AccountConfig {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	out := make([]AccountConfig, len(cs.cfg.Users))
	copy(out, cs.cfg.Users)
	return out
}

// UserByName returns the user with the exact username, and whether it was found.
func (cs *ConfigStore) UserByName(name string) (AccountConfig, bool) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	for _, u := range cs.cfg.Users {
		if u.Username == name {
			return u, true
		}
	}
	return AccountConfig{}, false
}

// UserByIdentity returns the user bound to (provider, subject).
func (cs *ConfigStore) UserByIdentity(provider, subject string) (AccountConfig, bool) {
	provider = strings.TrimSpace(strings.ToLower(provider))
	subject = strings.TrimSpace(subject)
	if provider == "" || subject == "" {
		return AccountConfig{}, false
	}
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	for _, u := range cs.cfg.Users {
		for _, id := range u.Identities {
			if strings.EqualFold(id.Provider, provider) && id.Subject == subject {
				return u, true
			}
		}
	}
	return AccountConfig{}, false
}

// BindUserIdentity attaches (provider, subject) to username if not already bound.
// Fails if the subject is already linked to a different user.
func (cs *ConfigStore) BindUserIdentity(username, provider, subject string) error {
	provider = strings.TrimSpace(strings.ToLower(provider))
	subject = strings.TrimSpace(subject)
	if username == "" || provider == "" || subject == "" {
		return fmt.Errorf("invalid identity")
	}
	cs.mu.Lock()
	for _, u := range cs.cfg.Users {
		for _, id := range u.Identities {
			if strings.EqualFold(id.Provider, provider) && id.Subject == subject && u.Username != username {
				cs.mu.Unlock()
				return fmt.Errorf("identity already bound")
			}
		}
	}
	i := cs.findLocked(username)
	if i < 0 {
		cs.mu.Unlock()
		return fmt.Errorf("%s", Tz("user.not_found"))
	}
	for _, id := range cs.cfg.Users[i].Identities {
		if strings.EqualFold(id.Provider, provider) && id.Subject == subject {
			cs.mu.Unlock()
			return nil
		}
	}
	cs.cfg.Users[i].Identities = append(cs.cfg.Users[i].Identities, ExternalIdentity{
		Provider: provider, Subject: subject, BoundAt: time.Now().Unix(),
	})
	cs.mu.Unlock()
	return cs.save()
}

// UnbindUserIdentity removes all identities for provider from username.
func (cs *ConfigStore) UnbindUserIdentity(username, provider string) error {
	provider = strings.TrimSpace(strings.ToLower(provider))
	if username == "" || provider == "" {
		return fmt.Errorf("invalid identity")
	}
	cs.mu.Lock()
	i := cs.findLocked(username)
	if i < 0 {
		cs.mu.Unlock()
		return fmt.Errorf("%s", Tz("user.not_found"))
	}
	ids := cs.cfg.Users[i].Identities
	out := make([]ExternalIdentity, 0, len(ids))
	found := false
	for _, id := range ids {
		if strings.EqualFold(id.Provider, provider) {
			found = true
			continue
		}
		out = append(out, id)
	}
	if !found {
		cs.mu.Unlock()
		return fmt.Errorf("identity not bound")
	}
	cs.cfg.Users[i].Identities = out
	cs.mu.Unlock()
	return cs.save()
}

// UserByEmail returns the first user whose email matches (case-insensitive).
func (cs *ConfigStore) UserByEmail(email string) (AccountConfig, bool) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	for _, u := range cs.cfg.Users {
		if u.Email != "" && strings.EqualFold(u.Email, email) {
			return u, true
		}
	}
	return AccountConfig{}, false
}

// UserByPhone returns the first user whose phone number matches (digits-normalized).
func (cs *ConfigStore) UserByPhone(phone string) (AccountConfig, bool) {
	phone = normalizePhoneDigits(phone)
	if phone == "" {
		return AccountConfig{}, false
	}
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	for _, u := range cs.cfg.Users {
		if u.Phone != "" && normalizePhoneDigits(u.Phone) == phone {
			return u, true
		}
	}
	return AccountConfig{}, false
}

// countUsersByPhoneLocked returns how many accounts share the normalized phone.
// Caller must hold cs.mu (read or write).
func (cs *ConfigStore) countUsersByPhoneLocked(phone string) int {
	phone = normalizePhoneDigits(phone)
	if phone == "" {
		return 0
	}
	n := 0
	for _, u := range cs.cfg.Users {
		if u.Phone != "" && normalizePhoneDigits(u.Phone) == phone {
			n++
		}
	}
	return n
}

// RoleOf returns a user's role, or "" if the user doesn't exist.
func (cs *ConfigStore) RoleOf(name string) string {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	for _, u := range cs.cfg.Users {
		if u.Username == name {
			return u.Role
		}
	}
	return ""
}

// callers below must hold cs.mu.
func (cs *ConfigStore) findLocked(name string) int {
	for i := range cs.cfg.Users {
		if cs.cfg.Users[i].Username == name {
			return i
		}
	}
	return -1
}
func (cs *ConfigStore) adminCountLocked() int {
	n := 0
	for _, u := range cs.cfg.Users {
		if u.Role == RoleAdmin {
			n++
		}
	}
	return n
}

// CreateUser adds a new user with the given password. Fails if the name exists.
func (cs *ConfigStore) CreateUser(username, password, displayName, email, role string) error {
	cs.mu.Lock()
	if cs.findLocked(username) >= 0 {
		cs.mu.Unlock()
		return fmt.Errorf("%s", Tz("user.username_exists"))
	}
	salt := genToken()[:16]
	cs.cfg.Users = append(cs.cfg.Users, AccountConfig{
		Username: username, DisplayName: displayName, Email: email,
		Salt: salt, Hash: hashPassword(password, salt), Role: role,
	})
	cs.mu.Unlock()
	return cs.save()
}

// UpdateUserMeta changes a user's display name, email and role (admin action).
// Refuses to demote the last remaining admin.
func (cs *ConfigStore) UpdateUserMeta(username, displayName, email, role string) error {
	cs.mu.Lock()
	i := cs.findLocked(username)
	if i < 0 {
		cs.mu.Unlock()
		return fmt.Errorf("%s", Tz("user.not_found"))
	}
	if cs.cfg.Users[i].Role == RoleAdmin && role != RoleAdmin && cs.adminCountLocked() <= 1 {
		cs.mu.Unlock()
		return fmt.Errorf("%s", Tz("user.keep_one_admin"))
	}
	cs.cfg.Users[i].DisplayName = displayName
	cs.cfg.Users[i].Email = email
	cs.cfg.Users[i].Role = role
	cs.mu.Unlock()
	return cs.save()
}

// UpdateUserHostScope sets host-group / tag RBAC scope (empty = unrestricted).
func (cs *ConfigStore) UpdateUserHostScope(username string, folders, hosts, tags []string) error {
	cs.mu.Lock()
	i := cs.findLocked(username)
	if i < 0 {
		cs.mu.Unlock()
		return fmt.Errorf("%s", Tz("user.not_found"))
	}
	cs.cfg.Users[i].AllowedFolderIDs = folders
	cs.cfg.Users[i].AllowedHostIDs = hosts
	cs.cfg.Users[i].AllowedTags = tags
	cs.mu.Unlock()
	return cs.save()
}

// SetUserPassword sets a user's password (self change-password or admin reset).
// v5.4.0: also clears the MustChangePassword flag since the password is being changed.
func (cs *ConfigStore) SetUserPassword(username, password string) error {
	cs.mu.Lock()
	i := cs.findLocked(username)
	if i < 0 {
		cs.mu.Unlock()
		return fmt.Errorf("%s", Tz("user.not_found"))
	}
	salt := genToken()[:16]
	cs.cfg.Users[i].Salt = salt
	cs.cfg.Users[i].Hash = hashPassword(password, salt)
	cs.cfg.Users[i].MustChangePassword = false
	cs.mu.Unlock()
	return cs.save()
}

// upgradeLoginHash re-hashes a user's login password with the current KDF,
// reusing the existing per-user salt. Called after a successful login when the
// stored hash is still in the legacy SHA-256 format, so existing accounts
// migrate to PBKDF2 transparently.
func (cs *ConfigStore) upgradeLoginHash(username, pass string) error {
	cs.mu.Lock()
	i := cs.findLocked(username)
	if i < 0 {
		cs.mu.Unlock()
		return fmt.Errorf("%s", Tz("user.not_found"))
	}
	if !isLegacyHash(cs.cfg.Users[i].Hash) {
		cs.mu.Unlock()
		return nil // already upgraded by a concurrent login
	}
	cs.cfg.Users[i].Hash = hashPassword(pass, cs.cfg.Users[i].Salt)
	cs.mu.Unlock()
	return cs.save()
}

// SetMustChangePassword sets the MustChangePassword flag for a user, forcing
// a password change on the next login. Used when default credentials are
// detected during login (v5.4.0).
func (cs *ConfigStore) SetMustChangePassword(username string) {
	cs.mu.Lock()
	i := cs.findLocked(username)
	if i >= 0 {
		cs.cfg.Users[i].MustChangePassword = true
	}
	cs.mu.Unlock()
	_ = cs.save()
}

// ClearMustChangePassword clears the MustChangePassword flag for a user.
// v5.4.0: called after a successful self password change.
func (cs *ConfigStore) ClearMustChangePassword(username string) {
	cs.mu.Lock()
	i := cs.findLocked(username)
	if i >= 0 {
		cs.cfg.Users[i].MustChangePassword = false
	}
	cs.mu.Unlock()
	_ = cs.save()
}

// SetUserProfile updates a user's own display name + email + phone.
// Phone numbers must be unique across accounts and must not equal another
// account's username (phone-shaped usernames would otherwise be shadowable
// via the unified username-or-phone login path / SMS OTP binding).
func (cs *ConfigStore) SetUserProfile(username, displayName, email, phone string) error {
	phone = strings.TrimSpace(phone)
	normPhone := normalizePhoneDigits(phone)
	cs.mu.Lock()
	i := cs.findLocked(username)
	if i < 0 {
		cs.mu.Unlock()
		return fmt.Errorf("%s", Tz("user.not_found"))
	}
	if normPhone != "" {
		for j, u := range cs.cfg.Users {
			if j == i {
				continue
			}
			if u.Phone != "" && normalizePhoneDigits(u.Phone) == normPhone {
				cs.mu.Unlock()
				return fmt.Errorf("%s", Tz("user.phone_in_use"))
			}
			if u.Username == normPhone || u.Username == phone {
				cs.mu.Unlock()
				return fmt.Errorf("%s", Tz("user.phone_username_conflict"))
			}
		}
	}
	cs.cfg.Users[i].DisplayName = displayName
	cs.cfg.Users[i].Email = email
	cs.cfg.Users[i].Phone = phone
	cs.mu.Unlock()
	return cs.save()
}

// SetTerminalPassword sets (or changes) the terminal secondary password.
// v5.3.0: terminal password uses the same salted SHA-256 scheme as login password.
func (cs *ConfigStore) SetTerminalPassword(username, password string) error {
	cs.mu.Lock()
	i := cs.findLocked(username)
	if i < 0 {
		cs.mu.Unlock()
		return fmt.Errorf("%s", Tz("user.not_found"))
	}
	salt := genToken()[:16]
	cs.cfg.Users[i].TerminalPasswordSalt = salt
	cs.cfg.Users[i].TerminalPasswordHash = hashPassword(password, salt)
	cs.mu.Unlock()
	return cs.save()
}

// VerifyTerminalPassword checks the terminal secondary password.
// Returns true if the password matches.
func (cs *ConfigStore) VerifyTerminalPassword(username, password string) bool {
	cs.mu.Lock()
	i := cs.findLocked(username)
	if i < 0 {
		cs.mu.Unlock()
		return false
	}
	hash := cs.cfg.Users[i].TerminalPasswordHash
	salt := cs.cfg.Users[i].TerminalPasswordSalt
	cs.mu.Unlock()
	if hash == "" || salt == "" {
		return false
	}
	return verifyPassword(password, salt, hash)
}

// HasTerminalPassword reports whether the user has set a terminal password.
func (cs *ConfigStore) HasTerminalPassword(username string) bool {
	cs.mu.Lock()
	i := cs.findLocked(username)
	if i < 0 {
		cs.mu.Unlock()
		return false
	}
	has := cs.cfg.Users[i].TerminalPasswordHash != ""
	cs.mu.Unlock()
	return has
}

// RenameUser changes a user's login name. Fails if the new name is taken.
func (cs *ConfigStore) RenameUser(oldName, newName string) error {
	cs.mu.Lock()
	if oldName != newName && cs.findLocked(newName) >= 0 {
		cs.mu.Unlock()
		return fmt.Errorf("%s", Tz("user.username_exists"))
	}
	i := cs.findLocked(oldName)
	if i < 0 {
		cs.mu.Unlock()
		return fmt.Errorf("%s", Tz("user.not_found"))
	}
	cs.cfg.Users[i].Username = newName
	cs.mu.Unlock()
	return cs.save()
}

// SetUserMFA enables/disables a user's TOTP factor (disabling clears the secret).
func (cs *ConfigStore) SetUserMFA(username string, enabled bool, secret string) error {
	cs.mu.Lock()
	i := cs.findLocked(username)
	if i < 0 {
		cs.mu.Unlock()
		return fmt.Errorf("%s", Tz("user.not_found"))
	}
	cs.cfg.Users[i].MFAEnabled = enabled
	if enabled {
		cs.cfg.Users[i].MFASecret = secret
	} else {
		cs.cfg.Users[i].MFASecret = ""
	}
	cs.mu.Unlock()
	return cs.save()
}

// DeleteUser removes a user. Refuses to delete the last admin or the last user.
func (cs *ConfigStore) DeleteUser(username string) error {
	cs.mu.Lock()
	i := cs.findLocked(username)
	if i < 0 {
		cs.mu.Unlock()
		return fmt.Errorf("%s", Tz("user.not_found"))
	}
	if len(cs.cfg.Users) <= 1 {
		cs.mu.Unlock()
		return fmt.Errorf("%s", Tz("user.keep_one_user"))
	}
	if cs.cfg.Users[i].Role == RoleAdmin && cs.adminCountLocked() <= 1 {
		cs.mu.Unlock()
		return fmt.Errorf("%s", Tz("user.keep_one_admin"))
	}
	cs.cfg.Users = append(cs.cfg.Users[:i], cs.cfg.Users[i+1:]...)
	cs.mu.Unlock()
	return cs.save()
}

// ResetAdminMFA clears TOTP for the first admin (or the named user). Used when
// the authenticator is lost/desynced and login is blocked. Returns the username.
func (cs *ConfigStore) ResetAdminMFA(username string) (string, error) {
	cs.mu.Lock()
	idx := -1
	want := strings.TrimSpace(username)
	for i := range cs.cfg.Users {
		u := cs.cfg.Users[i]
		if want != "" {
			if strings.EqualFold(u.Username, want) {
				idx = i
				break
			}
			continue
		}
		if u.Role == RoleAdmin {
			idx = i
			break
		}
	}
	if idx < 0 {
		cs.mu.Unlock()
		if want != "" {
			return "", fmt.Errorf("user %q not found", want)
		}
		return "", fmt.Errorf("no admin user found in config")
	}
	name := cs.cfg.Users[idx].Username
	cs.cfg.Users[idx].MFAEnabled = false
	cs.cfg.Users[idx].MFASecret = ""
	cs.mu.Unlock()
	if err := cs.save(); err != nil {
		return "", err
	}
	return name, nil
}

// ResetAdminPassword resets the password of the first admin user to a random
// value, forces a password change on next login, and returns the username and
// new plaintext password. Returns an error when no admin user exists.
// v5.4.0: admin password recovery via CLI / local API.
func (cs *ConfigStore) ResetAdminPassword() (string, string, error) {
	cs.mu.Lock()
	// Find the first admin user
	adminIdx := -1
	for i := range cs.cfg.Users {
		if cs.cfg.Users[i].Role == RoleAdmin {
			adminIdx = i
			break
		}
	}
	if adminIdx < 0 {
		cs.mu.Unlock()
		return "", "", fmt.Errorf("no admin user found in config")
	}
	username := cs.cfg.Users[adminIdx].Username
	newPass := generateRandomPassword()
	salt := genToken()[:16]
	cs.cfg.Users[adminIdx].Salt = salt
	cs.cfg.Users[adminIdx].Hash = hashPassword(newPass, salt)
	cs.cfg.Users[adminIdx].MustChangePassword = true
	cs.mu.Unlock()
	if err := cs.save(); err != nil {
		return "", "", err
	}
	return username, newPass, nil
}

// generateRandomPassword creates a cryptographically random 16-character
// password with mixed case letters, digits, and special characters.
func generateRandomPassword() string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%"
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "AIOps-Reset-000000" // fallback, never empty
	}
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b)
}

// AlertEmails returns the deduplicated non-empty emails of all users — the
// recipients for alert / test notifications.
func (cs *ConfigStore) AlertEmails() []string {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	var out []string
	seen := map[string]bool{}
	for _, u := range cs.cfg.Users {
		e := strings.TrimSpace(u.Email)
		if e != "" && !seen[strings.ToLower(e)] {
			seen[strings.ToLower(e)] = true
			out = append(out, e)
		}
	}
	return out
}
