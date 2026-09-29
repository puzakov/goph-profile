package metrics_test

import (
	"io"
	"log/slog"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"goph-profile/internal/metricstest"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// gather собирает метрики отдельных коллекторов: они регистрируются
// во временном реестре, поэтому тесты не видят метрик друг друга.
func gather(t *testing.T, collectors ...prometheus.Collector) map[string]float64 {
	t.Helper()
	reg := prometheus.NewRegistry()
	for _, c := range collectors {
		require.NoError(t, reg.Register(c))
	}
	return metricstest.Values(t, reg)
}
