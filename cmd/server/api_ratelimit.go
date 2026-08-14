package main

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Global API rate limit (sliding window). Complements login lockout and
// MCP/AI per-feature limits. Agent ingest + static downloads are excluded so
// high-frequency report/terminal reverse channels are not starved.
//
// Unauthenticated traffic is keyed by client IP so NAT / brute-force stays
// bounded. A *valid* session cookie is keyed by username so a Web console and
// Android app behind the same egress IP do not starve each other (the previous
// single 300/min IP bucket made the APK fail to open with HTTP 429).
const (
	apiRateWindowSec      = 60
	apiRateMaxAnonPerIP   = 300  // login + anonymous probes
	apiRateMaxAuthPerUser = 1800 // Web poll + Android + charts for one account
	apiRateRetryAfterSec  = 5
)

type apiRateLimiter struct {
	mu   sync.Mutex
	hits map[string][]int64
}

func newAPIRateLimiter() *apiRateLimiter {
	return &apiRateLimiter{hits: make(map[string][]int64)}
}

func (l *apiRateLimiter) allow(key string, max int) bool {
	if l == nil || key == "" {
		return true
	}
	if max <= 0 {
		max = apiRateMaxAnonPerIP
	}
	now := time.Now().Unix()
	cut := now - apiRateWindowSec
	l.mu.Lock()
	defer l.mu.Unlock()
	arr := l.hits[key]
	n := 0
	for _, t := range arr {
		if t > cut {
			arr[n] = t
			n++
		}
	}
	arr = arr[:n]
	if len(arr) >= max {
		l.hits[key] = arr
		return false
	}
	l.hits[key] = append(arr, now)
	// Opportunistic prune of idle keys when map grows large.
	if len(l.hits) > 10000 {
		for k, v := range l.hits {
			if len(v) == 0 || v[len(v)-1] < cut {
				delete(l.hits, k)
			}
		}
	}
	return true
}

func apiRateLimitSkip(path string) bool {
	if path == "/healthz" || path == "/" {
		return true
	}
	if strings.HasPrefix(path, "/dl/") || strings.HasPrefix(path, "/js/") ||
		strings.HasPrefix(path, "/css/") {
		return true
	}
	// Agent reverse channels + ingest — fingerprint/token gated, high volume.
	if strings.HasPrefix(path, "/api/v1/agent/") {
		return true
	}
	if path == "/api/v1/prom/write" || path == "/api/v1/mcp" ||
		path == "/api/v1/integrations/content-audit" {
		return true
	}
	if strings.HasPrefix(path, "/proxy/") {
		return true
	}
	return !strings.HasPrefix(path, "/api/")
}

func (s *Server) apiRateBucket(r *http.Request) (key string, max int) {
	ip := s.clientIP(r)
	if s.auth != nil {
		if user := strings.ToLower(strings.TrimSpace(s.auth.userForRequest(r))); user != "" {
			return "u:" + user, apiRateMaxAuthPerUser
		}
	}
	return "ip:" + ip, apiRateMaxAnonPerIP
}

func (s *Server) apiRateLimitMiddleware(next http.Handler) http.Handler {
	lim := newAPIRateLimiter()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if apiRateLimitSkip(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		key, max := s.apiRateBucket(r)
		if !lim.allow(key, max) {
			w.Header().Set("Retry-After", strconv.Itoa(apiRateRetryAfterSec))
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"error":       Tr(r, "api.rate_limited"),
				"code":        "rate_limited",
				"retry_after": apiRateRetryAfterSec,
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}
