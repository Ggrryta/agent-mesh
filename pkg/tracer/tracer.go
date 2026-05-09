package tracer

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

var tp *sdktrace.TracerProvider

// Init 初始化 OpenTelemetry TracerProvider
// endpoint: OTLP gRPC 地址，如 "localhost:4317"；为空则使用 NoopTracerProvider
// sampleRate: 采样率 0.0–1.0；0 或 1 使用 AlwaysSample，其余使用 TraceIDRatioBased
func Init(ctx context.Context, serviceName, endpoint string, sampleRate float64) (func(context.Context) error, error) {
	if endpoint == "" {
		otel.SetTracerProvider(trace.NewNoopTracerProvider())
		otel.SetTextMapPropagator(propagation.TraceContext{})
		return func(context.Context) error { return nil }, nil
	}

	exp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("create otlp exporter failed: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(serviceName)),
	)
	if err != nil {
		return nil, fmt.Errorf("create otel resource failed: %w", err)
	}

	tp = sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(buildSampler(sampleRate)),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	return func(ctx context.Context) error {
		shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return tp.Shutdown(shutdownCtx)
	}, nil
}

// Tracer 返回指定名称的 Tracer
func Tracer(name string) trace.Tracer {
	return otel.Tracer(name)
}

func buildSampler(rate float64) sdktrace.Sampler {
	if rate > 0 && rate < 1.0 {
		return sdktrace.TraceIDRatioBased(rate)
	}
	return sdktrace.AlwaysSample()
}
