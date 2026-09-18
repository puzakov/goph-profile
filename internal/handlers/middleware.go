package handlers

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"goph-profile/internal/metrics"
	"goph-profile/internal/telemetry"
)

// RequestLogger логирует каждый HTTP-запрос: метод, путь, статус, длительность,
// идентификаторы трейса и спана — и фиксирует RED-метрики запроса.
func RequestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			next.ServeHTTP(ww, r)

			duration := time.Since(start)
			route := routePattern(r)
			if route == "" {
				route = unmatchedRoute
			}
			// otelhttp выводит http.route из http.Request.Pattern, который chi
			// не заполняет, — шаблон маршрута проставляем вручную.
			trace.SpanFromContext(r.Context()).SetAttributes(semconv.HTTPRoute(route))

			log.Info("request",
				"request_id", middleware.GetReqID(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"route", route,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration_ms", duration.Milliseconds(),
				telemetry.TraceIDAttr(r.Context()),
				telemetry.SpanIDAttr(r.Context()),
			)

			metrics.ObserveHTTP(r.Method, route, ww.Status(), duration)
		})
	}
}
