package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// serverName — имя сервиса в именах операций трейсинга.
const serverName = "avatar-server"

// unmatchedRoute — метка запросов, не совпавших ни с одним маршрутом.
// Путь из URL в метрики не попадает: иначе кардинальность метки растёт
// с каждым запросом к несуществующему адресу.
const unmatchedRoute = "unmatched"

// Tracing оборачивает HTTP-запросы в спаны OpenTelemetry.
// Миддлвар ставится снаружи RequestLogger, чтобы логгер видел спан в контексте,
// и извлекает контекст трейса из заголовков входящего запроса.
func Tracing() func(http.Handler) http.Handler {
	return otelhttp.NewMiddleware(serverName, otelhttp.WithSpanNameFormatter(spanName))
}

// spanName возвращает «METHOD шаблон-маршрута» вместо конкретного пути,
// чтобы в трейсах запросы группировались по эндпоинтам, а не по id.
func spanName(operation string, r *http.Request) string {
	if route := routePattern(r); route != "" {
		return r.Method + " " + route
	}
	return operation
}

// routePattern возвращает шаблон маршрута chi, например /api/v1/avatars/{avatarID}.
// Пустая строка — маршрут не найден.
func routePattern(r *http.Request) string {
	rctx := chi.RouteContext(r.Context())
	if rctx == nil {
		return ""
	}
	return rctx.RoutePattern()
}
