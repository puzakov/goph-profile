// Package metrics содержит метрики Prometheus: технические (HTTP, пул БД,
// очереди брокера) и бизнесовые (загрузки и объём аватарок).
//
// Метрики живут в реестре, переданном снаружи: глобального состояния нет,
// поэтому в тестах каждый набор метрик изолирован и проверяется точными
// значениями, а не дельтами.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// namespace — префикс имён метрик сервиса.
const namespace = "avatars"

// Статусы бизнес-метрик загрузки.
const (
	StatusOK    = "ok"
	StatusError = "error"
)

// Metrics — метрики сервиса, зарегистрированные в переданном реестре.
type Metrics struct {
	registry *prometheus.Registry

	uploadsTotal               *prometheus.CounterVec
	uploadDurationSeconds      *prometheus.HistogramVec
	httpRequestsTotal          *prometheus.CounterVec
	httpRequestDurationSeconds *prometheus.HistogramVec
}

// NewRegistry создаёт реестр метрик сервиса. Стандартные метрики процесса
// (Go runtime, CPU, память) регистрируются сразу — их читают дашборды.
// Реестр создаётся явно вместо глобального: приложение не пишет в общее
// состояние, а тесты получают изолированный набор метрик.
func NewRegistry() *prometheus.Registry {
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return registry
}

// New регистрирует метрики сервиса в реестре reg и возвращает их держателю.
// Дополнительные коллекторы (пул БД, хранилище, очереди) регистрируются
// в тот же реестр.
func New(reg *prometheus.Registry) *Metrics {
	m := &Metrics{
		registry: reg,
		uploadsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "uploads_total",
			Help:      "Общее число загрузок аватарок по статусу.",
		}, []string{"status"}),
		uploadDurationSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "upload_duration_seconds",
			Help:      "Длительность загрузки аватарки в секундах.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"status"}),
		httpRequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "http_requests_total",
			Help:      "Общее число HTTP-запросов по методу, маршруту и коду ответа.",
		}, []string{"method", "route", "status"}),
		httpRequestDurationSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "http_request_duration_seconds",
			Help:      "Длительность HTTP-запросов в секундах.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"method", "route", "status"}),
	}
	// Метрики регистрируются один раз на старте: повторная регистрация —
	// ошибка программиста, её видно сразу на запуске.
	reg.MustRegister(
		m.uploadsTotal,
		m.uploadDurationSeconds,
		m.httpRequestsTotal,
		m.httpRequestDurationSeconds,
	)
	return m
}

// Handler отдаёт метрики реестра в формате Prometheus.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// ObserveHTTP фиксирует RED-метрики одного HTTP-запроса.
// route — шаблон маршрута ("/api/v1/avatars/{avatarID}"), а не путь запроса:
// иначе кардинальность метки растёт с каждым идентификатором.
func (m *Metrics) ObserveHTTP(method, route string, status int, d time.Duration) {
	code := strconv.Itoa(status)
	m.httpRequestsTotal.WithLabelValues(method, route, code).Inc()
	m.httpRequestDurationSeconds.WithLabelValues(method, route, code).Observe(d.Seconds())
}

// ObserveUpload фиксирует бизнес-метрики загрузки аватарки.
func (m *Metrics) ObserveUpload(status string, d time.Duration) {
	m.uploadsTotal.WithLabelValues(status).Inc()
	m.uploadDurationSeconds.WithLabelValues(status).Observe(d.Seconds())
}
