package ingestbuffer

import (
	"sync"
	"time"
)

// tokenBucket is a classic token-bucket rate limiter: tokens refill
// continuously at `rate` tokens/sec up to `burst` capacity; each Allow call
// consumes one token if available. Used per-tenant so that a bursty/noisy
// tenant is throttled at the publish API without a global lock contending
// across all tenants.
type tokenBucket struct {
	mu       sync.Mutex
	rate     float64 // tokens per second
	burst    float64 // max accumulated tokens
	tokens   float64
	lastFill time.Time
	now      func() time.Time
}

func newTokenBucket(rate, burst float64) *tokenBucket {
	if burst <= 0 {
		burst = 1
	}
	return &tokenBucket{
		rate:     rate,
		burst:    burst,
		tokens:   burst,
		lastFill: time.Now(),
		now:      time.Now,
	}
}

// Allow reports whether a single token is available, consuming it if so.
func (b *tokenBucket) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	elapsed := now.Sub(b.lastFill).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * b.rate
		if b.tokens > b.burst {
			b.tokens = b.burst
		}
		b.lastFill = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}
