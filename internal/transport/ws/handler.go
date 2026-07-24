package ws

import (
	"context"
	"log/slog"
	"net/http"

	"dispatchlab/internal/domain"
	"dispatchlab/internal/telemetry"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	// origin is validated by the transport's CORS middleware, which wraps
	// this route too and rejects a disallowed origin before the upgrade is
	// ever attempted. Repeating the check here with gorilla's default (which
	// only compares against the Host header) would reject the deployed
	// frontend, since it is served from a different origin than the API.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Snapshotter supplies a current-state snapshot for a reconnecting client.
type Snapshotter interface {
	CurrentSnapshot() domain.Event
}

// Lookup resolves a simulation id to its fanout hub and snapshot source. It
// takes the request as well as the id because a stream is exactly as private
// as the run behind it: whoever provides the lookup decides, from the
// request's identity, whether this caller may attach.
type Lookup func(r *http.Request, id string) (*Hub, Snapshotter, bool)

// Handler streams one simulation's events to a browser. On connect it sends a
// current snapshot and then only events newer than that snapshot, so a client
// that reconnects resumes cleanly from a known sequence with no gap or
// duplicate.
func Handler(lookup Lookup) http.HandlerFunc {
	return HandlerWithTelemetry(lookup, nil, nil)
}

// HandlerWithTelemetry is Handler with connection counting and per-event
// publication tracing. An event that arrived carrying a trace gets a
// publication span linked to it, which closes the loop from an HTTP command
// to the moment its result reaches a browser.
func HandlerWithTelemetry(lookup Lookup, metrics *telemetry.Metrics, logger *slog.Logger) http.HandlerFunc {
	if logger == nil {
		logger = slog.Default()
	}

	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		hub, snap, ok := lookup(r, id)
		if !ok {
			http.NotFound(w, r)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			logger.Warn("websocket upgrade failed", "simulation_id", id, "error", err)
			return
		}
		defer conn.Close()

		metrics.WebSocketClients().Inc()
		defer metrics.WebSocketClients().Dec()
		logger.Info("websocket client connected", "simulation_id", id)
		defer logger.Info("websocket client disconnected", "simulation_id", id)

		// detect client disconnect. closing the channel rather than relying on
		// the next write failing matters for an idle simulation: with no
		// events to send, a write-only check would leave this goroutine and
		// its subscription alive long after the browser was gone.
		disconnected := make(chan struct{})
		go func() {
			defer close(disconnected)
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					conn.Close()
					return
				}
			}
		}()

		// subscribe before snapshotting so no event emitted in between is lost;
		// events at or below the snapshot's sequence are already reflected in it.
		sub := hub.Subscribe()
		defer hub.Unsubscribe(sub)

		snapshot := snap.CurrentSnapshot()
		if err := conn.WriteJSON(snapshot); err != nil {
			return
		}
		lastSeq := snapshot.Sequence

		for {
			select {
			case <-disconnected:
				return
			case event, ok := <-sub:
				if !ok {
					return
				}
				if event.Sequence <= lastSeq && event.Type != domain.EventSimulationSnapshot {
					continue
				}
				if event.Sequence > lastSeq {
					lastSeq = event.Sequence
				}
				if err := publish(conn, event, logger); err != nil {
					return
				}
			}
		}
	}
}

func publish(conn *websocket.Conn, event domain.Event, logger *slog.Logger) error {
	if event.TraceID == "" {
		return conn.WriteJSON(event)
	}

	ctx := telemetry.WithSpanContext(
		telemetry.WithLogger(context.Background(), logger),
		telemetry.SpanContext{TraceID: event.TraceID},
	)
	_, span := telemetry.StartSpan(ctx, "ws.publish",
		slog.String("simulation_id", event.SimulationID),
		slog.Int("sequence", event.Sequence),
		slog.String("event_type", string(event.Type)))
	defer span.End()

	err := conn.WriteJSON(event)
	span.RecordError(err)
	return err
}
