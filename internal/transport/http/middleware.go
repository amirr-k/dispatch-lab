package http

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"dispatchlab/internal/telemetry"
)

// statusRecorder captures the status code so the access log can report it.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// withTelemetry starts a span for every request and puts it on the request
// context, so anything the handler does — including a command handed to a
// simulation goroutine — becomes part of the same trace. The trace id also
// goes back on the response, which is what makes a report of "this request
// misbehaved" actionable.
func withTelemetry(next http.Handler, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// the stream is long-lived and gets its own per-event spans; wrapping
		// it here would produce one span lasting the whole connection.
		if isStreamRequest(r) {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		ctx, span := telemetry.StartSpan(telemetry.WithLogger(r.Context(), logger), "http.request",
			slog.String("method", r.Method), slog.String("path", r.URL.Path))
		defer span.End()

		w.Header().Set("X-Trace-Id", span.TraceID())
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r.WithContext(ctx))

		span.SetAttrs(slog.Int("status", recorder.status))
		logger.Info("handled request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"duration_ms", telemetry.DurationMs(time.Since(start)),
			"trace_id", span.TraceID())
	})
}

func isStreamRequest(r *http.Request) bool {
	return strings.HasSuffix(r.URL.Path, "/stream")
}
