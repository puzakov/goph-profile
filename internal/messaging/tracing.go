package messaging

import (
	"context"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// injectTraceContext переносит контекст трейса в заголовки сообщения, чтобы
// воркер продолжил трейс, начатый HTTP-запросом.
func injectTraceContext(ctx context.Context) amqp.Table {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	if len(carrier) == 0 {
		return nil
	}
	headers := make(amqp.Table, len(carrier))
	for k, v := range carrier {
		headers[k] = v
	}
	return headers
}

// ExtractTraceContext восстанавливает контекст трейса из заголовков сообщения.
// Значения заголовков AMQP не ограничены строками (x-retry-count — число),
// поэтому нестроковые значения пропускаются: propagator их не поймёт.
func ExtractTraceContext(ctx context.Context, headers amqp.Table) context.Context {
	if len(headers) == 0 {
		return ctx
	}
	carrier := make(propagation.MapCarrier, len(headers))
	for k, v := range headers {
		if s, ok := v.(string); ok {
			carrier[k] = s
		}
	}
	return otel.GetTextMapPropagator().Extract(ctx, carrier)
}
