// Package ratelimit implements a per-key token bucket. It is deliberately
// in-process rather than backed by something like Redis, since this runs as
// a single instance and a shared limiter would be infrastructure with no
// measured need. One instance's limits being independent is the right trade
// for a demo, and the global simulation cap bounds the damage either way.
package ratelimit

import (
	"sync"
	"time"
)

// bucket is one key's allowance. Tokens refill continuously rather than in
// steps, so a caller that waits half a period gets half its budget back.
type bucket struct {
	tokens   float64
	lastSeen time.Time
}

// Limiter hands out a fixed rate of allowances per key, tolerating short
// bursts up to Burst.
type Limiter struct {
	rate  float64 // tokens per second
	burst float64
	// idleTTL is how long an untouched bucket is kept before being swept, so
	// a stream of one-off keys cannot grow the map without bound.
	idleTTL time.Duration

	mu      sync.Mutex
	buckets map[string]*bucket
	// now is overridable so tests can drive time directly instead of
	// sleeping through a refill window.
	now func() time.Time
}

// Config describes a limiter's allowance.
type Config struct {
	// PerSecond is the sustained rate. Non-positive disables limiting.
	PerSecond float64
	// Burst is the most a key may spend at once. Defaults to PerSecond.
	Burst float64
	// IdleTTL is how long an unused key's bucket is retained. Defaults to
	// ten minutes.
	IdleTTL time.Duration
}

// New returns a limiter. A non-positive rate returns nil, which every method
// treats as "allow everything" — that is how limiting is switched off.
func New(cfg Config) *Limiter {
	if cfg.PerSecond <= 0 {
		return nil
	}
	if cfg.Burst <= 0 {
		cfg.Burst = cfg.PerSecond
	}
	if cfg.IdleTTL <= 0 {
		cfg.IdleTTL = 10 * time.Minute
	}
	return &Limiter{
		rate:    cfg.PerSecond,
		burst:   cfg.Burst,
		idleTTL: cfg.IdleTTL,
		buckets: make(map[string]*bucket),
		now:     time.Now,
	}
}

// Allow spends one token for key, reporting whether it was available and, if
// not, how long until one is.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	if l == nil {
		return true, 0
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, lastSeen: now}
		l.buckets[key] = b
		l.sweep(now)
	}

	b.tokens += now.Sub(b.lastSeen).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.lastSeen = now

	if b.tokens < 1 {
		return false, time.Duration((1-b.tokens)/l.rate*float64(time.Second)) + time.Millisecond
	}
	b.tokens--
	return true, 0
}

// sweep drops buckets nobody has touched in a while. Called only when a new
// key appears, which is the only time the map can grow.
func (l *Limiter) sweep(now time.Time) {
	for key, b := range l.buckets {
		if now.Sub(b.lastSeen) > l.idleTTL {
			delete(l.buckets, key)
		}
	}
}

// Len reports how many keys are currently tracked.
func (l *Limiter) Len() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
