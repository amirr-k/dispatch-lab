package http

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"dispatchlab/internal/domain"
	"dispatchlab/internal/replay"
	"dispatchlab/internal/service"
	"dispatchlab/internal/store"
	"dispatchlab/internal/telemetry"
)

// newPersistedServer wires a server to an in-memory store, which is what the
// replay routes need to have anything to serve.
func newPersistedServer(t *testing.T) (*Server, store.Store, *telemetry.Metrics) {
	t.Helper()

	s := store.NewMemory()
	metrics := telemetry.NewMetrics()
	mgr := service.NewManagerWithConfig(service.ManagerConfig{Store: s, Metrics: metrics})
	t.Cleanup(mgr.Shutdown)

	server := NewServerWithConfig(ServerConfig{
		Manager:     mgr,
		Comparisons: service.NewComparisonsWithStore(s, metrics, nil),
		Store:       s,
		Metrics:     metrics,
	})
	return server, s, metrics
}

// createSimulationFor spins up a simulation and waits for its opening
// snapshot to reach the store, so a replay request has something to read.
func createSimulationFor(t *testing.T, server *Server, s store.Store) string {
	t.Helper()

	rec := doJSON(t, server.Routes(), http.MethodPost, "/api/v1/simulations", createRequest{})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create simulation: %d %s", rec.Code, rec.Body.String())
	}
	var resp createResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	waitFor(t, 3*time.Second, func() bool {
		seq, err := s.LatestSequence(t.Context(), resp.ID)
		return err == nil && seq > 0
	})
	return resp.ID
}

func waitFor(t *testing.T, limit time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for a condition")
}

