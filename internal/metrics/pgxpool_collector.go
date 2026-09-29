package metrics

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// pgxPoolCollector отдаёт статистику пула соединений PostgreSQL.
// Значения читаются из pgxpool.Stat() в момент сбора (pull-модель):
// фоновые горутины и собственное состояние не нужны.
type pgxPoolCollector struct {
	pool *pgxpool.Pool

	acquiredConns     *prometheus.Desc
	idleConns         *prometheus.Desc
	constructingConns *prometheus.Desc
	totalConns        *prometheus.Desc
	maxConns          *prometheus.Desc

	acquireCount         *prometheus.Desc
	emptyAcquireCount    *prometheus.Desc
	canceledAcquireCount *prometheus.Desc
	acquireDuration      *prometheus.Desc
}

// NewPGXPoolCollector возвращает коллектор метрик пула PostgreSQL.
func NewPGXPoolCollector(pool *pgxpool.Pool) prometheus.Collector {
	conns := func(name, help string) *prometheus.Desc {
		return prometheus.NewDesc(namespace+"_db_"+name, help, nil, nil)
	}
	return &pgxPoolCollector{
		pool: pool,

		acquiredConns:     conns("acquired_conns", "Число занятых соединений с БД."),
		idleConns:         conns("idle_conns", "Число свободных соединений с БД."),
		constructingConns: conns("constructing_conns", "Число соединений с БД в процессе установки."),
		totalConns:        conns("total_conns", "Общее число соединений с БД в пуле."),
		maxConns:          conns("max_conns", "Максимальный размер пула соединений с БД."),

		acquireCount:         conns("acquire_count_total", "Число успешных захватов соединения из пула."),
		emptyAcquireCount:    conns("empty_acquire_count_total", "Число захватов, которым не хватило свободного соединения."),
		canceledAcquireCount: conns("canceled_acquire_count_total", "Число отменённых ожиданий соединения из пула."),
		acquireDuration:      conns("acquire_duration_seconds_total", "Суммарное время ожидания соединения из пула, сек."),
	}
}

// Describe отправляет описания всех метрик коллектора.
func (c *pgxPoolCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.acquiredConns
	ch <- c.idleConns
	ch <- c.constructingConns
	ch <- c.totalConns
	ch <- c.maxConns
	ch <- c.acquireCount
	ch <- c.emptyAcquireCount
	ch <- c.canceledAcquireCount
	ch <- c.acquireDuration
}

// Collect читает текущую статистику пула и отдаёт её метриками.
func (c *pgxPoolCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.pool.Stat()
	ch <- prometheus.MustNewConstMetric(c.acquiredConns, prometheus.GaugeValue, float64(s.AcquiredConns()))
	ch <- prometheus.MustNewConstMetric(c.idleConns, prometheus.GaugeValue, float64(s.IdleConns()))
	ch <- prometheus.MustNewConstMetric(c.constructingConns, prometheus.GaugeValue, float64(s.ConstructingConns()))
	ch <- prometheus.MustNewConstMetric(c.totalConns, prometheus.GaugeValue, float64(s.TotalConns()))
	ch <- prometheus.MustNewConstMetric(c.maxConns, prometheus.GaugeValue, float64(s.MaxConns()))
	ch <- prometheus.MustNewConstMetric(c.acquireCount, prometheus.CounterValue, float64(s.AcquireCount()))
	ch <- prometheus.MustNewConstMetric(c.emptyAcquireCount, prometheus.CounterValue, float64(s.EmptyAcquireCount()))
	ch <- prometheus.MustNewConstMetric(c.canceledAcquireCount, prometheus.CounterValue, float64(s.CanceledAcquireCount()))
	ch <- prometheus.MustNewConstMetric(c.acquireDuration, prometheus.CounterValue, s.AcquireDuration().Seconds())
}
