package telemetry_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"goph-profile/internal/telemetry"
)

// newTestSpan запускает спан с всегда включённым сэмплированием:
// только тогда SpanContext валиден и trace_id можно прочитать.
func newTestSpan(t *testing.T) (context.Context, trace.Span) {
	t.Helper()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return tp.Tracer("test").Start(context.Background(), "operation")
}

func TestTraceIDAttr_WithoutSpan(t *testing.T) {
	attr := telemetry.TraceIDAttr(context.Background())
	require.Equal(t, "trace_id", attr.Key)
	require.Equal(t, "", attr.Value.String(), "без спана — пустое значение")

	spanAttr := telemetry.SpanIDAttr(context.Background())
	require.Equal(t, "", spanAttr.Value.String())
}

func TestTraceIDAttr_WithSpan(t *testing.T) {
	ctx, span := newTestSpan(t)
	defer span.End()

	traceAttr := telemetry.TraceIDAttr(ctx)
	require.NotEmpty(t, traceAttr.Value.String())
	require.Equal(t, span.SpanContext().TraceID().String(), traceAttr.Value.String())

	spanAttr := telemetry.SpanIDAttr(ctx)
	require.Equal(t, span.SpanContext().SpanID().String(), spanAttr.Value.String())
}

func TestInitTracerProvider_EmptyEndpointIsNoop(t *testing.T) {
	shutdown, err := telemetry.InitTracerProvider(context.Background(), "", "test-service")
	require.NoError(t, err, "без коллектора сервис должен запускаться")
	require.NoError(t, shutdown(context.Background()))
}

// Propagator ставится и без экспортёра: именно он переносит traceparent
// между сервисами — в заголовках HTTP и сообщениях брокера.
func TestPropagatorInjectsAndExtractsTraceContext(t *testing.T) {
	_, err := telemetry.InitTracerProvider(context.Background(), "", "test-service")
	require.NoError(t, err)

	ctx, span := newTestSpan(t)
	defer span.End()

	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	require.Contains(t, carrier["traceparent"], span.SpanContext().TraceID().String())

	extracted := trace.SpanContextFromContext(
		otel.GetTextMapPropagator().Extract(context.Background(), carrier))
	require.True(t, extracted.IsValid())
	require.Equal(t, span.SpanContext().TraceID(), extracted.TraceID())
}

func TestEndSpan_RecordsError(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(recorder),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	_, span := tp.Tracer("test").Start(context.Background(), "upload_avatar")
	telemetry.EndSpan(span, errors.New("сбой загрузки"))

	ended := recorder.Ended()
	require.Len(t, ended, 1)
	require.Equal(t, codes.Error, ended[0].Status().Code)
	require.Equal(t, "сбой загрузки", ended[0].Status().Description)
	require.Len(t, ended[0].Events(), 1, "ошибка записана событием спана")
}

func TestEndSpan_WithoutErrorKeepsStatusUnset(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(recorder),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	_, span := tp.Tracer("test").Start(context.Background(), "upload_avatar")
	telemetry.EndSpan(span, nil)

	ended := recorder.Ended()
	require.Len(t, ended, 1)
	require.Equal(t, codes.Unset, ended[0].Status().Code)
}
