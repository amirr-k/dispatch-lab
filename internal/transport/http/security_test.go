package http

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"dispatchlab/internal/domain"
	"dispatchlab/internal/service"
	"dispatchlab/internal/store"
	"dispatchlab/internal/telemetry"
)

// newSecureServer wires the full production middleware stack: sessions, a
// store, and whatever origin/rate configuration the test needs.
func newSecureServer(t *testing.T, cfg ServerConfig) (*Server, store.Store) {
	t.Helper()

	s := store.NewMemory()
	metrics := telemetry.NewMetrics()
	sessions := service.NewSessions(service.SessionsConfig{Store: s, Quota: 2})
	mgr := service.NewManagerWithConfig(service.ManagerConfig{
		Store: s, Metrics: metrics, Sessions: sessions,
	})
	t.Cleanup(mgr.Shutdown)

	cfg.Manager = mgr
	cfg.Store = s
	cfg.Sessions = sessions
	cfg.Metrics = metrics
	if cfg.Comparisons == nil {
		cfg.Comparisons = service.NewComparisonsWithStore(s, metrics, nil)
	}
	return NewServerWithConfig(cfg), s
}

// do sends a request with optional auth and headers.
func do(t *testing.T, h http.Handler, method, path, token string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}

	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// issueToken gets a guest session the way a browser would.
func issueToken(t *testing.T, h http.Handler) string {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/api/v1/guest-sessions", "", nil, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("guest session: %d %s", rec.Code, rec.Body.String())
	}
	var resp guestSessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Token == "" {
		t.Fatal("issued an empty token")
	}
	return resp.Token
}

func createSim(t *testing.T, h http.Handler, token string) string {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/api/v1/simulations", token, createRequest{}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create simulation: %d %s", rec.Code, rec.Body.String())
	}
	var resp createResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.ID
}

func TestCommandsRequireAGuestSession(t *testing.T) {
	server, _ := newSecureServer(t, ServerConfig{})
	h := server.Routes()

	protected := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/v1/simulations"},
		{http.MethodGet, "/api/v1/simulations/any"},
		{http.MethodPost, "/api/v1/simulations/any/orders"},
		{http.MethodPost, "/api/v1/simulations/any/closures"},
		{http.MethodDelete, "/api/v1/simulations/any/closures?edgeId=e-a-b"},
		{http.MethodPost, "/api/v1/simulations/any/pause"},
		{http.MethodPost, "/api/v1/simulations/any/reset"},
		{http.MethodPost, "/api/v1/simulations/any/showcase"},
		{http.MethodPost, "/api/v1/comparisons"},
	}
	for _, route := range protected {
		rec := do(t, h, route.method, route.path, "", nil, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a token = %d, want 401", route.method, route.path, rec.Code)
		}
	}

	// an invented token is no better than none.
	rec := do(t, h, http.MethodPost, "/api/v1/simulations", "made-up-token", createRequest{}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("a forged token = %d, want 401", rec.Code)
	}
}

func TestPublicRoutesNeedNoSession(t *testing.T) {
	server, _ := newSecureServer(t, ServerConfig{})
	h := server.Routes()

	for _, path := range []string{"/health/live", "/health/ready", "/metrics"} {
		if rec := do(t, h, http.MethodGet, path, "", nil, nil); rec.Code != http.StatusOK {
			t.Errorf("GET %s without a token = %d, want 200", path, rec.Code)
		}
	}
	if rec := do(t, h, http.MethodPost, "/api/v1/guest-sessions", "", nil, nil); rec.Code != http.StatusCreated {
		t.Errorf("issuing a session = %d, want 201", rec.Code)
	}
}

// the central access rule: one visitor cannot touch another's run.
func TestOneSessionCannotReachAnothersSimulation(t *testing.T) {
	server, _ := newSecureServer(t, ServerConfig{})
	h := server.Routes()

	mine := issueToken(t, h)
	theirs := issueToken(t, h)
	id := createSim(t, h, mine)

	// reported as not-found, so an id cannot be probed for existence.
	attempts := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/api/v1/simulations/" + id, nil},
		{http.MethodPost, "/api/v1/simulations/" + id + "/orders", placeOrderRequest{Pickup: "n-0-0", Destination: "n-1-1"}},
		{http.MethodPost, "/api/v1/simulations/" + id + "/pause", nil},
		{http.MethodPost, "/api/v1/simulations/" + id + "/reset", nil},
		{http.MethodPost, "/api/v1/simulations/" + id + "/closures", closeRoadRequest{EdgeID: "e-n-0-0-n-1-0"}},
		{http.MethodDelete, "/api/v1/simulations/" + id + "/closures?edgeId=e-n-0-0-n-1-0", nil},
		{http.MethodPost, "/api/v1/simulations/" + id + "/showcase", nil},
	}
	for _, attempt := range attempts {
		rec := do(t, h, attempt.method, attempt.path, theirs, attempt.body, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s as another session = %d, want 404", attempt.method, attempt.path, rec.Code)
		}
	}

	// and the owner is unaffected.
	if rec := do(t, h, http.MethodGet, "/api/v1/simulations/"+id, mine, nil, nil); rec.Code != http.StatusOK {
		t.Errorf("the owning session was refused its own run: %d", rec.Code)
	}
}

