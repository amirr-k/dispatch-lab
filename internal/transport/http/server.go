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
	"dispatchlab/internal/ratelimit"
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
	mgr        *service.Manager
	compare    *service.Comparisons
	replay     *replay.Reader
	store      store.Store
	sessions   *service.Sessions
	limiter    *ratelimit.Limiter
	idempotent *idempotencyCache
	origins    *originPolicy
	metrics    *telemetry.Metrics
	logger     *slog.Logger
}

// NewServer returns a server with no persistence, sessions, or telemetry
// attached.
func NewServer(mgr *service.Manager, compare *service.Comparisons) *Server {
	return NewServerWithConfig(ServerConfig{Manager: mgr, Comparisons: compare})
}

// ServerConfig wires the server to the rest of the backend. Everything but
// the manager is optional: without a store the replay routes report that a
// run has no persisted history, and without sessions every request is
// treated as anonymous and unowned.
type ServerConfig struct {
	Manager     *service.Manager
	Comparisons *service.Comparisons
	Store       store.Store
	Sessions    *service.Sessions
	Metrics     *telemetry.Metrics
	Logger      *slog.Logger
	// AllowedOrigins is the browser origin allowlist. Empty is permissive,
	// which is what makes a local vite dev server work against a clean clone.
	AllowedOrigins []string
	// RequestsPerSecond and RequestBurst configure the per-caller rate limit.
	// A non-positive rate disables limiting.
	RequestsPerSecond float64
	RequestBurst      float64
}

// NewServerWithConfig returns a fully wired server.
func NewServerWithConfig(cfg ServerConfig) *Server {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	s := &Server{
		mgr:        cfg.Manager,
		compare:    cfg.Comparisons,
		store:      cfg.Store,
		sessions:   cfg.Sessions,
		limiter:    limiterFor(cfg.RequestsPerSecond, cfg.RequestBurst),
		idempotent: newIdempotencyCache(idempotencyTTL, idempotencyMaxEntries),
		origins:    newOriginPolicy(cfg.AllowedOrigins),
		metrics:    cfg.Metrics,
		logger:     cfg.Logger,
	}
	if cfg.Store != nil {
		s.replay = replay.NewReader(cfg.Store)
	}
	return s
}

// Routes builds the full HTTP handler, including the WebSocket stream and
// health checks, wrapped in the request middleware stack.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/v1/guest-sessions", s.createGuestSession)
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
		ws.HandlerWithTelemetry(s.streamLookup, s.metrics, s.logger))

	mux.HandleFunc("POST /api/v1/comparisons", s.createComparison)
	mux.HandleFunc("GET /api/v1/comparisons/{id}", s.getComparison)

	mux.HandleFunc("GET /health/live", health)
	mux.HandleFunc("GET /health/ready", s.ready)
	mux.HandleFunc("GET /metrics", s.serveMetrics)

	// outermost first: reject a disallowed origin before doing any work, cap
	// the body before reading it, throttle before authenticating (so an
	// unauthenticated flood is cheap), then identify the caller, then dedupe
	// their retries.
	return chain(mux,
		func(next http.Handler) http.Handler { return withCORS(next, s.origins) },
		limitBody,
		func(next http.Handler) http.Handler { return withTelemetry(next, s.logger) },
		s.rateLimit,
		s.authenticate,
		s.idempotency,
	)
}

// streamLookup adapts the manager's ownership-aware lookup to the WebSocket
// handler, which only has the request to work from.
func (s *Server) streamLookup(r *http.Request, id string) (*ws.Hub, ws.Snapshotter, bool) {
	return s.mgr.StreamLookup(id, service.TokenFrom(r.Context()))
}

type guestSessionResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
	Quota     int       `json:"simulationQuota"`
}

// createGuestSession issues the token every other route is scoped to. It is
// the one route that cannot require a session of its own.
func (s *Server) createGuestSession(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeError(w, http.StatusServiceUnavailable, "no_sessions", "this server does not issue guest sessions")
		return
	}

	session, err := s.sessions.Issue(r.Context())
	if err != nil {
		s.logger.Error("could not issue a guest session", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not issue a guest session")
		return
	}

	writeJSON(w, http.StatusCreated, guestSessionResponse{
		Token:     session.Token,
		ExpiresAt: session.ExpiresAt,
		Quota:     s.sessions.Quota(),
	})
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

	id, err := s.mgr.Create(r.Context(), "", seed, drivers)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, createResponse{ID: id, Seed: seed, Drivers: drivers})
}

func (s *Server) getSimulation(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.mgr.Snapshot(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

type createComparisonRequest struct {
	Seed    *int64  `json:"seed"`
	Drivers *int    `json:"drivers"`
	Demand  *string `json:"demand"`
}

// createComparison runs a fresh deterministic scenario for the given seed,
// driver count and demand level through both matching strategies and stores
// the result. The scenario itself is checked-in code (service.ScenarioFor),
// so the same three inputs always reproduce the same comparison.
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
	demand := service.DemandRush
	if req.Demand != nil {
		demand = service.NormalizeDemand(*req.Demand)
	}

	result := s.compare.Create(r.Context(), seed, drivers, demand)
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
	if !s.mayReadReplay(r, id) {
		// not-found rather than forbidden: a visitor should not be able to
		// probe which simulation ids exist.
		writeError(w, http.StatusNotFound, "not_found", "no replay exists for that simulation")
		return
	}

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

// mayReadReplay decides who can see a run's history. A showcase run is
// public — that is what a stable replay URL means. Anything else belongs to
// the session that created it. Runs with no owner were created by the server
// itself and are public too.
//
// The replay route is deliberately outside the authentication middleware, so
// a showcase link works for a visitor with no session at all; the ownership
// check therefore happens here, against the token if one was supplied.
func (s *Server) mayReadReplay(r *http.Request, id string) bool {
	if s.store == nil {
		return false
	}

	sim, err := s.store.GetSimulation(r.Context(), id)
	if err != nil {
		// no metadata row: either unknown, or a run whose owner was already
		// pruned. Let the reader decide whether any history exists.
		return true
	}
	if sim.Showcase || sim.GuestToken == "" {
		return true
	}

	token := bearerToken(r)
	if token == "" || s.sessions == nil {
		return false
	}
	session, err := s.sessions.Validate(r.Context(), token)
	if err != nil {
		return false
	}
	return session.Token == sim.GuestToken
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
	case errors.Is(err, service.ErrQuota):
		writeError(w, http.StatusTooManyRequests, "quota",
			"this session already has as many simulations as it may; reset or reuse one")
	case errors.Is(err, service.ErrOrderLimit):
		writeError(w, http.StatusTooManyRequests, "order_limit",
			"this simulation has as many orders as it may hold; reset it to continue")
	case errors.Is(err, service.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "not your simulation")
	case errors.Is(err, service.ErrBusy):
		writeError(w, http.StatusServiceUnavailable, "busy", "simulation is busy, retry shortly")
	default:
		writeError(w, http.StatusInternalServerError, "internal", "unexpected error")
	}
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
