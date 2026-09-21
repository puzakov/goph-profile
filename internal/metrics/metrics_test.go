package metrics_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"goph-profile/internal/metrics"
	"goph-profile/internal/metricstest"
)

// newMetrics отдаёт метрики вместе с реестром, в который они записаны:
// тесты читают значения из него напрямую.
func newMetrics() (*metrics.Metrics, *prometheus.Registry) {
	registry := metrics.NewRegistry()
	return metrics.New(registry), registry
}

func TestObserveHTTP_RecordsRedMetrics(t *testing.T) {
	m, registry := newMetrics()

	m.ObserveHTTP(http.MethodPost, "/api/v1/avatars", http.StatusCreated, 1500*time.Millisecond)

	// Метки в dto отсортированы по имени: method, route, status.
	require.Equal(t, float64(1), metricstest.Values(t, registry)["avatars_http_requests_total_POST_/api/v1/avatars_201"])
	require.Equal(t, uint64(1),
		metricstest.HistogramSamples(t, registry)["avatars_http_request_duration_seconds_POST_/api/v1/avatars_201"])
}

func TestObserveUpload_RecordsBusinessMetrics(t *testing.T) {
	m, registry := newMetrics()

	m.ObserveUpload(metrics.StatusError, 2*time.Second)
	m.ObserveUpload(metrics.StatusOK, time.Second)
	m.ObserveUpload(metrics.StatusOK, time.Second)

	values := metricstest.Values(t, registry)
	samples := metricstest.HistogramSamples(t, registry)
	require.Equal(t, float64(1), values["avatars_uploads_total_error"])
	require.Equal(t, float64(2), values["avatars_uploads_total_ok"])
	require.Equal(t, uint64(1), samples["avatars_upload_duration_seconds_error"])
	require.Equal(t, uint64(2), samples["avatars_upload_duration_seconds_ok"])
}

// Реестр внедряется снаружи: наборы метрик в тестах не пересекаются,
// поэтому проверяются точные значения, а не дельты.
func TestMetrics_RegistriesAreIsolated(t *testing.T) {
	first, firstRegistry := newMetrics()
	_, secondRegistry := newMetrics()

	first.ObserveUpload(metrics.StatusOK, time.Second)

	require.Equal(t, float64(1), metricstest.Values(t, firstRegistry)["avatars_uploads_total_ok"])
	require.NotContains(t, metricstest.Values(t, secondRegistry), "avatars_uploads_total_ok")
}

// Повторная регистрация тех же метрик в одном реестре — ошибка программиста,
// а не данные: она видна сразу на старте.
func TestNew_DuplicateMetricsPanics(t *testing.T) {
	registry := metrics.NewRegistry()
	metrics.New(registry)

	require.Panics(t, func() { metrics.New(registry) })
}

// Имена метрик должны совпадать с ТЗ дословно: по ним пишутся
// дашборды Grafana и правила алертинга.
func TestMetricNames_MatchSpec(t *testing.T) {
	m, registry := newMetrics()
	m.ObserveUpload(metrics.StatusOK, time.Second)
	m.ObserveHTTP(http.MethodGet, "/api/v1/avatars/{avatarID}", http.StatusOK, time.Second)

	families, err := registry.Gather()
	require.NoError(t, err)

	names := make([]string, 0, len(families))
	for _, f := range families {
		names = append(names, f.GetName())
	}
	require.Contains(t, names, "avatars_uploads_total")
	require.Contains(t, names, "avatars_upload_duration_seconds")
	require.Contains(t, names, "avatars_http_requests_total")
	require.Contains(t, names, "avatars_http_request_duration_seconds")
}

// /metrics отдаёт тот же реестр, куда пишут метрики, и стандартные метрики
// процесса: на них построена панель «Ресурсы» в Grafana.
func TestMetrics_HandlerExposesRegistry(t *testing.T) {
	m, _ := newMetrics()
	m.ObserveUpload(metrics.StatusOK, time.Second)

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	body, err := io.ReadAll(rec.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "avatars_uploads_total{status=\"ok\"} 1")
	require.Contains(t, string(body), "go_goroutines")
	require.Contains(t, string(body), "process_cpu_seconds_total")
	// Коллекторы пула БД и хранилища регистрирует main, а не конструктор метрик.
	require.False(t, strings.Contains(string(body), "avatars_db_total_conns"))
}
