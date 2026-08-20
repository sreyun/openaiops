package main

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// OIDC ID Token validation (JWKS + iss/aud/exp/nonce). Fail-closed when id_token present
// or required; used by the OIDC callback path.

type oidcDiscovery struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserinfoEndpoint      string `json:"userinfo_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

type oidcJWKS struct {
	Keys []oidcJWK `json:"keys"`
}

type oidcJWK struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type oidcIDClaims struct {
	Iss               string          `json:"iss"`
	Sub               string          `json:"sub"`
	Aud               json.RawMessage `json:"aud"`
	Exp               int64           `json:"exp"`
	Iat               int64           `json:"iat"`
	Nonce             string          `json:"nonce"`
	Email             string          `json:"email"`
	Name              string          `json:"name"`
	PreferredUsername string          `json:"preferred_username"`
}

var (
	jwksCacheMu sync.Mutex
	jwksCache   = map[string]cachedJWKS{}
)

type cachedJWKS struct {
	keys      map[string]*rsa.PublicKey
	expiresAt int64
}

func fetchOIDCDiscovery(issuer string) (oidcDiscovery, error) {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	var d oidcDiscovery
	if issuer == "" {
		return d, fmt.Errorf("empty issuer")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(issuer + "/.well-known/openid-configuration")
	if err != nil {
		return d, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return d, fmt.Errorf("discovery status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return d, err
	}
	if d.AuthorizationEndpoint == "" || d.TokenEndpoint == "" {
		return d, fmt.Errorf("incomplete discovery document")
	}
	return d, nil
}

// validateOIDCIDToken verifies RS256 signature via JWKS and standard claims.
// Returns parsed claims on success.
func validateOIDCIDToken(raw, issuer, clientID, nonce, jwksURI string) (oidcIDClaims, error) {
	var claims oidcIDClaims
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return claims, fmt.Errorf("malformed id_token")
	}
	hdrJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return claims, fmt.Errorf("id_token header: %w", err)
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
		Typ string `json:"typ"`
	}
	if err := json.Unmarshal(hdrJSON, &hdr); err != nil {
		return claims, err
	}
	if hdr.Alg != "RS256" {
		return claims, fmt.Errorf("unsupported id_token alg %s", hdr.Alg)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims, fmt.Errorf("id_token payload: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return claims, fmt.Errorf("id_token sig: %w", err)
	}
	if jwksURI == "" {
		return claims, fmt.Errorf("no jwks_uri")
	}
	pub, err := lookupOIDCJWK(jwksURI, hdr.Kid)
	if err != nil {
		return claims, err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		return claims, fmt.Errorf("id_token signature invalid")
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return claims, err
	}
	iss := strings.TrimRight(strings.TrimSpace(issuer), "/")
	gotIss := strings.TrimRight(strings.TrimSpace(claims.Iss), "/")
	if gotIss == "" || !strings.EqualFold(gotIss, iss) {
		return claims, fmt.Errorf("id_token iss mismatch")
	}
	if !oidcAudContains(claims.Aud, clientID) {
		return claims, fmt.Errorf("id_token aud mismatch")
	}
	now := time.Now().Unix()
	if claims.Exp == 0 || now > claims.Exp+60 {
		return claims, fmt.Errorf("id_token expired")
	}
	if nonce != "" && claims.Nonce != nonce {
		return claims, fmt.Errorf("id_token nonce mismatch")
	}
	if claims.Sub == "" {
		return claims, fmt.Errorf("id_token missing sub")
	}
	return claims, nil
}

func oidcAudContains(raw json.RawMessage, clientID string) bool {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" || len(raw) == 0 {
		return false
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return one == clientID
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		for _, a := range many {
			if a == clientID {
				return true
			}
		}
	}
	return false
}

func lookupOIDCJWK(jwksURI, kid string) (*rsa.PublicKey, error) {
	keys, err := loadOIDCJWKS(jwksURI)
	if err != nil {
		return nil, err
	}
	if kid != "" {
		if k, ok := keys[kid]; ok {
			return k, nil
		}
		return nil, fmt.Errorf("jwks kid not found")
	}
	for _, k := range keys {
		return k, nil
	}
	return nil, fmt.Errorf("empty jwks")
}

func loadOIDCJWKS(jwksURI string) (map[string]*rsa.PublicKey, error) {
	now := time.Now().Unix()
	jwksCacheMu.Lock()
	if c, ok := jwksCache[jwksURI]; ok && now < c.expiresAt {
		out := c.keys
		jwksCacheMu.Unlock()
		return out, nil
	}
	jwksCacheMu.Unlock()

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(jwksURI)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("jwks status %d", resp.StatusCode)
	}
	var doc oidcJWKS
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range doc.Keys {
		if !strings.EqualFold(k.Kty, "RSA") {
			continue
		}
		pub, err := jwkToRSAPublic(k)
		if err != nil {
			continue
		}
		id := k.Kid
		if id == "" {
			id = fmt.Sprintf("key-%d", len(keys))
		}
		keys[id] = pub
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no RSA keys in jwks")
	}
	jwksCacheMu.Lock()
	jwksCache[jwksURI] = cachedJWKS{keys: keys, expiresAt: now + 600}
	jwksCacheMu.Unlock()
	return keys, nil
}

func jwkToRSAPublic(k oidcJWK) (*rsa.PublicKey, error) {
	nb, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, err
	}
	eb, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, err
	}
	var eInt int
	for _, b := range eb {
		eInt = eInt<<8 | int(b)
	}
	if eInt == 0 {
		eInt = 65537
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: eInt}, nil
}
