// Package telemetry provides the structured logging, tracing, and metrics
// used across the backend. It deliberately depends on nothing outside the
// standard library: a single-binary demo does not need a metrics client or a
// trace collector, and the exposition format below is the standard Prometheus
// text format, so a scraper can be pointed at it whenever one exists.
package telemetry

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// latencyBucketsMs are the upper bounds, in milliseconds, for the route and
// match compute histograms. They cover the range these operations actually
// land in: sub-millisecond A* runs on a small city up to a slow batch solve.
var latencyBucketsMs = []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 25, 50, 100, 250, 500, 1000}

// Counter is a monotonically increasing value.
type Counter struct {
	name string
	help string
	bits atomic.Uint64
}

// Inc adds one.
func (c *Counter) Inc() { c.Add(1) }

// Add increases the counter by delta. Negative deltas are ignored, since a
// counter that can go down would break rate() on the scraping side.
func (c *Counter) Add(delta float64) {
	if c == nil || delta < 0 {
		return
	}
	for {
		old := c.bits.Load()
		next := math.Float64bits(math.Float64frombits(old) + delta)
		if c.bits.CompareAndSwap(old, next) {
			return
		}
	}
}

// Value returns the current total.
func (c *Counter) Value() float64 {
	if c == nil {
		return 0
	}
	return math.Float64frombits(c.bits.Load())
}

// Gauge is a value that can go up and down.
type Gauge struct {
	name string
	help string
	bits atomic.Uint64
}

// Set replaces the gauge's value.
func (g *Gauge) Set(v float64) {
	if g == nil {
		return
	}
	g.bits.Store(math.Float64bits(v))
}

// Add applies a signed delta.
func (g *Gauge) Add(delta float64) {
	if g == nil {
		return
	}
	for {
		old := g.bits.Load()
		next := math.Float64bits(math.Float64frombits(old) + delta)
		if g.bits.CompareAndSwap(old, next) {
			return
		}
	}
}

// Inc adds one.
func (g *Gauge) Inc() { g.Add(1) }

// Dec subtracts one.
func (g *Gauge) Dec() { g.Add(-1) }

// Value returns the current value.
func (g *Gauge) Value() float64 {
	if g == nil {
		return 0
	}
	return math.Float64frombits(g.bits.Load())
}

// Histogram records a distribution into fixed cumulative buckets.
type Histogram struct {
	name   string
	help   string
	bounds []float64
	mu     sync.Mutex
	counts []uint64
	sum    float64
	total  uint64
}

// Observe records one sample.
func (h *Histogram) Observe(v float64) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sum += v
	h.total++
	// counts is one longer than bounds; the final slot is the +Inf bucket,
	// which is where SearchFloat64s lands anything above the last bound.
	h.counts[sort.SearchFloat64s(h.bounds, v)]++
}

// Count returns how many samples have been observed.
func (h *Histogram) Count() uint64 {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.total
}

// Sum returns the total of every observed sample.
func (h *Histogram) Sum() float64 {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sum
}

// Metrics is the backend's full metric set. A nil *Metrics is usable: every
// accessor returns a nil metric and every nil metric's methods are no-ops, so
// components can be constructed without telemetry in tests.
type Metrics struct {
	activeSimulations *Gauge
	websocketClients  *Gauge
	routeLatency      *Histogram
	matchLatency      *Histogram
	droppedUpdates    *Counter
	persistenceErrors *Counter
	eventsPersisted   *Counter
	snapshotsWritten  *Counter

	counters   []*Counter
	gauges     []*Gauge
	histograms []*Histogram
}

