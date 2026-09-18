package metrics_test

import (
	"context"
	"io"
	"log/slog"
	"regexp"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"goph-profile/internal/metrics"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// gather собирает метрики коллекторов в отдельном реестре: глобальный реестр
// между тестами не разделяется, локальный даёт изолированные значения.
// Ключ — имя метрики с значениями меток через подчёркивание.
func gather(t *testing.T, collectors ...prometheus.Collector) map[string]float64 {
	t.Helper()
	reg := prometheus.NewRegistry()
	for _, c := range collectors {
		require.NoError(t, reg.Register(c))
	}
	families, err := reg.Gather()
	require.NoError(t, err)

	values := map[string]float64{}
	for _, f := range families {
		for _, m := range f.GetMetric() {
			key := f.GetName()
			for _, l := range m.GetLabel() {
				key += "_" + l.GetValue()
			}
			values[key] = metricValue(m)
		}
	}
	return values
}

// metricValue читает значение метрики любого типа.
func metricValue(m *dto.Metric) float64 {
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

func TestStorageCollector_EmitsBytesPerUser(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	t.Cleanup(mock.Close)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT user_id, COALESCE(SUM(size_bytes), 0)")).
		WillReturnRows(pgxmock.NewRows([]string{"user_id", "sum"}).
			AddRow("user-1", int64(2048)).
			AddRow("user-2", int64(512)))

	values := gather(t, metrics.NewStorageCollector(mock, testLogger()))

	require.Equal(t, float64(2048), values["avatars_storage_bytes_user-1"])
	require.Equal(t, float64(512), values["avatars_storage_bytes_user-2"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestStorageCollector_QueryErrorEmitsNothing(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	t.Cleanup(mock.Close)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT user_id, COALESCE(SUM(size_bytes), 0)")).
		WillReturnError(context.DeadlineExceeded)

	// Ошибка сбора не ломает scrape: метрик просто нет.
	values := gather(t, metrics.NewStorageCollector(mock, testLogger()))

	require.Empty(t, values)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPGXPoolCollector_ReportsPoolStatistics(t *testing.T) {
	cfg, err := pgxpool.ParseConfig("postgres://user:pass@127.0.0.1:1/db?sslmode=disable&pool_max_conns=7")
	require.NoError(t, err)
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	values := gather(t, metrics.NewPGXPoolCollector(pool))

	// Значения берутся из pgxpool.Stat() в момент сбора.
	require.Equal(t, float64(7), values["avatars_db_max_conns"])
	require.Equal(t, float64(0), values["avatars_db_total_conns"])
	require.Contains(t, values, "avatars_db_acquired_conns")
	require.Contains(t, values, "avatars_db_idle_conns")
	require.Contains(t, values, "avatars_db_acquire_duration_seconds_total")
}
