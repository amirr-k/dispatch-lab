package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// capture returns a logger writing JSON into buf, plus a decoder for the
// records it emitted.
func capture() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}

func records(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %v (%q)", err, line)
		}
		out = append(out, rec)
	}
	return out
}

func TestNestedSpansShareTraceAndLink(t *testing.T) {
	logger, buf := capture()
	ctx := WithLogger(context.Background(), logger)

	ctx, parent := StartSpan(ctx, "http.command")
	_, child := StartSpan(ctx, "simulation.apply")
	child.End()
	parent.End()

	recs := records(t, buf)
	if len(recs) != 2 {
		t.Fatalf("got %d span records, want 2", len(recs))
	}

	childRec, parentRec := recs[0], recs[1]
	if childRec["span"] != "simulation.apply" || parentRec["span"] != "http.command" {
		t.Fatalf("unexpected span order: %v", recs)
	}
	if childRec["trace_id"] != parentRec["trace_id"] {
		t.Errorf("child trace_id %v != parent %v", childRec["trace_id"], parentRec["trace_id"])
	}
	if childRec["parent_span_id"] != parentRec["span_id"] {
		t.Errorf("child parent_span_id %v != parent span_id %v", childRec["parent_span_id"], parentRec["span_id"])
	}
	if _, ok := parentRec["parent_span_id"]; ok {
		t.Error("root span should have no parent_span_id")
	}
	if _, ok := childRec["duration_ms"].(float64); !ok {
		t.Errorf("missing duration_ms: %v", childRec)
	}
}

// a trace has to survive being handed to another goroutine, which is how an
// http command reaches the simulation actor.
func TestSpanContextCarriesTraceAcrossContexts(t *testing.T) {
	logger, buf := capture()
	ctx := WithLogger(context.Background(), logger)

	ctx, origin := StartSpan(ctx, "http.command")
	carried := origin.Context()
	origin.End()

	detached := WithSpanContext(WithLogger(context.Background(), logger), carried)
	_, continued := StartSpan(detached, "simulation.apply")
	continued.End()

	recs := records(t, buf)
	if recs[0]["trace_id"] != recs[1]["trace_id"] {
		t.Errorf("trace did not carry across contexts: %v vs %v", recs[0]["trace_id"], recs[1]["trace_id"])
	}
	if recs[1]["parent_span_id"] != recs[0]["span_id"] {
		t.Errorf("continued span is not a child of the origin: %v", recs[1])
	}
}

func TestStartSpanWithoutParentBeginsNewTrace(t *testing.T) {
	logger, buf := capture()
	ctx := WithLogger(context.Background(), logger)

	_, a := StartSpan(ctx, "one")
	a.End()
	_, b := StartSpan(ctx, "two")
	b.End()

	recs := records(t, buf)
	if recs[0]["trace_id"] == recs[1]["trace_id"] {
		t.Error("independent spans should not share a trace id")
	}
	if recs[0]["trace_id"] == "" {
		t.Error("empty trace id")
	}
}

func TestSpanAttrsAndError(t *testing.T) {
	logger, buf := capture()
	ctx := WithLogger(context.Background(), logger)

	_, span := StartSpan(ctx, "store.write", slog.String("simulation_id", "sim-1"))
	span.SetAttrs(slog.Int("events", 12))
	span.RecordError(errors.New("connection refused"))
	span.End()
	span.End() // second End must not emit a duplicate record

	recs := records(t, buf)
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	rec := recs[0]
	if rec["simulation_id"] != "sim-1" || rec["events"] != float64(12) {
		t.Errorf("attributes missing: %v", rec)
	}
	if rec["status"] != "error" || rec["error"] != "connection refused" {
		t.Errorf("error not recorded: %v", rec)
	}
}

func TestNilSpanIsSafe(t *testing.T) {
	var span *Span
	span.SetAttrs(slog.String("k", "v"))
	span.RecordError(errors.New("boom"))
	span.End()
	if span.TraceID() != "" || span.Context().Valid() {
		t.Error("nil span should report an empty context")
	}
}

func TestLoggerFromFallsBackToDefault(t *testing.T) {
	if LoggerFrom(context.Background()) != slog.Default() {
		t.Error("expected the default logger when the context carries none")
	}
}
