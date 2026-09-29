package handlers

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"goph-profile/internal/metrics"
	"goph-profile/internal/metricstest"
)

// newTestRouter собирает боевой роутер с заглушкой сервиса: статика берётся
// из временного каталога, метрики — из собственного реестра.
func newTestRouter(t *testing.T) (http.Handler, *prometheus.Registry) {
	t.Helper()

	registry := metrics.NewRegistry()
	appMetrics := metrics.New(registry)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	r := NewRouter(&fakeService{}, t.TempDir(), appMetrics, log)
	return r, registry
}

// /metrics регистрируется до FileServer на "/*": иначе статика перехватила
// бы путь и Prometheus получал бы 404.
func TestRouter_MetricsEndpoint(t *testing.T) {
	router, registry := newTestRouter(t)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	body, err := io.ReadAll(rec.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "go_goroutines")

	// Сам запрос тоже проходит через RequestLogger.
	require.Greater(t, metricstest.Values(t, registry)["avatars_http_requests_total_GET_/metrics_200"], float64(0))
}

func TestRouter_HealthAndNotFound(t *testing.T) {
	router, _ := newTestRouter(t)

	health := httptest.NewRecorder()
	router.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	require.Equal(t, http.StatusOK, health.Code)

	missing := httptest.NewRecorder()
	router.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/api/v1/nope", nil))
	require.Equal(t, http.StatusNotFound, missing.Code)
	require.False(t, strings.Contains(missing.Body.String(), "goroutine"),
		"внутренние детали не уходят клиенту")
}
