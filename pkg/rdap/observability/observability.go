// Package observability wires slog + OpenTelemetry. Handlers receive a
// pre-configured *slog.Logger; the tracer is pulled via
// otel.Tracer("gordap") where it's actually needed, keeping import graphs
// shallow.
package observability

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

func NewLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}

// AccessLog wraps a handler and emits one structured record per request. Uses
// a minimal response writer wrapper so we can capture the status code without
// reaching for a third-party middleware library.
func AccessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rw, r)

			span := trace.SpanFromContext(r.Context())
			logger.LogAttrs(r.Context(), slog.LevelInfo, "http",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rw.status),
				slog.Duration("duration", time.Since(start)),
				slog.String("trace_id", span.SpanContext().TraceID().String()),
			)
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// StartSpan is a tiny helper so callers don't have to import otel directly.
func StartSpan(ctx context.Context, name string) (context.Context, trace.Span) {
	return otel.Tracer("gordap").Start(ctx, name)
}