// NewMetrics builds the metric set the server registers at startup.
func NewMetrics() *Metrics {
	m := &Metrics{}
	m.activeSimulations = m.gauge("dispatchlab_active_simulations", "Simulations currently running in this process.")
	m.websocketClients = m.gauge("dispatchlab_websocket_clients", "WebSocket stream connections currently open.")
	m.routeLatency = m.histogram("dispatchlab_route_compute_duration_ms", "Wall-clock duration of a single A* route computation.")
	m.matchLatency = m.histogram("dispatchlab_match_compute_duration_ms", "Wall-clock duration of one matching call (immediate assignment or one batch solve).")
	m.droppedUpdates = m.counter("dispatchlab_dropped_updates_total", "Events dropped because a consumer could not keep up.")
	m.persistenceErrors = m.counter("dispatchlab_persistence_errors_total", "Failed writes to the event or snapshot store.")
	m.eventsPersisted = m.counter("dispatchlab_events_persisted_total", "Simulation events written to the store.")
	m.snapshotsWritten = m.counter("dispatchlab_snapshots_written_total", "Periodic snapshots written to the store.")
	return m
}

func (m *Metrics) counter(name, help string) *Counter {
	c := &Counter{name: name, help: help}
	m.counters = append(m.counters, c)
	return c
}

func (m *Metrics) gauge(name, help string) *Gauge {
	g := &Gauge{name: name, help: help}
	m.gauges = append(m.gauges, g)
	return g
}

func (m *Metrics) histogram(name, help string) *Histogram {
	h := &Histogram{name: name, help: help, bounds: latencyBucketsMs, counts: make([]uint64, len(latencyBucketsMs)+1)}
	m.histograms = append(m.histograms, h)
	return h
}

// ActiveSimulations tracks how many simulations are running.
func (m *Metrics) ActiveSimulations() *Gauge {
	if m == nil {
		return nil
	}
	return m.activeSimulations
}

// WebSocketClients tracks how many stream connections are open.
func (m *Metrics) WebSocketClients() *Gauge {
	if m == nil {
		return nil
	}
	return m.websocketClients
}

// RouteLatency records A* route computation durations in milliseconds.
func (m *Metrics) RouteLatency() *Histogram {
	if m == nil {
		return nil
	}
	return m.routeLatency
}

// MatchLatency records matching durations in milliseconds.
func (m *Metrics) MatchLatency() *Histogram {
	if m == nil {
		return nil
	}
	return m.matchLatency
}

// DroppedUpdates counts events a consumer was too slow to receive.
func (m *Metrics) DroppedUpdates() *Counter {
	if m == nil {
		return nil
	}
	return m.droppedUpdates
}

// PersistenceErrors counts failed store writes.
func (m *Metrics) PersistenceErrors() *Counter {
	if m == nil {
		return nil
	}
	return m.persistenceErrors
}

// EventsPersisted counts events successfully written to the store.
func (m *Metrics) EventsPersisted() *Counter {
	if m == nil {
		return nil
	}
	return m.eventsPersisted
}

// SnapshotsWritten counts snapshots successfully written to the store.
func (m *Metrics) SnapshotsWritten() *Counter {
	if m == nil {
		return nil
	}
	return m.snapshotsWritten
}

// WritePrometheus renders every metric in Prometheus text exposition format.
func (m *Metrics) WritePrometheus(w io.Writer) error {
	if m == nil {
		return nil
	}
	var b strings.Builder
	for _, c := range m.counters {
		writeHeader(&b, c.name, c.help, "counter")
		fmt.Fprintf(&b, "%s %s\n", c.name, formatFloat(c.Value()))
	}
	for _, g := range m.gauges {
		writeHeader(&b, g.name, g.help, "gauge")
		fmt.Fprintf(&b, "%s %s\n", g.name, formatFloat(g.Value()))
	}
	for _, h := range m.histograms {
		writeHeader(&b, h.name, h.help, "histogram")
		h.mu.Lock()
		var cumulative uint64
		for i, bound := range h.bounds {
			cumulative += h.counts[i]
			fmt.Fprintf(&b, "%s_bucket{le=\"%s\"} %d\n", h.name, formatFloat(bound), cumulative)
		}
		cumulative += h.counts[len(h.counts)-1]
		fmt.Fprintf(&b, "%s_bucket{le=\"+Inf\"} %d\n", h.name, cumulative)
		fmt.Fprintf(&b, "%s_sum %s\n", h.name, formatFloat(h.sum))
		fmt.Fprintf(&b, "%s_count %d\n", h.name, h.total)
		h.mu.Unlock()
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func writeHeader(b *strings.Builder, name, help, kind string) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, kind)
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}
