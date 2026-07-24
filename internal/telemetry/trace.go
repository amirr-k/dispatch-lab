package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"strings"
	"time"
)

// NewLogger builds the process logger. JSON keeps log records machine-
// readable in a container; text is far easier to read during local
// development.
func NewLogger(format string, level slog.Level) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if strings.EqualFold(format, "text") {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(handler)
}

// SpanContext identifies one span and the trace it belongs to. It is small
// and copyable so it can ride along with a command into the simulation
// goroutine, which is where the HTTP request's trace continues.
type SpanContext struct {
	TraceID string
	SpanID  string
}

// Valid reports whether this context refers to a real trace.
func (sc SpanContext) Valid() bool { return sc.TraceID != "" }

type spanKey struct{}

type loggerKey struct{}

// WithLogger stores a logger on the context for downstream components.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, logger)
}

// LoggerFrom returns the context's logger, falling back to the default one.
func LoggerFrom(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok && logger != nil {
		return logger
	}
	return slog.Default()
}

// WithSpanContext continues an existing trace in a new context. Used when a
// trace crosses a goroutine boundary and there is no parent span object to
// carry, as with a command handed to the simulation actor.
func WithSpanContext(ctx context.Context, sc SpanContext) context.Context {
	if !sc.Valid() {
		return ctx
	}
	return context.WithValue(ctx, spanKey{}, sc)
}

// SpanContextFrom returns the span context active on ctx, if any.
func SpanContextFrom(ctx context.Context) SpanContext {
	sc, _ := ctx.Value(spanKey{}).(SpanContext)
	return sc
}

// Span is one timed operation in a trace. Spans are emitted as structured log
// records rather than sent to a collector: this is a single binary, and the
// field names match OpenTelemetry's so a collector can be introduced later
// without changing any call site.
type Span struct {
	logger   *slog.Logger
	name     string
	traceID  string
	spanID   string
	parentID string
	start    time.Time
	attrs    []slog.Attr
	ended    bool
}

// StartSpan begins a span, continuing whatever trace ctx already carries and
// starting a new one otherwise. The returned context carries the new span, so
// anything started under it becomes a child.
func StartSpan(ctx context.Context, name string, attrs ...slog.Attr) (context.Context, *Span) {
	parent := SpanContextFrom(ctx)
	span := &Span{
		logger:   LoggerFrom(ctx),
		name:     name,
		traceID:  parent.TraceID,
		spanID:   randomID(8),
		parentID: parent.SpanID,
		start:    time.Now(),
		attrs:    attrs,
	}
	if span.traceID == "" {
		span.traceID = randomID(16)
	}
	ctx = WithSpanContext(ctx, SpanContext{TraceID: span.traceID, SpanID: span.spanID})
	return ctx, span
}

// Context returns this span's identifiers, for propagation to another
// goroutine.
func (s *Span) Context() SpanContext {
	if s == nil {
		return SpanContext{}
	}
	return SpanContext{TraceID: s.traceID, SpanID: s.spanID}
}

// TraceID is the id shared by every span in this trace.
func (s *Span) TraceID() string {
	if s == nil {
		return ""
	}
	return s.traceID
}

// SetAttrs adds attributes recorded when the span ends.
func (s *Span) SetAttrs(attrs ...slog.Attr) {
	if s == nil {
		return
	}
	s.attrs = append(s.attrs, attrs...)
}

// RecordError marks the span failed and attaches the error.
func (s *Span) RecordError(err error) {
	if s == nil || err == nil {
		return
	}
	s.attrs = append(s.attrs, slog.String("error", err.Error()), slog.String("status", "error"))
}

// End closes the span and emits it. Calling End twice is a no-op, so it is
// safe to both defer End and end a span early on an error path.
func (s *Span) End() {
	if s == nil || s.ended {
		return
	}
	s.ended = true

	attrs := make([]slog.Attr, 0, len(s.attrs)+5)
	attrs = append(attrs,
		slog.String("span", s.name),
		slog.String("trace_id", s.traceID),
		slog.String("span_id", s.spanID),
		slog.Float64("duration_ms", DurationMs(time.Since(s.start))),
	)
	if s.parentID != "" {
		attrs = append(attrs, slog.String("parent_span_id", s.parentID))
	}
	attrs = append(attrs, s.attrs...)

	s.logger.LogAttrs(context.Background(), slog.LevelInfo, "span", attrs...)
}

// DurationMs converts a duration to fractional milliseconds, the unit every
// latency metric and span in this project reports.
func DurationMs(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000.0
}

func randomID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}
