package main

import (
	"sort"
	"sync"
	"time"
)

// latencies collects samples from many goroutines and reduces them to the
// percentiles a load-test report actually needs. All methods are safe for
// concurrent use; summarizing sorts a private copy so producers are never
// blocked by it.
type latencies struct {
	mu      sync.Mutex
	samples []time.Duration
}

func (l *latencies) add(d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.samples = append(l.samples, d)
}

func (l *latencies) len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.samples)
}

type summary struct {
	Count int           `json:"count"`
	Min   time.Duration `json:"min"`
	P50   time.Duration `json:"p50"`
	P95   time.Duration `json:"p95"`
	P99   time.Duration `json:"p99"`
	Max   time.Duration `json:"max"`
}

func (l *latencies) summarize() summary {
	l.mu.Lock()
	sorted := make([]time.Duration, len(l.samples))
	copy(sorted, l.samples)
	l.mu.Unlock()

	if len(sorted) == 0 {
		return summary{}
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	pick := func(p float64) time.Duration {
		idx := int(p * float64(len(sorted)-1))
		return sorted[idx]
	}
	return summary{
		Count: len(sorted),
		Min:   sorted[0],
		P50:   pick(0.50),
		P95:   pick(0.95),
		P99:   pick(0.99),
		Max:   sorted[len(sorted)-1],
	}
}

// counter is a plain concurrency-safe tally, used for status-code and error
// counts where a full latency distribution is not needed.
type counter struct {
	mu     sync.Mutex
	counts map[int]int
}

func newCounter() *counter {
	return &counter{counts: make(map[int]int)}
}

func (c *counter) inc(key int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[key]++
}

func (c *counter) snapshot() map[int]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[int]int, len(c.counts))
	for k, v := range c.counts {
		out[k] = v
	}
	return out
}
