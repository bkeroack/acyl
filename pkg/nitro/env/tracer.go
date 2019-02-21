package env

import (
	"context"

	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"
)

const spanContextKey = "span_key"

type acylContextKey string

// newSpanContext returns a context with the span embedded as a value
func newSpanContext(ctx context.Context, span tracer.Span) context.Context {
	return context.WithValue(ctx, acylContextKey(spanContextKey), span)
}

// getSpanFromContext returns the span (or nil) and a boolean indicating whether it exists
func getSpanFromContext(ctx context.Context) (tracer.Span, bool) {
	span, ok := ctx.Value(acylContextKey(spanContextKey)).(tracer.Span)
	return span, ok
}
