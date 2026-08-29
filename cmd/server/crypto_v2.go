package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"log/slog"
	"os"
	"strings"
)

const secretEncPrefixV2 = "enc:v2:"

// secretKeyEntry is one named AES-256 key derived from a passphrase.
type secretKeyEntry struct {
	ID  string
	Key []byte
}

func deriveKeyFromPassphrase(pass string) []byte {
	sum := sha256.Sum256([]byte(pass))
	return sum[:]
}

// currentSecretKeyID returns AIOPS_SECRET_KEY_ID or "v1".
func currentSecretKeyID() string {
	id := strings.TrimSpace(os.Getenv("AIOPS_SECRET_KEY_ID"))
	if id == "" {
		return "v1"
	}
	id = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return -1
	}, id)
	if id == "" {
		return "v1"
	}
	return id
}

// loadAllSecretKeys returns current + previous keys for decrypt.
func loadAllSecretKeys() []secretKeyEntry {
	var out []secretKeyEntry
	if raw := strings.TrimSpace(os.Getenv("AIOPS_SECRET_KEY")); raw != "" {
		out = append(out, secretKeyEntry{ID: currentSecretKeyID(), Key: deriveKeyFromPassphrase(raw)})
	}
	prev := strings.TrimSpace(os.Getenv("AIOPS_SECRET_KEYS_PREV"))
	if prev == "" {
		return out
	}
	seen := map[string]bool{}
	for _, e := range out {
		seen[e.ID] = true
	}
	for _, part := range strings.Split(prev, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		idx := strings.IndexByte(part, ':')
		if idx <= 0 || idx >= len(part)-1 {
			continue
		}
		id := strings.TrimSpace(part[:idx])
		pass := part[idx+1:]
		if id == "" || pass == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, secretKeyEntry{ID: id, Key: deriveKeyFromPassphrase(pass)})
	}
	return out
}

func encryptSecretV2(plain string) string {
	if plain == "" || strings.HasPrefix(plain, secretEncPrefix) || strings.HasPrefix(plain, secretEncPrefixV2) {
		return plain
	}
	keys := loadAllSecretKeys()
	if len(keys) == 0 {
		return plain
	}
	primary := keys[0]
	gcm, err := newGCM(primary.Key)
	if err != nil {
		slog.Error("配置密钥 v2 加密初始化失败", "err", err)
		return "" // fail closed — never persist plaintext when keys exist
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		slog.Error("配置密钥 v2 随机数失败", "err", err)
		return ""
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return secretEncPrefixV2 + primary.ID + ":" + base64.StdEncoding.EncodeToString(sealed)
}

func decryptSecretV2(v string) (string, bool) {
	if !strings.HasPrefix(v, secretEncPrefixV2) {
		return "", false
	}
	rest := strings.TrimPrefix(v, secretEncPrefixV2)
	idx := strings.IndexByte(rest, ':')
	if idx <= 0 {
		// Malformed enc:v2 — keep original so a later save cannot blank the field.
		return v, true
	}
	kid, b64 := rest[:idx], rest[idx+1:]
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		slog.Error("配置密钥 v2 base64 失败", "err", err)
		return v, true
	}
	for _, e := range loadAllSecretKeys() {
		if e.ID == kid {
			if pt := tryOpenGCM(e.Key, data); pt != "" {
				return pt, true
			}
		}
	}
	for _, e := range loadAllSecretKeys() {
		if pt := tryOpenGCM(e.Key, data); pt != "" {
			return pt, true
		}
	}
	slog.Error("配置密钥 v2 解密失败：无匹配密钥（密文已保留以免写回清空）", "key_id", kid)
	return v, true
}

func tryOpenGCM(key, data []byte) string {
	gcm, err := newGCM(key)
	if err != nil || len(data) < gcm.NonceSize() {
		return ""
	}
	nonce, ct := data[:gcm.NonceSize()], data[gcm.NonceSize():]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return ""
	}
	return string(pt)
}

// rewrapSecretIfNeeded re-encrypts v1 or foreign-kid ciphertext with current primary key.
func rewrapSecretIfNeeded(v string) string {
	plain := decryptSecret(v)
	if plain == "" || plain == v {
		return v
	}
	return encryptSecret(plain)
}

func secretKeyStatus() map[string]any {
	keys := loadAllSecretKeys()
	ids := make([]string, 0, len(keys))
	for _, k := range keys {
		ids = append(ids, k.ID)
	}
	return map[string]any{
		"enabled":     len(keys) > 0,
		"primary_id":  currentSecretKeyID(),
		"key_ids":     ids,
		"format":      "enc:v2:<key_id>:<payload> (compat enc:v1:)",
		"prev_env":    "AIOPS_SECRET_KEYS_PREV",
		"store_env":   "AIOPS_SECRET_KEY_STORE",
		"auto_rotate": "POST /api/v1/security/secret-rotate + interval_days",
	}
}
