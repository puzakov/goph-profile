// Package metricstest содержит помощники для проверки метрик в тестах.
//
// Реестр метрик внедряется в приложение снаружи, поэтому тесты читают из
// своего реестра точные значения по имени метрики и меткам — без сравнения
// дельт и без общего состояния между тестами.
package metricstest

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// Values — значения метрик реестра.
// Ключ: имя метрики, затем значения меток через подчёркивание в порядке
// объявления, например "avatars_http_requests_total_GET_/health_200".
func Values(t testing.TB, reg *prometheus.Registry) map[string]float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("сбор метрик: %v", err)
	}

	values := map[string]float64{}
	for _, f := range families {
		for _, m := range f.GetMetric() {
			values[key(f.GetName(), m)] = value(m)
		}
	}
	return values
}

// HistogramSamples — число наблюдений гистограмм по тем же ключам, что и
// Values: сумма наблюдений в map[string]float64 не помещается.
func HistogramSamples(t testing.TB, reg *prometheus.Registry) map[string]uint64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("сбор метрик: %v", err)
	}

	samples := map[string]uint64{}
	for _, f := range families {
		for _, m := range f.GetMetric() {
			if m.GetHistogram() == nil {
				continue
			}
			samples[key(f.GetName(), m)] = m.GetHistogram().GetSampleCount()
		}
	}
	return samples
}

// key собирает ключ метрики: имя и значения меток через подчёркивание.
func key(name string, m *dto.Metric) string {
	for _, l := range m.GetLabel() {
		name += "_" + l.GetValue()
	}
	return name
}

// value читает значение метрики любого типа.
func value(m *dto.Metric) float64 {
	switch {
	case m.GetGauge() != nil:
		return m.GetGauge().GetValue()
	case m.GetCounter() != nil:
		return m.GetCounter().GetValue()
	case m.GetUntyped() != nil:
		return m.GetUntyped().GetValue()
	default:
		return 0
	}
}
