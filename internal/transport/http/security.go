package http

import (
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"dispatchlab/internal/ratelimit"
	"dispatchlab/internal/service"
)

const (
	// maxRequestBytes bounds a request body. Every command this API takes is
	// a handful of fields; anything larger is a mistake or an attack.
	maxRequestBytes = 16 << 10
	// bearerPrefix is the scheme guest tokens arrive under.
	bearerPrefix = "Bearer "
)

// authenticate resolves the request's guest token and puts it on the context
// for the manager's ownership checks. Routes listed as public skip it.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.sessions == nil || isPublicRoute(r) {
			next.ServeHTTP(w, r)
			return
		}

		session, err := s.sessions.Validate(r.Context(), bearerToken(r))
		if err != nil {
			if errors.Is(err, service.ErrNoSession) {
				writeError(w, http.StatusUnauthorized, "no_session",
					"missing or expired guest session; request one from POST /api/v1/guest-sessions")
				return
			}
			s.logger.Error("could not validate a guest session", "error", err)
			writeError(w, http.StatusInternalServerError, "internal", "unexpected error")
			return
		}

		next.ServeHTTP(w, r.WithContext(service.WithToken(r.Context(), session.Token)))
	})
}

// isPublicRoute reports whether a request may proceed without a session.
// Issuing a session obviously cannot require one; health and metrics are
// operational; and a showcase replay is public by design, which the replay
// handler enforces per-run.
func isPublicRoute(r *http.Request) bool {
	path := r.URL.Path
	switch {
	case path == "/api/v1/guest-sessions":
		return true
	case strings.HasPrefix(path, "/health/"), path == "/metrics":
		return true
	case strings.HasSuffix(path, "/replay"):
		return true
	default:
		return false
	}
}

func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if strings.HasPrefix(header, bearerPrefix) {
		return strings.TrimSpace(strings.TrimPrefix(header, bearerPrefix))
	}
	// the WebSocket API cannot set headers from a browser, so the stream
	// route accepts the token as a query parameter instead.
	return r.URL.Query().Get("token")
}

// rateLimit throttles by guest token when there is one and by client address
// otherwise, so an unauthenticated flood of session requests is bounded too.
func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.limiter == nil || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		key := bearerToken(r)
		if key == "" {
			key = clientIP(r)
		}

		if ok, retry := s.limiter.Allow(key); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(retry.Round(time.Second).Seconds())))
			writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests, slow down")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request) string {
	// X-Forwarded-For is only meaningful behind a proxy that sets it; Render
	// terminates TLS at one, so take the first hop.
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		if first, _, found := strings.Cut(forwarded, ","); found {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(forwarded)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// limitBody caps how much of a request body will be read. MaxBytesReader
// makes the read itself fail past the cap, so a large body is refused rather
// than buffered.
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// originPolicy decides which browser origins may call this API.
type originPolicy struct {
	allowed map[string]bool
	// permissive is the local-development mode: any origin, which is what
	// makes a vite dev server on a different port work.
	permissive bool
}

// newOriginPolicy parses a comma-separated allowlist. An empty list is
// permissive, which is the right default for a clone-and-run demo and is
// overridden in deployment by setting ALLOWED_ORIGINS.
func newOriginPolicy(origins []string) *originPolicy {
	policy := &originPolicy{allowed: make(map[string]bool)}
	for _, origin := range origins {
		origin = strings.TrimSpace(origin)
		if origin != "" {
			policy.allowed[origin] = true
		}
	}
	policy.permissive = len(policy.allowed) == 0
	return policy
}

// permits reports whether an Origin header may be served. A request with no
// Origin is not a browser cross-origin request at all — curl, a health
// probe — and is allowed.
func (p *originPolicy) permits(origin string) bool {
	if origin == "" || p.permissive {
		return true
	}
	return p.allowed[origin]
}

// withCORS answers preflights and reflects only permitted origins. It never
// echoes an arbitrary origin back, which is what would make the allowlist
// decorative.
func withCORS(next http.Handler, policy *originPolicy) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if !policy.permits(origin) {
			writeError(w, http.StatusForbidden, "origin_not_allowed", "origin is not allowed")
			return
		}

		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Idempotency-Key")
		w.Header().Set("Access-Control-Expose-Headers", "X-Trace-Id, Retry-After")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// idempotency replays the outcome of a command a client already sent rather
// than applying it twice. A browser retrying a request it never saw the
// response to is the case this exists for: placing the same order twice
// because of a flaky connection is a real, visible bug.
func (s *Server) idempotency(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" || r.Method != http.MethodPost || s.idempotent == nil {
			next.ServeHTTP(w, r)
			return
		}
		if len(key) > 200 {
			writeError(w, http.StatusBadRequest, "invalid_request", "idempotency key is too long")
			return
		}

		// scope the key to the session so one visitor cannot replay or
		// collide with another's command.
		scoped := service.TokenFrom(r.Context()) + "\x00" + r.URL.Path + "\x00" + key

		if replayed, ok := s.idempotent.Get(scoped); ok {
			w.Header().Set("Idempotent-Replay", "true")
			writeStored(w, replayed)
			return
		}

		recorder := &bodyRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)

		// only successful commands are remembered: a failure the client can
		// fix by retrying should not be pinned to its first outcome.
		if recorder.status < 300 {
			s.idempotent.Put(scoped, storedResponse{
				status:      recorder.status,
				body:        recorder.body,
				contentType: w.Header().Get("Content-Type"),
			})
		}
	})
}

// bodyRecorder keeps a copy of a response so it can be replayed later.
type bodyRecorder struct {
	http.ResponseWriter
	status int
	body   []byte
}

func (r *bodyRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *bodyRecorder) Write(b []byte) (int, error) {
	r.body = append(r.body, b...)
	return r.ResponseWriter.Write(b)
}

func writeStored(w http.ResponseWriter, stored storedResponse) {
	if stored.contentType != "" {
		w.Header().Set("Content-Type", stored.contentType)
	}
	w.WriteHeader(stored.status)
	_, _ = w.Write(stored.body)
}

// chain applies middleware so the first listed runs outermost.
func chain(h http.Handler, middleware ...func(http.Handler) http.Handler) http.Handler {
	for i := len(middleware) - 1; i >= 0; i-- {
		h = middleware[i](h)
	}
	return h
}

// limiterFor builds the request limiter from a per-second allowance.
func limiterFor(perSecond, burst float64) *ratelimit.Limiter {
	return ratelimit.New(ratelimit.Config{PerSecond: perSecond, Burst: burst})
}
