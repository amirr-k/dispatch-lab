// Package http exposes the REST command API and mounts the WebSocket stream.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand"
	"net/http"
	"strconv"
	"time"

	"dispatchlab/internal/domain"
	"dispatchlab/internal/replay"
	"dispatchlab/internal/service"
	"dispatchlab/internal/store"
	"dispatchlab/internal/telemetry"
	"dispatchlab/internal/transport/ws"
)

const (
	defaultDrivers = 12
	maxDrivers     = 40
	// readyTimeout bounds how long a readiness probe waits on the store
	// before reporting the process unready.
	readyTimeout = 2 * time.Second
)

// Server adapts the service Manager and comparison store to HTTP.
type Server struct {
	mgr     *service.Manager
	compare *service.Comparisons
	replay  *replay.Reader
	store   store.Store
	metrics *telemetry.Metrics
	logger  *slog.Logger
}

// NewServer returns a server with no persistence or telemetry attached.
func NewServer(mgr *service.Manager, compare *service.Comparisons) *Server {
	return NewServerWithConfig(ServerConfig{Manager: mgr, Comparisons: compare})
}

// ServerConfig wires the server to the rest of the backend. Store, Metrics,
// and Logger are optional; without a store the replay routes report that a
// run has no persisted history.
type ServerConfig struct {
	Manager     *service.Manager
	Comparisons *service.Comparisons
	Store       store.Store
	Metrics     *telemetry.Metrics
	Logger      *slog.Logger
}

// NewServerWithConfig returns a fully wired server.
func NewServerWithConfig(cfg ServerConfig) *Server {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	s := &Server{
		mgr:     cfg.Manager,
		compare: cfg.Comparisons,
		store:   cfg.Store,
		metrics: cfg.Metrics,
		logger:  cfg.Logger,
	}
	if cfg.Store != nil {
		s.replay = replay.NewReader(cfg.Store)
	}
	return s
}

// Routes builds the full HTTP handler, including the WebSocket stream and
// health checks, wrapped in request tracing and permissive dev CORS.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/v1/simulations", s.createSimulation)
	mux.HandleFunc("GET /api/v1/simulations/{id}", s.getSimulation)
	mux.HandleFunc("POST /api/v1/simulations/{id}/orders", s.placeOrder)
	mux.HandleFunc("POST /api/v1/simulations/{id}/closures", s.closeRoad)
	mux.HandleFunc("POST /api/v1/simulations/{id}/pause", s.pause)
	mux.HandleFunc("POST /api/v1/simulations/{id}/resume", s.resume)
	mux.HandleFunc("POST /api/v1/simulations/{id}/reset", s.reset)
	mux.HandleFunc("POST /api/v1/simulations/{id}/speed", s.setSpeed)
	mux.HandleFunc("POST /api/v1/simulations/{id}/showcase", s.markShowcase)
	mux.HandleFunc("GET /api/v1/simulations/{id}/replay", s.getReplay)
	mux.HandleFunc("GET /api/v1/simulations/{id}/stream",
		ws.HandlerWithTelemetry(s.mgr.StreamLookup, s.metrics, s.logger))

	mux.HandleFunc("POST /api/v1/comparisons", s.createComparison)
	mux.HandleFunc("GET /api/v1/comparisons/{id}", s.getComparison)

	mux.HandleFunc("GET /health/live", health)
	mux.HandleFunc("GET /health/ready", s.ready)
	mux.HandleFunc("GET /metrics", s.serveMetrics)

	return withCORS(withTelemetry(mux, s.logger))
}

type createRequest struct {
	Seed    *int64 `json:"seed"`
	Drivers *int   `json:"drivers"`
}

type createResponse struct {
	ID      string `json:"id"`
	Seed    int64  `json:"seed"`
	Drivers int    `json:"drivers"`
}

func (s *Server) createSimulation(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if !decode(w, r, &req) {
		return
	}

	seed := rand.Int63()
	if req.Seed != nil {
		seed = *req.Seed
	}
	drivers := defaultDrivers
	if req.Drivers != nil {
		drivers = clamp(*req.Drivers, 1, maxDrivers)
	}

	id, err := s.mgr.Create("", seed, drivers)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, createResponse{ID: id, Seed: seed, Drivers: drivers})
}

