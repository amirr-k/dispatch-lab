package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"dispatchlab/internal/domain"
	"dispatchlab/internal/service"
)

func newTestServer() *Server {
	return NewServer(service.NewManager(0))
}

func doJSON(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestCreateSimulation(t *testing.T) {
	s := newTestServer()
	rec := doJSON(t, s.Routes(), http.MethodPost, "/api/v1/simulations", createRequest{})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp createResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ID == "" {
		t.Fatal("expected a non-empty simulation id")
	}
	if resp.Drivers != defaultDrivers {
		t.Fatalf("expected default driver count %d, got %d", defaultDrivers, resp.Drivers)
	}
}

func TestCreateSimulationClampsDriverCount(t *testing.T) {
	s := newTestServer()
	huge := maxDrivers + 500
	rec := doJSON(t, s.Routes(), http.MethodPost, "/api/v1/simulations", createRequest{Drivers: &huge})
	var resp createResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Drivers != maxDrivers {
		t.Fatalf("expected driver count clamped to %d, got %d", maxDrivers, resp.Drivers)
	}
}

func TestGetSimulationNotFound(t *testing.T) {
	s := newTestServer()
	rec := doJSON(t, s.Routes(), http.MethodGet, "/api/v1/simulations/missing", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("expected a structured error envelope, decode failed: %v", err)
	}
	if body.Error.Code != "not_found" {
		t.Fatalf("expected error code not_found, got %s", body.Error.Code)
	}
}

func TestGetSimulationReturnsSnapshot(t *testing.T) {
	s := newTestServer()
	created := doJSON(t, s.Routes(), http.MethodPost, "/api/v1/simulations", createRequest{})
	var resp createResponse
	json.Unmarshal(created.Body.Bytes(), &resp)

	rec := doJSON(t, s.Routes(), http.MethodGet, "/api/v1/simulations/"+resp.ID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPlaceOrderRejectsMissingFields(t *testing.T) {
	s := newTestServer()
	created := doJSON(t, s.Routes(), http.MethodPost, "/api/v1/simulations", createRequest{})
	var resp createResponse
	json.Unmarshal(created.Body.Bytes(), &resp)

	rec := doJSON(t, s.Routes(), http.MethodPost, "/api/v1/simulations/"+resp.ID+"/orders", placeOrderRequest{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing pickup/destination, got %d", rec.Code)
	}
}

func TestPlaceOrderOnMissingSimulation(t *testing.T) {
	s := newTestServer()
	rec := doJSON(t, s.Routes(), http.MethodPost, "/api/v1/simulations/missing/orders",
		placeOrderRequest{Pickup: "n-0-0", Destination: "n-1-1"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestCloseRoadRejectsMissingEdgeID(t *testing.T) {
	s := newTestServer()
	created := doJSON(t, s.Routes(), http.MethodPost, "/api/v1/simulations", createRequest{})
	var resp createResponse
	json.Unmarshal(created.Body.Bytes(), &resp)

	rec := doJSON(t, s.Routes(), http.MethodPost, "/api/v1/simulations/"+resp.ID+"/closures", closeRoadRequest{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing edgeId, got %d", rec.Code)
	}
}

func TestCloseRoadOnMissingSimulation(t *testing.T) {
	s := newTestServer()
	rec := doJSON(t, s.Routes(), http.MethodPost, "/api/v1/simulations/missing/closures", closeRoadRequest{EdgeID: "e-a-b"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestCloseRoadAccepted(t *testing.T) {
	s := newTestServer()
	created := doJSON(t, s.Routes(), http.MethodPost, "/api/v1/simulations", createRequest{})
	var resp createResponse
	json.Unmarshal(created.Body.Bytes(), &resp)

	snapRec := doJSON(t, s.Routes(), http.MethodGet, "/api/v1/simulations/"+resp.ID, nil)
	var snap struct {
		Payload struct {
			Edges []struct {
				ID string `json:"id"`
			} `json:"edges"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(snapRec.Body.Bytes(), &snap); err != nil || len(snap.Payload.Edges) == 0 {
		t.Fatalf("expected a snapshot with at least one edge, err=%v body=%s", err, snapRec.Body.String())
	}

	rec := doJSON(t, s.Routes(), http.MethodPost, "/api/v1/simulations/"+resp.ID+"/closures",
		closeRoadRequest{EdgeID: domain.EdgeID(snap.Payload.Edges[0].ID)})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPauseResumeReset(t *testing.T) {
	s := newTestServer()
	created := doJSON(t, s.Routes(), http.MethodPost, "/api/v1/simulations", createRequest{})
	var resp createResponse
	json.Unmarshal(created.Body.Bytes(), &resp)
	base := "/api/v1/simulations/" + resp.ID

	for _, path := range []string{base + "/pause", base + "/resume", base + "/reset"} {
		rec := doJSON(t, s.Routes(), http.MethodPost, path, nil)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("%s: expected 202, got %d: %s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestSetSpeedRejectsNonPositive(t *testing.T) {
	s := newTestServer()
	created := doJSON(t, s.Routes(), http.MethodPost, "/api/v1/simulations", createRequest{})
	var resp createResponse
	json.Unmarshal(created.Body.Bytes(), &resp)

	rec := doJSON(t, s.Routes(), http.MethodPost, "/api/v1/simulations/"+resp.ID+"/speed", speedRequest{Multiplier: 0})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-positive multiplier, got %d", rec.Code)
	}
}

func TestMalformedJSONBodyReturns400(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/simulations", bytes.NewBufferString("{not json"))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = -1 // force decode() past the empty-body fast path
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for malformed JSON, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHealthEndpoints(t *testing.T) {
	s := newTestServer()
	for _, path := range []string{"/health/live", "/health/ready"} {
		rec := doJSON(t, s.Routes(), http.MethodGet, path, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", path, rec.Code)
		}
	}
}

func TestCORSPreflight(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/simulations", nil)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for CORS preflight, got %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("expected CORS header on preflight response")
	}
}
