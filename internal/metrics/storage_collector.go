package metrics

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
)

// storageQueryTimeout — предел на запрос статистики при scrape.
const storageQueryTimeout = 5 * time.Second

// storageUsageQuery — суммарный размер живых аватарок по пользователям.
const storageUsageQuery = `SELECT user_id, COALESCE(SUM(size_bytes), 0)
	FROM avatars
	WHERE deleted_at IS NULL
	GROUP BY user_id`

// Queryer — часть интерфейса репозитория, достаточная коллектору.
// Реализуется *pgxpool.Pool (прод) и pgxmock (тесты).
type Queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// storageCollector считает объём данных в S3 запросом к БД в момент scrape.
// Расчёт по таблице, а не инкремент в сервисе: значение не дрейфует при
// мягком удалении, ретраях и повторной обработке событий.
type storageCollector struct {
	db        Queryer
	log       *slog.Logger
	bytesDesc *prometheus.Desc
}

// NewStorageCollector возвращает коллектор метрики avatars_storage_bytes.
func NewStorageCollector(db Queryer, log *slog.Logger) prometheus.Collector {
	return &storageCollector{
		db:  db,
		log: log,
		bytesDesc: prometheus.NewDesc(
			namespace+"_storage_bytes",
			"Суммарный размер живых аватарок пользователя в S3, байт.",
			[]string{"user_id"}, nil),
	}
}

// Describe отправляет описание метрики коллектора.
func (c *storageCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.bytesDesc
}

// Collect выполняет запрос и отдаёт по метрике на пользователя.
// Ошибка запроса не ломает scrape: она логируется, метрик просто нет.
func (c *storageCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), storageQueryTimeout)
	defer cancel()

	rows, err := c.db.Query(ctx, storageUsageQuery)
	if err != nil {
		c.log.Error("collect storage usage", "error", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var userID string
		var total int64
		if err := rows.Scan(&userID, &total); err != nil {
			c.log.Error("scan storage usage", "error", err)
			return
		}
		ch <- prometheus.MustNewConstMetric(c.bytesDesc, prometheus.GaugeValue,
			float64(total), userID)
	}
	if err := rows.Err(); err != nil {
		c.log.Error("read storage usage", "error", err)
	}
}
