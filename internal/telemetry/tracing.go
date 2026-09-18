// Package telemetry содержит инициализацию OpenTelemetry-трейсинга,
// создание логгера и хелперы корреляции логов со спанами.
package telemetry

import (
	"context"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// ShutdownFunc останавливает провайдер трейсинга, сбрасывая буфер спанов.
type ShutdownFunc func(context.Context) error

// InitTracerProvider настраивает глобальные TracerProvider и propagator.
// endpoint — полный URL OTLP/HTTP-коллектора (например, http://jaeger:4318).
// Пустой endpoint выключает трейсинг: остаётся no-op провайдер, и сервис
// работает без коллектора — так запускаются unit- и интеграционные тесты.
func InitTracerProvider(ctx context.Context, endpoint, serviceName string) (ShutdownFunc, error) {
	// Propagator нужен и без экспортёра: он извлекает traceparent из
	// входящих запросов и сообщений, поэтому ставится всегда.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))

	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}

	exp, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(endpoint))
	if err != nil {
		return nil, fmt.Errorf("create otlp exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
		)),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// EndSpan завершает спан, отмечая ошибку, если она есть: ошибка попадёт
// в трейс как событие и переведёт спан в статус Error.
func EndSpan(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

// TraceIDAttr возвращает атрибут slog с идентификатором текущего трейса.
// Если спана в контексте нет, значение пустое — лог всё равно пишется.
func TraceIDAttr(ctx context.Context) slog.Attr {
	return slog.String("trace_id", traceID(ctx))
}

// SpanIDAttr возвращает атрибут slog с идентификатором текущего спана.
func SpanIDAttr(ctx context.Context) slog.Attr {
	return slog.String("span_id", spanID(ctx))
}

func traceID(ctx context.Context) string {
	sc := trace.SpanFromContext(ctx).SpanContext()
	if !sc.IsValid() {
		return ""
	}
	return sc.TraceID().String()
}

func spanID(ctx context.Context) string {
	sc := trace.SpanFromContext(ctx).SpanContext()
	if !sc.IsValid() {
		return ""
	}
	return sc.SpanID().String()
}
