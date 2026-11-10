package observability

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/shtmthw/ponnoKhoj/internal/version"
)

// TracerName is the instrumentation scope for ponnoKhoj's own spans.
const TracerName = "github.com/shtmthw/ponnoKhoj"

// SetupTracing installs the global tracer provider. When enabled is false it
// installs a no-op provider and exports nothing. The OTLP/HTTP exporter is
// configured by the standard OTEL_EXPORTER_OTLP_* environment variables.
// The returned function flushes and shuts the provider down.
func SetupTracing(ctx context.Context, enabled bool, service string) (func(context.Context) error, error) {
	if !enabled {
		otel.SetTracerProvider(noop.NewTracerProvider())
		return func(context.Context) error { return nil }, nil
	}
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("observability: otlp exporter: %w", err)
	}
	res := resource.NewSchemaless(
		attribute.String("service.name", service),
		attribute.String("service.version", version.Version),
	)
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// Tracer returns ponnoKhoj's tracer from the global provider.
func Tracer() trace.Tracer { return otel.Tracer(TracerName) }
