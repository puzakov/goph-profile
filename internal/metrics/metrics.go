// Package metrics содержит метрики Prometheus: технические (HTTP, пул БД,
// очереди брокера) и бизнесовые (загрузки и объём аватарок).
// Счётчики и гистограммы регистрируются через promauto в реестре по умолчанию.
package metrics

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// namespace — префикс имён метрик сервиса.
const namespace = "avatars"

// Статусы бизнес-метрик загрузки.
const (
	StatusOK    = "ok"
	StatusError = "error"
)

var (
	// UploadsTotal — число загрузок аватарок по статусу.
	UploadsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "uploads_total",
		Help:      "Общее число загрузок аватарок по статусу.",
	}, []string{"status"})

	// UploadDurationSeconds — длительность загрузки аватарки по статусу.
	UploadDurationSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "upload_duration_seconds",
		Help:      "Длительность загрузки аватарки в секундах.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"status"})

	// HTTPRequestsTotal — число HTTP-запросов по методу, маршруту и коду ответа.
	HTTPRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "http_requests_total",
		Help:      "Общее число HTTP-запросов по методу, маршруту и коду ответа.",
	}, []string{"method", "route", "status"})

	// HTTPRequestDurationSeconds — длительность HTTP-запросов.
	HTTPRequestDurationSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "http_request_duration_seconds",
		Help:      "Длительность HTTP-запросов в секундах.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"method", "route", "status"})

	// Метрики объёма хранилища (avatars_storage_bytes,
	// avatars_users_with_avatars) объявлены не здесь, а коллектором
	// (storage_collector.go): они считаются по БД, а не накапливаются
	// в памяти процесса.
)

// ObserveHTTP фиксирует RED-метрики одного HTTP-запроса.
// route — шаблон маршрута ("/api/v1/avatars/{avatarID}"), а не путь запроса:
// иначе кардинальность метки растёт с каждым идентификатором.
func ObserveHTTP(method, route string, status int, d time.Duration) {
	code := strconv.Itoa(status)
	HTTPRequestsTotal.WithLabelValues(method, route, code).Inc()
	HTTPRequestDurationSeconds.WithLabelValues(method, route, code).Observe(d.Seconds())
}

// ObserveUpload фиксирует бизнес-метрики загрузки аватарки.
func ObserveUpload(status string, d time.Duration) {
	UploadsTotal.WithLabelValues(status).Inc()
	UploadDurationSeconds.WithLabelValues(status).Observe(d.Seconds())
}
