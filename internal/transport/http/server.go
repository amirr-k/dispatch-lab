// Package http exposes the REST command API and mounts the WebSocket stream.
package http

import (
	"encoding/json"
	"errors"
	"math/rand"
	"net/http"

	"dispatchlab/internal/domain"
	"dispatchlab/internal/service"
	"dispatchlab/internal/transport/ws"
)

const (
	defaultDrivers = 12
	maxDrivers     = 40
)

// Server adapts the service Manager to HTTP.
type Server struct {
	mgr *service.Manager
}

func NewServer(mgr *service.Manager) *Server {
	return &Server{mgr: mgr}
}

// Routes builds the full HTTP handler, including the WebSocket stream and
// health checks, wrapped in permissive dev CORS.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/v1/simulations", s.createSimulation)
	mux.HandleFunc("GET /api/v1/simulations/{id}", s.getSimulation)
	mux.HandleFunc("POST /api/v1/simulations/{id}/orders", s.placeOrder)
	mux.HandleFunc("POST /api/v1/simulations/{id}/pause", s.pause)
	mux.HandleFunc("POST /api/v1/simulations/{id}/resume", s.resume)
	mux.HandleFunc("POST /api/v1/simulations/{id}/reset", s.reset)
	mux.HandleFunc("POST /api/v1/simulations/{id}/speed", s.setSpeed)
	mux.HandleFunc("GET /api/v1/simulations/{id}/stream", ws.Handler(s.mgr.StreamLookup))

	mux.HandleFunc("GET /health/live", health)
	mux.HandleFunc("GET /health/ready", health)

	return withCORS(mux)
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
	if err := s.mgr.PlaceOrder(r.PathValue("id"), req.Pickup, req.Destination); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) pause(w http.ResponseWriter, r *http.Request)  { s.setPaused(w, r, true) }
func (s *Server) resume(w http.ResponseWriter, r *http.Request) { s.setPaused(w, r, false) }

func (s *Server) setPaused(w http.ResponseWriter, r *http.Request, paused bool) {
	if err := s.mgr.SetPaused(r.PathValue("id"), paused); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) reset(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.Reset(r.PathValue("id")); err != nil {
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
	if err := s.mgr.SetSpeed(r.PathValue("id"), req.Multiplier); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func health(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

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