func TestStreamIsScopedToItsSession(t *testing.T) {
	server, _ := newSecureServer(t, ServerConfig{})
	h := server.Routes()

	mine := issueToken(t, h)
	theirs := issueToken(t, h)
	id := createSim(t, h, mine)

	// the websocket handshake fails before upgrading, so this is a plain 404.
	rec := do(t, h, http.MethodGet, "/api/v1/simulations/"+id+"/stream", theirs, nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("streaming another session's run = %d, want 404", rec.Code)
	}
}

func TestSessionQuotaLimitsConcurrentRuns(t *testing.T) {
	server, _ := newSecureServer(t, ServerConfig{})
	h := server.Routes()
	token := issueToken(t, h)

	// the test server's quota is 2.
	createSim(t, h, token)
	createSim(t, h, token)

	rec := do(t, h, http.MethodPost, "/api/v1/simulations", token, createRequest{}, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("a third simulation = %d, want 429", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "quota") {
		t.Errorf("the refusal does not explain itself: %s", rec.Body.String())
	}

	// a different visitor is unaffected by the first one's usage.
	other := issueToken(t, h)
	if rec := do(t, h, http.MethodPost, "/api/v1/simulations", other, createRequest{}, nil); rec.Code != http.StatusCreated {
		t.Errorf("a second session was refused because of the first: %d", rec.Code)
	}
}

func TestRateLimitRefusesAFlood(t *testing.T) {
	server, _ := newSecureServer(t, ServerConfig{RequestsPerSecond: 1, RequestBurst: 3})
	h := server.Routes()

	limited := false
	for i := 0; i < 20; i++ {
		rec := do(t, h, http.MethodPost, "/api/v1/guest-sessions", "", nil, nil)
		if rec.Code == http.StatusTooManyRequests {
			limited = true
			if rec.Header().Get("Retry-After") == "" {
				t.Error("a rate-limited response carries no Retry-After header")
			}
			break
		}
	}
	if !limited {
		t.Error("twenty requests against a burst of three were never limited")
	}
}

func TestPayloadsAreCapped(t *testing.T) {
	server, _ := newSecureServer(t, ServerConfig{})
	h := server.Routes()
	token := issueToken(t, h)
	id := createSim(t, h, token)

	huge := strings.Repeat("x", maxRequestBytes*2)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/simulations/"+id+"/orders",
		strings.NewReader(fmt.Sprintf(`{"pickup":"%s","destination":"n-1-1"}`, huge)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("an oversized body = %d, want 400", rec.Code)
	}
}

func TestOriginAllowlistIsEnforced(t *testing.T) {
	server, _ := newSecureServer(t, ServerConfig{AllowedOrigins: []string{"https://demo.example"}})
	h := server.Routes()

	rec := do(t, h, http.MethodPost, "/api/v1/guest-sessions", "", nil,
		map[string]string{"Origin": "https://demo.example"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("an allowed origin = %d, want 201", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://demo.example" {
		t.Errorf("Access-Control-Allow-Origin = %q", got)
	}

	rec = do(t, h, http.MethodPost, "/api/v1/guest-sessions", "", nil,
		map[string]string{"Origin": "https://evil.example"})
	if rec.Code != http.StatusForbidden {
		t.Errorf("a disallowed origin = %d, want 403", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("a refused origin was still echoed back in the CORS header")
	}

	// a request with no Origin is not a browser cross-origin call at all.
	if rec := do(t, h, http.MethodGet, "/health/live", "", nil, nil); rec.Code != http.StatusOK {
		t.Errorf("a request with no Origin = %d, want 200", rec.Code)
	}
}

// a browser that retries a command it never saw the response to must not
// place the order twice.
func TestIdempotencyKeyReplaysTheFirstOutcome(t *testing.T) {
	server, _ := newSecureServer(t, ServerConfig{})
	h := server.Routes()
	token := issueToken(t, h)

	headers := map[string]string{"Idempotency-Key": "abc-123"}

	first := do(t, h, http.MethodPost, "/api/v1/simulations", token, createRequest{}, headers)
	if first.Code != http.StatusCreated {
		t.Fatalf("first request = %d", first.Code)
	}
	second := do(t, h, http.MethodPost, "/api/v1/simulations", token, createRequest{}, headers)
	if second.Code != http.StatusCreated {
		t.Fatalf("replayed request = %d", second.Code)
	}
	if second.Header().Get("Idempotent-Replay") != "true" {
		t.Error("the replay is not marked as one")
	}
	if first.Body.String() != second.Body.String() {
		t.Errorf("the retry created something new:\n %s\n %s", first.Body.String(), second.Body.String())
	}

	// a different key is a different command.
	third := do(t, h, http.MethodPost, "/api/v1/simulations", token, createRequest{},
		map[string]string{"Idempotency-Key": "different"})
	if third.Body.String() == first.Body.String() {
		t.Error("a distinct idempotency key replayed an unrelated command")
	}
}

// keys are scoped per session, so one visitor's key cannot collide with or
// replay another's.
func TestIdempotencyKeysAreScopedToTheSession(t *testing.T) {
	server, _ := newSecureServer(t, ServerConfig{})
	h := server.Routes()

	mine := issueToken(t, h)
	theirs := issueToken(t, h)
	headers := map[string]string{"Idempotency-Key": "same-key"}

	first := do(t, h, http.MethodPost, "/api/v1/simulations", mine, createRequest{}, headers)
	second := do(t, h, http.MethodPost, "/api/v1/simulations", theirs, createRequest{}, headers)

	if first.Body.String() == second.Body.String() {
		t.Error("two sessions using the same key got the same simulation")
	}
	if second.Header().Get("Idempotent-Replay") == "true" {
		t.Error("another session's key was treated as a replay")
	}
}

func TestOrderLimitPerRun(t *testing.T) {
	server, _ := newSecureServer(t, ServerConfig{})
	h := server.Routes()
	token := issueToken(t, h)
	id := createSim(t, h, token)

	// find two real node ids from the snapshot.
	rec := do(t, h, http.MethodGet, "/api/v1/simulations/"+id, token, nil, nil)
	var snapshot struct {
		Payload struct {
			Nodes []struct {
				ID string `json:"id"`
			} `json:"nodes"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	nodes := snapshot.Payload.Nodes
	if len(nodes) < 2 {
		t.Fatal("the snapshot has no nodes to order between")
	}
	order := placeOrderRequest{
		Pickup:      domain.NodeID(nodes[0].ID),
		Destination: domain.NodeID(nodes[len(nodes)-1].ID),
	}

	// a synchronous flood outruns the actor loop and fills the command
	// buffer, so a 503 here is backpressure rather than the limit we are
	// testing — wait for the actor to drain and re-send the same order.
	limited := false
	placed := 0
	for attempts := 0; attempts < 4000 && placed <= service.MaxOrdersPerRun; attempts++ {
		rec := do(t, h, http.MethodPost, "/api/v1/simulations/"+id+"/orders", token, order, nil)
		switch rec.Code {
		case http.StatusTooManyRequests:
			limited = true
			if !strings.Contains(rec.Body.String(), "order_limit") {
				t.Errorf("the refusal is not about the order limit: %s", rec.Body.String())
			}
		case http.StatusServiceUnavailable:
			time.Sleep(time.Millisecond)
			continue
		case http.StatusAccepted:
			placed++
			continue
		default:
			t.Fatalf("order %d = %d %s", placed, rec.Code, rec.Body.String())
		}
		break
	}
	if !limited {
		t.Errorf("the order limit never refused a request after %d accepted orders", placed)
	}
	if limited && placed != service.MaxOrdersPerRun {
		t.Errorf("refused after %d orders, want the limit of %d", placed, service.MaxOrdersPerRun)
	}
}

// an error response must never leak internals.
func TestErrorsCarryNoInternalDetail(t *testing.T) {
	server, _ := newSecureServer(t, ServerConfig{})
	h := server.Routes()

	rec := do(t, h, http.MethodPost, "/api/v1/simulations", "bad-token", createRequest{}, nil)

	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("an error response is not the documented envelope: %v", err)
	}
	if body.Error.Code == "" || body.Error.Message == "" {
		t.Errorf("incomplete error envelope: %+v", body)
	}
	for _, leak := range []string{"goroutine", ".go:", "dispatchlab/internal", "0x"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("error response leaks internals (%q): %s", leak, rec.Body.String())
		}
	}
}

// a showcase replay is public: that is what a stable URL means. A private run
// is not.
func TestReplayVisibilityFollowsShowcaseStatus(t *testing.T) {
	server, s := newSecureServer(t, ServerConfig{})
	h := server.Routes()

	mine := issueToken(t, h)
	theirs := issueToken(t, h)
	id := createSim(t, h, mine)

	waitFor(t, 3*time.Second, func() bool {
		seq, err := s.LatestSequence(t.Context(), id)
		return err == nil && seq > 0
	})

	if rec := do(t, h, http.MethodGet, "/api/v1/simulations/"+id+"/replay", theirs, nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("another session read a private replay: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/api/v1/simulations/"+id+"/replay", "", nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("an anonymous caller read a private replay: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/api/v1/simulations/"+id+"/replay", mine, nil, nil); rec.Code != http.StatusOK {
		t.Errorf("the owner could not read their own replay: %d", rec.Code)
	}

	if rec := do(t, h, http.MethodPost, "/api/v1/simulations/"+id+"/showcase", mine, nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("showcase = %d", rec.Code)
	}

	// once saved, anyone with the link can watch it.
	if rec := do(t, h, http.MethodGet, "/api/v1/simulations/"+id+"/replay", "", nil, nil); rec.Code != http.StatusOK {
		t.Errorf("a saved replay is not publicly readable: %d", rec.Code)
	}
}
