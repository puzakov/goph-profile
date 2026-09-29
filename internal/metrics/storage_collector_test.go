package metrics_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"goph-profile/internal/metrics"
)

// fakeQueryer — заглушка пула: считает обращения к БД и отдаёт заданный
// агрегат. Счётчик нужен, чтобы проверить кэш: пока снимок свежий,
// обращений к БД быть не должно.
type fakeQueryer struct {
	calls atomic.Int64
	bytes int64
	users int64
	err   error
}

func (f *fakeQueryer) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	f.calls.Add(1)
	return fakeRow{bytes: f.bytes, users: f.users, err: f.err}
}

// fakeRow отдаёт заранее известный агрегат в приёмники коллектора.
type fakeRow struct {
	bytes int64
	users int64
	err   error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != 2 {
		return fmt.Errorf("ожидалось два приёмника, получено %d", len(dest))
	}
	bytes, ok := dest[0].(*int64)
	if !ok {
		return fmt.Errorf("приёмник объёма: %T", dest[0])
	}
	users, ok := dest[1].(*int64)
	if !ok {
		return fmt.Errorf("приёмник числа пользователей: %T", dest[1])
	}
	*bytes, *users = r.bytes, r.users
	return nil
}

func TestStorageCollector_EmitsAggregate(t *testing.T) {
	db := &fakeQueryer{bytes: 2560, users: 2}

	values := gather(t, metrics.NewStorageCollector(db, metrics.StorageCacheTTL, testLogger()))

	// Метрики без метки пользователя: кардинальность не растёт с числом
	// пользователей.
	require.Equal(t, float64(2560), values["avatars_storage_bytes"])
	require.Equal(t, float64(2), values["avatars_users_with_avatars"])
	require.EqualValues(t, 1, db.calls.Load())
}

func TestStorageCollector_ServesSnapshotWithinTTL(t *testing.T) {
	db := &fakeQueryer{bytes: 1024, users: 1}
	collector := metrics.NewStorageCollector(db, metrics.StorageCacheTTL, testLogger())

	first := gather(t, collector)
	second := gather(t, collector)

	require.Equal(t, float64(1024), first["avatars_storage_bytes"])
	require.Equal(t, first, second)
	require.EqualValues(t, 1, db.calls.Load(), "второй scrape обошёлся без запроса к БД")
}

func TestStorageCollector_RefreshesAfterTTL(t *testing.T) {
	db := &fakeQueryer{bytes: 1024, users: 1}
	collector := metrics.NewStorageCollector(db, 10*time.Millisecond, testLogger())

	require.Equal(t, float64(1024), gather(t, collector)["avatars_storage_bytes"])

	db.bytes, db.users = 4096, 3
	time.Sleep(20 * time.Millisecond)

	values := gather(t, collector)
	require.Equal(t, float64(4096), values["avatars_storage_bytes"])
	require.Equal(t, float64(3), values["avatars_users_with_avatars"])
	require.EqualValues(t, 2, db.calls.Load())
}

func TestStorageCollector_QueryErrorEmitsNothing(t *testing.T) {
	db := &fakeQueryer{err: context.DeadlineExceeded}

	// Ошибка сбора не ломает scrape: метрик просто нет.
	values := gather(t, metrics.NewStorageCollector(db, metrics.StorageCacheTTL, testLogger()))

	require.Empty(t, values)
}

// Недоступная БД не должна обнулять метрику: отдаётся последний удачный снимок.
func TestStorageCollector_KeepsSnapshotOnError(t *testing.T) {
	db := &fakeQueryer{bytes: 2048, users: 2}
	collector := metrics.NewStorageCollector(db, 10*time.Millisecond, testLogger())
	require.Equal(t, float64(2048), gather(t, collector)["avatars_storage_bytes"])

	db.err = context.DeadlineExceeded
	time.Sleep(20 * time.Millisecond)

	require.Equal(t, float64(2048), gather(t, collector)["avatars_storage_bytes"])
	require.EqualValues(t, 2, db.calls.Load(), "снимок перечитан, но отдан прошлый")
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
