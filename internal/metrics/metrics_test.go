package metrics_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"goph-profile/internal/metrics"
)

// histogramSamples — число наблюдений гистограмм по ключам вида
// «имя_значение-метки»: сами значения наблюдений между тестами не
// сбрасываются (реестр promauto глобальный), поэтому сравниваются дельты.
func histogramSamples(t *testing.T, collector prometheus.Collector) map[string]uint64 {
	t.Helper()
	reg := prometheus.NewRegistry()
	require.NoError(t, reg.Register(collector))
	families, err := reg.Gather()
	require.NoError(t, err)

	samples := map[string]uint64{}
	for _, f := range families {
		for _, m := range f.GetMetric() {
			key := f.GetName()
			for _, l := range m.GetLabel() {
				key += "_" + l.GetValue()
			}
			samples[key] = m.GetHistogram().GetSampleCount()
		}
	}
	return samples
}

func TestObserveHTTP_RecordsRedMetrics(t *testing.T) {
	const (
		method = http.MethodPost
		route  = "/api/v1/avatars"
		code   = "201"
	)
	// Метки в dto отсортированы по имени: method, route, status.
	durationKey := "avatars_http_request_duration_seconds_" + method + "_" + route + "_" + code

	counter := metrics.HTTPRequestsTotal.WithLabelValues(method, route, code)
	requestsBefore := testutil.ToFloat64(counter)
	observedBefore := histogramSamples(t, metrics.HTTPRequestDurationSeconds)[durationKey]

	metrics.ObserveHTTP(method, route, http.StatusCreated, 1500*time.Millisecond)

	require.Equal(t, requestsBefore+1, testutil.ToFloat64(counter))
	require.Equal(t, observedBefore+1, histogramSamples(t, metrics.HTTPRequestDurationSeconds)[durationKey])
}

func TestObserveUpload_RecordsBusinessMetrics(t *testing.T) {
	counter := metrics.UploadsTotal.WithLabelValues(metrics.StatusError)
	uploadsBefore := testutil.ToFloat64(counter)
	durationKey := "avatars_upload_duration_seconds_" + metrics.StatusError
	observedBefore := histogramSamples(t, metrics.UploadDurationSeconds)[durationKey]

	metrics.ObserveUpload(metrics.StatusError, 2*time.Second)

	require.Equal(t, uploadsBefore+1, testutil.ToFloat64(counter))
	require.Equal(t, observedBefore+1, histogramSamples(t, metrics.UploadDurationSeconds)[durationKey])
}

// Имена метрик должны совпадать с ТЗ дословно: по ним пишутся
// дашборды Grafana и правила алертинга.
func TestMetricNames_MatchSpec(t *testing.T) {
	metrics.ObserveUpload(metrics.StatusOK, time.Second)
	metrics.ObserveHTTP(http.MethodGet, "/api/v1/avatars/{avatarID}", http.StatusOK, time.Second)

	families, err := prometheus.DefaultGatherer.Gather()
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
