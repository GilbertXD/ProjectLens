package telemetry

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdk "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

type localExporter struct{ log *slog.Logger }

func (e localExporter) ExportSpans(ctx context.Context, spans []sdk.ReadOnlySpan) error {
	for _, s := range spans {
		e.log.InfoContext(ctx, "trace span", "name", s.Name(), "trace_id", s.SpanContext().TraceID().String(), "span_id", s.SpanContext().SpanID().String(), "parent_id", s.Parent().SpanID().String(), "duration_ms", s.EndTime().Sub(s.StartTime()).Milliseconds())
	}
	return nil
}
func (localExporter) Shutdown(context.Context) error { return nil }
func StartTracing(enabled bool, log *slog.Logger) func() {
	if !enabled {
		return func() {}
	}
	p := sdk.NewTracerProvider(sdk.WithBatcher(localExporter{log}))
	otel.SetTracerProvider(p)
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = p.Shutdown(ctx)
	}
}
func Span(ctx context.Context, name string) (context.Context, trace.Span) {
	return otel.Tracer("projectlens").Start(ctx, name)
}
func TraceParent(ctx context.Context) string {
	m := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, m)
	return m.Get("traceparent")
}
func Restore(ctx context.Context, parent string) context.Context {
	return propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{"traceparent": parent})
}
func TraceID(ctx context.Context) string {
	s := trace.SpanContextFromContext(ctx)
	if !s.IsValid() {
		return ""
	}
	return s.TraceID().String()
}

// Avoid emitting root spans for idle worker polling and metrics scrapes.
type QueryTracer struct{}
type querySpan struct{}

func (QueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	if !trace.SpanFromContext(ctx).IsRecording() {
		return ctx
	}
	ctx, s := Span(ctx, "postgres.query")
	return context.WithValue(ctx, querySpan{}, s)
}
func (QueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if s, ok := ctx.Value(querySpan{}).(trace.Span); ok {
		s.End()
	}
}
