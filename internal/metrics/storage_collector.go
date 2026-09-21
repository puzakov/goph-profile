package metrics

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
)

// StorageCacheTTL — как долго коллектор отдаёт снимок агрегата, не заглядывая
// в БД. Prometheus опрашивает /metrics каждые 15 секунд, а агрегат считается
// по всей таблице аватарок: без кэша это был бы запрос на каждый scrape.
const StorageCacheTTL = time.Minute

// storageQueryTimeout — предел на запрос агрегата.
const storageQueryTimeout = 5 * time.Second

// storageUsageQuery — объём данных и число пользователей одним запросом.
// Агрегат без группировки: разбивка по пользователям дала бы метрику
// с неограниченной кардинальностью метки user_id.
const storageUsageQuery = `SELECT COALESCE(SUM(size_bytes), 0), COUNT(DISTINCT user_id)
	FROM avatars
	WHERE deleted_at IS NULL`

// Queryer — часть интерфейса пула, достаточная коллектору.
// Реализуется *pgxpool.Pool (прод) и заглушками в тестах.
type Queryer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// storageUsage — снимок агрегата из БД.
type storageUsage struct {
	bytes int64
	users int64
}

// storageCollector отдаёт объём данных в S3 и число пользователей с живыми
// аватарками. Значения считаются по таблице, а не инкрементом в сервисе:
// так они не дрейфуют при мягком удалении, ретраях и повторной обработке.
type storageCollector struct {
	db  Queryer
	ttl time.Duration
	log *slog.Logger

	// mu защищает снимок: Collect может вызываться из нескольких
	// горутин (экспортёры Prometheus, тесты).
	mu       sync.Mutex
	snapshot *storageUsage
	fetched  time.Time

	bytesDesc *prometheus.Desc
	usersDesc *prometheus.Desc
}

// NewStorageCollector возвращает коллектор метрик объёма хранилища.
// ttl — срок жизни снимка, обычно StorageCacheTTL.
func NewStorageCollector(db Queryer, ttl time.Duration, log *slog.Logger) prometheus.Collector {
	return &storageCollector{
		db:  db,
		ttl: ttl,
		log: log,
		bytesDesc: prometheus.NewDesc(
			namespace+"_storage_bytes",
			"Суммарный размер живых аватарок в S3, байт.",
			nil, nil),
		usersDesc: prometheus.NewDesc(
			namespace+"_users_with_avatars",
			"Число пользователей с живыми аватарками.",
			nil, nil),
	}
}

// Describe отправляет описания метрик коллектора.
func (c *storageCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.bytesDesc
	ch <- c.usersDesc
}

// Collect отдаёт метрики по снимку. Пока снимка нет (первый запрос упал),
// метрик нет: нулевое значение выглядело бы как «хранилище пустое».
func (c *storageCollector) Collect(ch chan<- prometheus.Metric) {
	usage, err := c.usage()
	if err != nil {
		c.log.Error("collect storage usage", "error", err)
	}
	if usage == nil {
		return
	}
	ch <- prometheus.MustNewConstMetric(c.bytesDesc, prometheus.GaugeValue, float64(usage.bytes))
	ch <- prometheus.MustNewConstMetric(c.usersDesc, prometheus.GaugeValue, float64(usage.users))
}

// usage возвращает снимок агрегата: из кэша, если он ещё свежий, иначе
// перечитанный из БД. При ошибке чтения отдаётся последний удачный снимок —
// метрика не пропадает из-за недоступной БД.
func (c *storageCollector) usage() (*storageUsage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.snapshot != nil && time.Since(c.fetched) < c.ttl {
		return c.snapshot, nil
	}

	usage, err := c.query()
	if err != nil {
		return c.snapshot, err
	}
	c.snapshot, c.fetched = usage, time.Now()
	return usage, nil
}

// query читает агрегат из БД.
func (c *storageCollector) query() (*storageUsage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), storageQueryTimeout)
	defer cancel()

	var usage storageUsage
	if err := c.db.QueryRow(ctx, storageUsageQuery).Scan(&usage.bytes, &usage.users); err != nil {
		return nil, fmt.Errorf("query storage usage: %w", err)
	}
	return &usage, nil
}