func TestReplayReturnsTheEventLog(t *testing.T) {
	server, s, _ := newPersistedServer(t)
	id := createSimulationFor(t, server, s)

	rec := doJSON(t, server.Routes(), http.MethodGet, "/api/v1/simulations/"+id+"/replay", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var log replay.Log
	if err := json.Unmarshal(rec.Body.Bytes(), &log); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(log.Events) == 0 {
		t.Fatal("replay returned no events")
	}
	if log.Events[0].Type != domain.EventSimulationSnapshot {
		t.Errorf("first event is %q, want the opening snapshot", log.Events[0].Type)
	}
	if log.Simulation.ID != id {
		t.Errorf("metadata id = %q, want %q", log.Simulation.ID, id)
	}
	if log.LatestSequence < 1 {
		t.Errorf("latest sequence = %d", log.LatestSequence)
	}
}

func TestReplayReconstructsStateAtASequence(t *testing.T) {
	server, s, _ := newPersistedServer(t)
	id := createSimulationFor(t, server, s)

	rec := doJSON(t, server.Routes(), http.MethodGet, "/api/v1/simulations/"+id+"/replay?at=1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var state replay.State
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if state.Sequence != 1 {
		t.Errorf("sequence = %d, want 1", state.Sequence)
	}
	if len(state.Nodes) == 0 || len(state.Drivers) == 0 {
		t.Error("reconstructed state has no city or drivers")
	}
}

func TestReplayPaginates(t *testing.T) {
	server, s, _ := newPersistedServer(t)
	id := createSimulationFor(t, server, s)

	rec := doJSON(t, server.Routes(), http.MethodGet, "/api/v1/simulations/"+id+"/replay?fromSequence=0&limit=1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var log replay.Log
	if err := json.Unmarshal(rec.Body.Bytes(), &log); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(log.Events) != 1 {
		t.Fatalf("limit was ignored: got %d events", len(log.Events))
	}
}

func TestReplayRejectsBadParameters(t *testing.T) {
	server, s, _ := newPersistedServer(t)
	id := createSimulationFor(t, server, s)

	for _, query := range []string{"?at=abc", "?fromSequence=abc", "?limit=abc"} {
		rec := doJSON(t, server.Routes(), http.MethodGet, "/api/v1/simulations/"+id+"/replay"+query, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d", query, rec.Code)
		}
	}
}

func TestReplayOfUnknownSimulationIsNotFound(t *testing.T) {
	server, _, _ := newPersistedServer(t)

	rec := doJSON(t, server.Routes(), http.MethodGet, "/api/v1/simulations/nope/replay", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// without a database there is no history, and the api has to say so rather
// than pretend the run does not exist.
func TestReplayWithoutPersistenceIsUnavailable(t *testing.T) {
	server := newTestServer()

	rec := doJSON(t, server.Routes(), http.MethodGet, "/api/v1/simulations/any/replay", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
	rec = doJSON(t, server.Routes(), http.MethodPost, "/api/v1/simulations/any/showcase", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

// a finished run gets a stable replay url that keeps serving after the live
// simulation is gone.
func TestShowcaseGivesAStableReplayURL(t *testing.T) {
	server, s, _ := newPersistedServer(t)
	id := createSimulationFor(t, server, s)

	rec := doJSON(t, server.Routes(), http.MethodPost, "/api/v1/simulations/"+id+"/showcase", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp showcaseResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ReplayURL != "/replay/"+id {
		t.Errorf("replay url = %q, want /replay/%s", resp.ReplayURL, id)
	}
	if !resp.Showcase {
		t.Error("response does not report the run as a showcase")
	}

	stored, err := s.GetSimulation(t.Context(), id)
	if err != nil {
		t.Fatalf("GetSimulation: %v", err)
	}
	if !stored.Showcase || stored.CompletedAt == nil {
		t.Errorf("run was not marked complete and retained: %+v", stored)
	}

	replayRec := doJSON(t, server.Routes(), http.MethodGet, "/api/v1/simulations/"+id+"/replay", nil)
	if replayRec.Code != http.StatusOK {
		t.Fatalf("replay url does not serve: %d", replayRec.Code)
	}
}

func TestShowcaseOfUnknownSimulationIsNotFound(t *testing.T) {
	server, _, _ := newPersistedServer(t)

	rec := doJSON(t, server.Routes(), http.MethodPost, "/api/v1/simulations/nope/showcase", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMetricsEndpointExposesTheRequiredMetrics(t *testing.T) {
	server, s, _ := newPersistedServer(t)
	createSimulationFor(t, server, s)

	rec := doJSON(t, server.Routes(), http.MethodGet, "/metrics", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	for _, name := range []string{
		"dispatchlab_active_simulations",
		"dispatchlab_route_compute_duration_ms",
		"dispatchlab_match_compute_duration_ms",
		"dispatchlab_websocket_clients",
		"dispatchlab_dropped_updates_total",
		"dispatchlab_persistence_errors_total",
	} {
		if !strings.Contains(body, name) {
			t.Errorf("/metrics is missing %q", name)
		}
	}
	if !strings.Contains(body, "dispatchlab_active_simulations 1") {
		t.Errorf("active simulations was not tracked:\n%s", body)
	}
}

func TestReadinessReflectsTheStore(t *testing.T) {
	server, _, _ := newPersistedServer(t)

	rec := doJSON(t, server.Routes(), http.MethodGet, "/health/ready", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

// every response carries the id of the trace that produced it, which is what
// makes a report about one bad request findable in the logs.
func TestResponsesCarryATraceID(t *testing.T) {
	server := newTestServer()

	rec := doJSON(t, server.Routes(), http.MethodPost, "/api/v1/simulations", createRequest{})
	if rec.Header().Get("X-Trace-Id") == "" {
		t.Error("response carries no X-Trace-Id header")
	}
}

func TestComparisonIsPersisted(t *testing.T) {
	server, s, _ := newPersistedServer(t)

	seed := int64(42)
	drivers := 8
	rec := doJSON(t, server.Routes(), http.MethodPost, "/api/v1/comparisons",
		createComparisonRequest{Seed: &seed, Drivers: &drivers})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var result service.ComparisonResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}

	stored, err := s.GetComparison(t.Context(), result.ID)
	if err != nil {
		t.Fatalf("comparison was not persisted: %v", err)
	}
	if stored.Seed != seed || stored.Drivers != drivers {
		t.Errorf("stored comparison = %+v", stored)
	}
}
