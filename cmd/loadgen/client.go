package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// client drives one simulated visitor's REST calls against a running
// dispatchlab server. It holds no state of its own beyond the guest token,
// mirroring how the real frontend treats a session.
type client struct {
	addr  string
	token string
	http  *http.Client
}

func newClient(addr string) *client {
	return &client{addr: addr, http: &http.Client{Timeout: 10 * time.Second}}
}

func (c *client) request(ctx context.Context, method, path string, body any) (*http.Response, time.Duration, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.addr+path, reader)
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	start := time.Now()
	resp, err := c.http.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		return nil, elapsed, fmt.Errorf("%s %s: %w", method, path, err)
	}
	return resp, elapsed, nil
}

// issueSession gets a guest token the way a real visitor's first page load
// does. Every subsequent call on this client uses it.
func (c *client) issueSession(ctx context.Context) (time.Duration, error) {
	resp, elapsed, err := c.request(ctx, http.MethodPost, "/api/v1/guest-sessions", nil)
	if err != nil {
		return elapsed, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return elapsed, statusError(resp)
	}

	var decoded struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return elapsed, fmt.Errorf("decode guest session: %w", err)
	}
	c.token = decoded.Token
	return elapsed, nil
}

// createSimulation makes a new run and returns its id plus two real node ids
// from its generated city, taken from the snapshot rather than assumed, so
// this tool keeps working if the default grid size ever changes.
func (c *client) createSimulation(ctx context.Context, drivers int) (id string, pickup, destination string, elapsed time.Duration, err error) {
	resp, elapsed, err := c.request(ctx, http.MethodPost, "/api/v1/simulations", map[string]any{"drivers": drivers})
	if err != nil {
		return "", "", "", elapsed, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", "", "", elapsed, statusError(resp)
	}

	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return "", "", "", elapsed, fmt.Errorf("decode created simulation: %w", err)
	}

	snapResp, _, err := c.request(ctx, http.MethodGet, "/api/v1/simulations/"+created.ID, nil)
	if err != nil {
		return "", "", "", elapsed, err
	}
	defer snapResp.Body.Close()

	var snapshot struct {
		Payload struct {
			Nodes []struct {
				ID string `json:"id"`
			} `json:"nodes"`
		} `json:"payload"`
	}
	if err := json.NewDecoder(snapResp.Body).Decode(&snapshot); err != nil {
		return "", "", "", elapsed, fmt.Errorf("decode snapshot: %w", err)
	}
	if len(snapshot.Payload.Nodes) < 2 {
		return "", "", "", elapsed, fmt.Errorf("simulation %s has no nodes to order between", created.ID)
	}

	nodes := snapshot.Payload.Nodes
	return created.ID, nodes[0].ID, nodes[len(nodes)-1].ID, elapsed, nil
}

// placeOrder submits one order and reports whether the server accepted it.
// A quota or rate-limit refusal is not treated as an error by the caller -
// it is exactly the boundary these tests exist to find.
func (c *client) placeOrder(ctx context.Context, simID, pickup, destination string) (status int, elapsed time.Duration, err error) {
	resp, elapsed, err := c.request(ctx, http.MethodPost, "/api/v1/simulations/"+simID+"/orders", map[string]string{
		"pickup":      pickup,
		"destination": destination,
	})
	if err != nil {
		return 0, elapsed, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, elapsed, nil
}

// streamURL builds the WebSocket address for a simulation, carrying the
// token as a query parameter since a browser cannot set a header on the
// handshake - the same constraint the real frontend works around.
func (c *client) streamURL(simID string) string {
	base := strings.Replace(c.addr, "http://", "ws://", 1)
	base = strings.Replace(base, "https://", "wss://", 1)
	return fmt.Sprintf("%s/api/v1/simulations/%s/stream?token=%s", base, simID, c.token)
}

func (c *client) dialStream(ctx context.Context, simID string) (*websocket.Conn, error) {
	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, c.streamURL(simID), nil)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("stream handshake: %s", resp.Status)
		}
		return nil, fmt.Errorf("stream handshake: %w", err)
	}
	return conn, nil
}

func (c *client) markShowcase(ctx context.Context, simID string) error {
	resp, _, err := c.request(ctx, http.MethodPost, "/api/v1/simulations/"+simID+"/showcase", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return statusError(resp)
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}

func (c *client) setPaused(ctx context.Context, simID string, paused bool) error {
	path := "/api/v1/simulations/" + simID + "/resume"
	if paused {
		path = "/api/v1/simulations/" + simID + "/pause"
	}
	resp, _, err := c.request(ctx, http.MethodPost, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusAccepted {
		return statusError(resp)
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}

func (c *client) fetchReplaySequences(ctx context.Context, simID string) (map[int]bool, error) {
	resp, _, err := c.request(ctx, http.MethodGet, "/api/v1/simulations/"+simID+"/replay", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp)
	}
	var log struct {
		Events []struct {
			Sequence int    `json:"sequence"`
			Type     string `json:"type"`
		} `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&log); err != nil {
		return nil, fmt.Errorf("decode replay: %w", err)
	}
	out := make(map[int]bool, len(log.Events))
	for _, e := range log.Events {
		if e.Type == "simulation.snapshot" {
			continue
		}
		out[e.Sequence] = true
	}
	return out, nil
}

func statusError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("%s: %d %s", resp.Request.URL.Path, resp.StatusCode, string(body))
}
