// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package obs

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	apitrace "go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// NewGRPCTracerProvider creates an OTEL SDK TracerProvider that exports spans
// to endpoint (e.g. "localhost:4317") via gRPC (unencrypted).
//
// Returns the provider and a shutdown function; caller must defer shutdown(ctx).
// On error, returns (nil, nil) and logs a warning — callers should check for nil
// and fall back to NoopTracer.
func NewGRPCTracerProvider(ctx context.Context, endpoint, serviceName, version, clusterName string) (*sdktrace.TracerProvider, func(context.Context)) {
	conn, err := grpc.NewClient(endpoint,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		fmt.Printf("⚠ tracing: grpc.NewClient(%q): %v — disabling OTEL export\n", endpoint, err)
		return nil, nil
	}

	exp, err := otlptracegrpc.New(ctx, otlptracegrpc.WithGRPCConn(conn))
	if err != nil {
		fmt.Printf("⚠ tracing: otlptracegrpc.New: %v — disabling OTEL export\n", err)
		return nil, nil
	}

	res := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceNameKey.String(serviceName),
		semconv.ServiceVersionKey.String(version),
		attribute.String("cluster", clusterName),
	)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)

	shutdown := func(ctx context.Context) {
		// Use a bounded context for shutdown so a hung collector doesn't
		// delay process exit indefinitely.
		shutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_ = tp.Shutdown(shutCtx)
	}
	return tp, shutdown
}

// OTELTracer adapts the OTEL SDK tracer to the obs.Tracer interface.
type OTELTracer struct{ t apitrace.Tracer }

// NewOTELTracer wraps tp's tracer for name and returns it as a Tracer.
func NewOTELTracer(tp *sdktrace.TracerProvider, name string) Tracer {
	return OTELTracer{t: tp.Tracer(name)}
}

// Start opens a new OTEL span as a child of ctx. obs.Field values (slog.Attr)
// are converted to OTEL string attributes.
func (o OTELTracer) Start(ctx context.Context, name string, attrs ...Field) (context.Context, Span) {
	otelAttrs := make([]attribute.KeyValue, 0, len(attrs))
	for _, a := range attrs {
		otelAttrs = append(otelAttrs, attribute.String(a.Key, a.Value.String()))
	}
	ctx, span := o.t.Start(ctx, name, apitrace.WithAttributes(otelAttrs...))
	return ctx, OTELSpan{span}
}

// OTELSpan wraps an OTEL SDK Span to implement obs.Span.
type OTELSpan struct{ s apitrace.Span }

// End marks the span as complete.
func (o OTELSpan) End() { o.s.End() }

// SetErr records err on the span and sets the span status to Error.
func (o OTELSpan) SetErr(err error) {
	if err == nil {
		return
	}
	o.s.RecordError(err)
	o.s.SetStatus(otelcodes.Error, err.Error())
}

// Set attaches additional fields to the span as OTEL string attributes.
func (o OTELSpan) Set(attrs ...Field) {
	otelAttrs := make([]attribute.KeyValue, 0, len(attrs))
	for _, a := range attrs {
		otelAttrs = append(otelAttrs, attribute.String(a.Key, a.Value.String()))
	}
	o.s.SetAttributes(otelAttrs...)
}
