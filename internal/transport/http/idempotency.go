package http

import (
	"sync"
	"time"
)

const (
	// idempotencyTTL is how long a command's outcome is remembered. Retries
	// happen within seconds of the original; a window measured in minutes is
	// generous and keeps the cache small.
	idempotencyTTL = 10 * time.Minute
	// idempotencyMaxEntries bounds the cache. Past it the oldest entries are
	// dropped, because an unbounded cache keyed by client-supplied strings is
	// a memory exhaustion vector.
	idempotencyMaxEntries = 4096
)

// storedResponse is a command's recorded outcome.
type storedResponse struct {
	status      int
	body        []byte
	contentType string
	storedAt    time.Time
}

// idempotencyCache remembers recent command outcomes by key.
type idempotencyCache struct {
	ttl     time.Duration
	max     int
	mu      sync.Mutex
	entries map[string]storedResponse
	now     func() time.Time
}

func newIdempotencyCache(ttl time.Duration, max int) *idempotencyCache {
	if ttl <= 0 {
		ttl = idempotencyTTL
	}
	if max <= 0 {
		max = idempotencyMaxEntries
	}
	return &idempotencyCache{
		ttl:     ttl,
		max:     max,
		entries: make(map[string]storedResponse),
		now:     time.Now,
	}
}

// Get returns a remembered outcome that has not yet expired.
func (c *idempotencyCache) Get(key string) (storedResponse, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	stored, ok := c.entries[key]
	if !ok {
		return storedResponse{}, false
	}
	if c.now().Sub(stored.storedAt) > c.ttl {
		delete(c.entries, key)
		return storedResponse{}, false
	}
	return stored, true
}

// Put records an outcome, evicting expired entries first and the oldest
// remaining one if the cache is still at capacity.
func (c *idempotencyCache) Put(key string, response storedResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	response.storedAt = now

	if len(c.entries) >= c.max {
		c.evict(now)
	}
	c.entries[key] = response
}

func (c *idempotencyCache) evict(now time.Time) {
	oldestKey := ""
	var oldestAt time.Time

	for key, entry := range c.entries {
		if now.Sub(entry.storedAt) > c.ttl {
			delete(c.entries, key)
			continue
		}
		if oldestKey == "" || entry.storedAt.Before(oldestAt) {
			oldestKey, oldestAt = key, entry.storedAt
		}
	}

	if len(c.entries) >= c.max && oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}

// Len reports how many outcomes are currently remembered.
func (c *idempotencyCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
