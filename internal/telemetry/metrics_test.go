package telemetry

import (
	"strings"
	"sync"
	"testing"
)

func TestCounterAndGauge(t *testing.T) {
	m := NewMetrics()

	m.DroppedUpdates().Inc()
	m.DroppedUpdates().Add(4)
	if got := m.DroppedUpdates().Value(); got != 5 {
		t.Fatalf("counter = %v, want 5", got)
	}

	m.DroppedUpdates().Add(-1)
	if got := m.DroppedUpdates().Value(); got != 5 {
		t.Fatalf("counter accepted a negative delta: %v", got)
	}

	m.ActiveSimulations().Inc()
	m.ActiveSimulations().Inc()
	m.ActiveSimulations().Dec()
	if got := m.ActiveSimulations().Value(); got != 1 {
		t.Fatalf("gauge = %v, want 1", got)
	}
	m.ActiveSimulations().Set(9)
	if got := m.ActiveSimulations().Value(); got != 9 {
		t.Fatalf("gauge after Set = %v, want 9", got)
	}
}

func TestHistogramBucketsAreCumulative(t *testing.T) {
	m := NewMetrics()
	for _, v := range []float64{0.05, 0.4, 3, 7000} {
		m.RouteLatency().Observe(v)
	}

	if got := m.RouteLatency().Count(); got != 4 {
		t.Fatalf("count = %d, want 4", got)
	}
	if got := m.RouteLatency().Sum(); got != 7003.45 {
		t.Fatalf("sum = %v, want 7003.45", got)
	}

	var out strings.Builder
	if err := m.WritePrometheus(&out); err != nil {
		t.Fatalf("WritePrometheus: %v", err)
	}
	text := out.String()

	for _, want := range []string{
		`dispatchlab_route_compute_duration_ms_bucket{le="0.1"} 1`,
		`dispatchlab_route_compute_duration_ms_bucket{le="0.5"} 2`,
		`dispatchlab_route_compute_duration_ms_bucket{le="5"} 3`,
		`dispatchlab_route_compute_duration_ms_bucket{le="+Inf"} 4`,
		`dispatchlab_route_compute_duration_ms_count 4`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("exposition missing %q\n%s", want, text)
		}
	}
}

// a sample exactly on a bucket bound belongs in that bucket, since prometheus
// buckets are "less than or equal".
func TestHistogramBoundaryIsInclusive(t *testing.T) {
	m := NewMetrics()
	m.MatchLatency().Observe(1)

	var out strings.Builder
	if err := m.WritePrometheus(&out); err != nil {
		t.Fatalf("WritePrometheus: %v", err)
	}
	if !strings.Contains(out.String(), `dispatchlab_match_compute_duration_ms_bucket{le="1"} 1`) {
		t.Errorf("sample on the bound did not land in that bucket:\n%s", out.String())
	}
}

func TestExpositionCoversEveryRequiredMetric(t *testing.T) {
	var out strings.Builder
	if err := NewMetrics().WritePrometheus(&out); err != nil {
		t.Fatalf("WritePrometheus: %v", err)
	}
	text := out.String()

	// the metrics the phase 5 spec requires by name.
	for _, name := range []string{
		"dispatchlab_active_simulations",
		"dispatchlab_route_compute_duration_ms",
		"dispatchlab_match_compute_duration_ms",
		"dispatchlab_websocket_clients",
		"dispatchlab_dropped_updates_total",
		"dispatchlab_persistence_errors_total",
	} {
		if !strings.Contains(text, "# TYPE "+name+" ") {
			t.Errorf("missing metric %q", name)
		}
	}
}

func TestNilMetricsIsUsable(t *testing.T) {
	var m *Metrics
	m.ActiveSimulations().Inc()
	m.RouteLatency().Observe(1)
	m.DroppedUpdates().Add(3)
	if err := m.WritePrometheus(&strings.Builder{}); err != nil {
		t.Fatalf("WritePrometheus on nil metrics: %v", err)
	}
	if got := m.DroppedUpdates().Value(); got != 0 {
		t.Fatalf("nil counter value = %v, want 0", got)
	}
}

func TestConcurrentUpdates(t *testing.T) {
	m := NewMetrics()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				m.EventsPersisted().Inc()
				m.WebSocketClients().Inc()
				m.WebSocketClients().Dec()
				m.RouteLatency().Observe(float64(j % 20))
			}
		}()
	}
	wg.Wait()

	if got := m.EventsPersisted().Value(); got != 4000 {
		t.Fatalf("counter = %v, want 4000", got)
	}
	if got := m.WebSocketClients().Value(); got != 0 {
		t.Fatalf("gauge = %v, want 0", got)
	}
	if got := m.RouteLatency().Count(); got != 4000 {
		t.Fatalf("histogram count = %d, want 4000", got)
	}
}