func (s *Server) getSimulation(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.mgr.Snapshot(r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

type createComparisonRequest struct {
	Seed    *int64 `json:"seed"`
	Drivers *int   `json:"drivers"`
}

// createComparison runs a fresh deterministic scenario for the given seed
// and driver count through both matching strategies and stores the result.
// The scenario itself is checked-in code (service.DefaultScenario), so the
// same seed and driver count always reproduce the same comparison.
func (s *Server) createComparison(w http.ResponseWriter, r *http.Request) {
	var req createComparisonRequest
	if !decode(w, r, &req) {
		return
	}

	seed := rand.Int63()
	if req.Seed != nil {
		seed = *req.Seed
	}
	drivers := defaultDrivers
	if req.Drivers != nil {
		drivers = clamp(*req.Drivers, 1, maxDrivers)
	}

	result := s.compare.Create(r.Context(), seed, drivers)
	writeJSON(w, http.StatusCreated, result)
}

func (s *Server) getComparison(w http.ResponseWriter, r *http.Request) {
	result, ok := s.compare.Get(r.Context(), r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "comparison not found")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type placeOrderRequest struct {
	Pickup      domain.NodeID `json:"pickup"`
	Destination domain.NodeID `json:"destination"`
}

func (s *Server) placeOrder(w http.ResponseWriter, r *http.Request) {
	var req placeOrderRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Pickup == "" || req.Destination == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "pickup and destination are required")
		return
	}
	if err := s.mgr.PlaceOrder(r.Context(), r.PathValue("id"), req.Pickup, req.Destination); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

type closeRoadRequest struct {
	EdgeID domain.EdgeID `json:"edgeId"`
}

func (s *Server) closeRoad(w http.ResponseWriter, r *http.Request) {
	var req closeRoadRequest
	if !decode(w, r, &req) {
		return
	}
	if req.EdgeID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "edgeId is required")
		return
	}
	if err := s.mgr.CloseRoad(r.Context(), r.PathValue("id"), req.EdgeID); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) pause(w http.ResponseWriter, r *http.Request)  { s.setPaused(w, r, true) }
func (s *Server) resume(w http.ResponseWriter, r *http.Request) { s.setPaused(w, r, false) }

func (s *Server) setPaused(w http.ResponseWriter, r *http.Request, paused bool) {
	if err := s.mgr.SetPaused(r.Context(), r.PathValue("id"), paused); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) reset(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.Reset(r.Context(), r.PathValue("id")); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

type speedRequest struct {
	Multiplier float64 `json:"multiplier"`
}

func (s *Server) setSpeed(w http.ResponseWriter, r *http.Request) {
	var req speedRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Multiplier <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "multiplier must be positive")
		return
	}
	if err := s.mgr.SetSpeed(r.Context(), r.PathValue("id"), req.Multiplier); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// markShowcase retains a finished run permanently and hands back the stable
// URL its replay lives at.
func (s *Server) markShowcase(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "no_persistence",
			"this server has no database attached, so runs cannot be saved")
		return
	}

	id := r.PathValue("id")
	if err := s.mgr.MarkShowcase(r.Context(), id); err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, showcaseResponse{
		ID:        id,
		Showcase:  true,
		ReplayURL: "/replay/" + id,
	})
}

type showcaseResponse struct {
	ID        string `json:"id"`
	Showcase  bool   `json:"showcase"`
	ReplayURL string `json:"replayUrl"`
}

// getReplay serves a run's persisted history. With no query parameters it
// returns the event log itself, which is what the scrubber plays through.
// With ?at=<sequence> it instead returns the state reconstructed at that
// point, for a client that wants one frame rather than the whole run.
func (s *Server) getReplay(w http.ResponseWriter, r *http.Request) {
	if s.replay == nil {
		writeError(w, http.StatusServiceUnavailable, "no_persistence",
			"this server has no database attached, so no history was recorded")
		return
	}

	id := r.PathValue("id")
	query := r.URL.Query()

	if raw := query.Get("at"); raw != "" {
		sequence, err := strconv.Atoi(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "at must be a sequence number")
			return
		}
		state, err := s.replay.StateAt(r.Context(), id, sequence)
		if err != nil {
			writeReplayError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, state)
		return
	}

	from, err := intParam(query.Get("fromSequence"), 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "fromSequence must be a number")
		return
	}
	limit, err := intParam(query.Get("limit"), 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "limit must be a number")
		return
	}

	log, err := s.replay.Load(r.Context(), id, from, limit)
	if err != nil {
		writeReplayError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, log)
}

func intParam(raw string, fallback int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	return strconv.Atoi(raw)
}

func (s *Server) serveMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if err := s.metrics.WritePrometheus(w); err != nil {
		s.logger.Error("could not write metrics", "error", err)
	}
}

func health(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

// ready reports whether the process can actually serve: with a database
// attached, that means the database answers, not merely that the process is
// alive.
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()

	if err := s.store.Ping(ctx); err != nil {
		s.logger.Warn("readiness probe failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "not_ready", "store is unreachable")
		return
	}
	w.WriteHeader(http.StatusOK)
}

// decode reads a JSON body, tolerating an empty body as an empty object. It
// writes a 400 and returns false if the body is present but malformed.
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Body == nil || r.ContentLength == 0 {
		return true
	}
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body is not valid JSON")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// writeError returns a structured error envelope, never an internal stack trace.
func writeError(w http.ResponseWriter, status int, code, message string) {
	var body errorBody
	body.Error.Code = code
	body.Error.Message = message
	writeJSON(w, status, body)
}

func writeReplayError(w http.ResponseWriter, err error) {
	if errors.Is(err, replay.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "no replay exists for that simulation")
		return
	}
	writeError(w, http.StatusInternalServerError, "internal", "could not read the replay")
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "simulation not found")
	case errors.Is(err, service.ErrCapacity):
		writeError(w, http.StatusTooManyRequests, "capacity", "simulation capacity reached")
	case errors.Is(err, service.ErrBusy):
		writeError(w, http.StatusServiceUnavailable, "busy", "simulation is busy, retry shortly")
	default:
		writeError(w, http.StatusInternalServerError, "internal", "unexpected error")
	}
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
