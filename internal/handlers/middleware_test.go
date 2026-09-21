package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"goph-profile/internal/metrics"
	"goph-profile/internal/metricstest"
	"goph-profile/internal/telemetry"
)

// traceparent из примера W3C: трейс 4bf92f3577b34da6a3ce929d0e0e4736
// продолжается спанами сервиса, поэтому его id должен попасть в логи.
const (
	testTraceID     = "4bf92f3577b34da6a3ce929d0e0e4736"
	testTraceparent = "00-" + testTraceID + "-00f067aa0ba902b7-01"

	testRoute = "/api/v1/avatars/{avatarID}"
)

// newTracedRouter собирает роутер с той же цепочкой middleware, что и
// боевой, и возвращает буфер логов, записанные спаны и реестр метрик.
func newTracedRouter(t *testing.T) (http.Handler, *bytes.Buffer, *tracetest.SpanRecorder, *prometheus.Registry) {
	t.Helper()

	// Нужен работающий провайдер: с no-op трейсером SpanContext невалиден
	// и trace_id в логе был бы пустым.
	prev := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(recorder),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
		_ = tp.Shutdown(context.Background())
	})

	// Propagator ставится и без коллектора — именно он читает traceparent.
	_, err := telemetry.InitTracerProvider(context.Background(), "", "test-service")
	require.NoError(t, err)

	logBuf := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(logBuf, nil))

	// Реестр создаётся на тест: метрики изолированы, значения точные.
	registry := metrics.NewRegistry()
	appMetrics := metrics.New(registry)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(Tracing())
	r.Use(RequestLogger(log, appMetrics))
	r.Get(testRoute, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return r, logBuf, recorder, registry
}

func TestRequestLogger_PutsTraceIDFromHeaderInLog(t *testing.T) {
	router, logBuf, _, _ := newTracedRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/avatars/42", nil)
	req.Header.Set("traceparent", testTraceparent)
	router.ServeHTTP(httptest.NewRecorder(), req)

	var entry map[string]any
	require.NoError(t, json.Unmarshal(logBuf.Bytes(), &entry), "лог пишется в JSON")

	require.Equal(t, testTraceID, entry["trace_id"], "трейс продолжен, а не начат заново")
	require.NotEmpty(t, entry["span_id"])
	require.NotEmpty(t, entry["request_id"])
	require.Equal(t, http.MethodGet, entry["method"])
	require.Equal(t, float64(http.StatusOK), entry["status"])
	require.Equal(t, float64(0), entry["bytes"])
}

// Шаблон маршрута chi не попадает в http.Request.Pattern, поэтому otelhttp
// не может вывести http.route сам — его проставляет RequestLogger.
func TestRequestLogger_SetsRouteOnSpan(t *testing.T) {
	router, _, recorder, _ := newTracedRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/avatars/42", nil)
	router.ServeHTTP(httptest.NewRecorder(), req)

	ended := recorder.Ended()
	require.Len(t, ended, 1)
	// Имя спана — метод и шаблон маршрута, а не путь с идентификатором.
	require.Equal(t, "GET "+testRoute, ended[0].Name())

	attrs := map[string]string{}
	for _, kv := range ended[0].Attributes() {
		attrs[string(kv.Key)] = kv.Value.AsString()
	}
	require.Equal(t, testRoute, attrs["http.route"])
}

func TestRequestLogger_RecordsRedMetrics(t *testing.T) {
	router, _, _, registry := newTracedRouter(t)

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/avatars/42", nil))

	// Реестр свой у каждого теста: сравнивать дельты не нужно.
	require.Equal(t, float64(1),
		metricstest.Values(t, registry)["avatars_http_requests_total_GET_"+testRoute+"_200"])
	require.Equal(t, uint64(1),
		metricstest.HistogramSamples(t, registry)["avatars_http_request_duration_seconds_GET_"+testRoute+"_200"])
}

// Незаматченный путь не должен раздувать кардинальность метки route.
func TestRequestLogger_UnmatchedPathUsesPlaceholder(t *testing.T) {
	router, logBuf, _, _ := newTracedRouter(t)

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/nope", nil))

	var entry map[string]any
	require.NoError(t, json.Unmarshal(logBuf.Bytes(), &entry))
	require.Equal(t, unmatchedRoute, entry["route"])
}
