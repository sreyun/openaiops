package main

import (
	"bytes"
	"math"
	"math/rand"
	"sync"
	"time"
)

// ---- sync.Pool-based buffer reuse ----
// Reduces GC pressure by recycling []byte buffers across hot paths
// (collector reading, JSON encoding, terminal framing).

// bufPool32K 只有 collector_windows.go 的一处使用者。CI 的 staticcheck 按 linux 分析，
// 看不到那条 build tag 分支，所以会报 U1000——那是分析范围的结果，不是死代码。
// 池里存 *[]byte 而不是 []byte：切片是三字的值，直接 Put 会在每次调用时把它逃逸到堆上，
// 恰好抵消掉用池省下来的分配（SA6002）。
var (
	bufPool32K   = sync.Pool{New: func() any { b := make([]byte, 32<<10); return &b }}
	bytesBufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}
)

//lint:ignore U1000 used by collector_windows.go; CI analyses linux only
func getBuf32K() []byte { return *(bufPool32K.Get().(*[]byte)) }

//lint:ignore U1000 used by collector_windows.go; CI analyses linux only
func putBuf32K(b []byte) { bufPool32K.Put(&b) }
func getBytesBuf() *bytes.Buffer {
	b := bytesBufPool.Get().(*bytes.Buffer)
	b.Reset()
	return b
}
func putBytesBuf(b *bytes.Buffer) { bytesBufPool.Put(b) }

// ---- Exponential Backoff with Jitter ----
// Prevents thundering-herd and respects server overload. Used by all retry
// loops (registration, reporting, terminal/forward channel reconnect).

type backoff struct {
	initial time.Duration
	max     time.Duration
	current time.Duration
	rng     *rand.Rand
}

func newBackoff(initial, max time.Duration) *backoff {
	return &backoff{
		initial: initial,
		max:     max,
		current: initial,
		rng:     rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// next returns the next backoff duration (with ±25% jitter), then doubles
// the base for next call, capped at max.
func (b *backoff) next() time.Duration {
	d := b.current
	// Apply ±25% jitter to avoid synchronized retry storms.
	jitter := time.Duration(float64(d) * (0.75 + b.rng.Float64()*0.5))
	b.current = time.Duration(math.Min(float64(b.current*2), float64(b.max)))
	return jitter
}

// reset brings the backoff back to its initial value (call after success).
func (b *backoff) reset() {
	b.current = b.initial
}

// ---- Sliding-Window Circuit Breaker ----
// Protects against cascading failures. The breaker opens after consecutive
// failures exceed the threshold and auto-recovers after the cooldown period
// with a single trial request.

type circuitState int

const (
	cbClosed   circuitState = iota // normal operation
	cbOpen                         // failing fast, no requests sent
	cbHalfOpen                     // trial request allowed
)

type circuitBreaker struct {
	mu              sync.Mutex
	state           circuitState
	failures        int
	threshold       int           // consecutive failures to open
	cooldown        time.Duration // time before half-open trial
	lastFailureTime time.Time
}

func newCircuitBreaker(threshold int, cooldown time.Duration) *circuitBreaker {
	return &circuitBreaker{
		threshold: threshold,
		cooldown:  cooldown,
	}
}

// allow returns true if a request may be attempted.
func (cb *circuitBreaker) allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	switch cb.state {
	case cbClosed:
		return true
	case cbOpen:
		if time.Since(cb.lastFailureTime) > cb.cooldown {
			cb.state = cbHalfOpen
			return true
		}
		return false
	case cbHalfOpen:
		return false // only one trial at a time
	}
	return false
}

// success signals a successful request — reset the breaker.
func (cb *circuitBreaker) success() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.state = cbClosed
	cb.failures = 0
}

// failure signals a failed request. Returns true if the circuit just opened.
func (cb *circuitBreaker) failure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures++
	cb.lastFailureTime = time.Now()
	if cb.failures >= cb.threshold {
		cb.state = cbOpen
	}
}

// isOpen returns true when the breaker is tripped AND still in cooldown.
//
// v5.2.8: CRITICAL FIX — previously isOpen() returned true whenever state==cbOpen,
// regardless of whether the cooldown had elapsed. The main report loop checks
// isOpen() BEFORE allow(), and does `continue` (skipping the goroutine that
// calls allow()). This meant allow() — the only method that transitions
// cbOpen→cbHalfOpen — was never called, so the breaker stayed open FOREVER
// after a server restart. Agents would never recover.
//
// Now: isOpen() returns false once the cooldown expires, allowing the goroutine
// to be created, where allow() handles the half-open transition.
func (cb *circuitBreaker) isOpen() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.state == cbOpen && time.Since(cb.lastFailureTime) > cb.cooldown {
		// Cooldown expired — don't skip this target. The goroutine's allow()
		// call will transition to cbHalfOpen and permit a trial request.
		return false
	}
	return cb.state == cbOpen
}

// maskToken returns a safe representation of a token for logging.
// Never logs the full token — only the first 4 characters + "..." suffix.
// Returns "(empty)" for blank tokens so operators can distinguish
// "no token configured" from a misconfigured token.
func maskToken(t string) string {
	if t == "" {
		return "(none)"
	}
	if len(t) <= 4 {
		return t[:1] + "***"
	}
	return t[:4] + "..."
}
