// Package messaging реализует работу с брокером сообщений RabbitMQ:
// публикацию событий и топологию очередей с retry через dead-letter.
package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"

	"goph-profile/internal/domain"
)

// Имена обменников и очередей.
const (
	ExchangeName  = "avatars.exchange" // topic: события аватарок
	RetryExchange = "avatars.retry"    // direct: сообщения на повторную попытку
	DeadExchange  = "avatars.dlx"      // direct: сообщения с исчерпанными попытками
	RetryQueue    = "avatar.retry.queue"
	DeadQueue     = "avatar.dead.queue"
	UploadQueue   = "avatar.uploaded.queue"
	ProcessQueue  = "avatar.process.queue"
	DeleteQueue   = "avatar.deleted.queue"
)

// Заголовок сообщения с номером попытки.
const headerRetryCount = "x-retry-count"

// EventPublisher публикует события аватарок в брокер.
type EventPublisher interface {
	PublishUpload(ctx context.Context, event domain.AvatarUploadEvent) error
	PublishProcess(ctx context.Context, event domain.AvatarProcessEvent) error
	PublishDelete(ctx context.Context, event domain.AvatarDeleteEvent) error
	// RetryPublish перепубликует исходное тело события в retry-обменник
	// с экспоненциальной задержкой (expiration) и тем же message_id.
	RetryPublish(ctx context.Context, routingKey string, body []byte, messageID string, retryCount int) error
	Close() error
}

// RabbitPublisher — издатель событий на базе RabbitMQ.
type RabbitPublisher struct {
	mu   sync.Mutex // каналы amqp не потокобезопасны
	conn *amqp.Connection
	ch   *amqp.Channel
}

// NewRabbitPublisher подключается к RabbitMQ и объявляет топологию.
func NewRabbitPublisher(url string) (*RabbitPublisher, error) {
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, fmt.Errorf("dial rabbitmq: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open channel: %w", err)
	}
	if err := declareTopology(ch); err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return nil, err
	}
	return &RabbitPublisher{conn: conn, ch: ch}, nil
}

// Close закрывает канал и соединение с брокером.
func (p *RabbitPublisher) Close() error {
	if err := p.ch.Close(); err != nil {
		return err
	}
	return p.conn.Close()
}

// Ping проверяет доступность брокера: passive-объявление обменника
// не создаёт ничего нового, но возвращает ошибку, если брокер недоступен.
func (p *RabbitPublisher) Ping(_ context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ch.ExchangeDeclarePassive(
		ExchangeName, amqp.ExchangeTopic, true, false, false, false, nil)
}

// PublishUpload публикует событие загрузки аватарки в брокер.
func (p *RabbitPublisher) PublishUpload(ctx context.Context, event domain.AvatarUploadEvent) error {
	return p.publish(ctx, domain.RoutingKeyUpload, event)
}

// PublishProcess публикует событие обработки изображения в брокер.
func (p *RabbitPublisher) PublishProcess(ctx context.Context, event domain.AvatarProcessEvent) error {
	return p.publish(ctx, domain.RoutingKeyProcess, event)
}

// PublishDelete публикует событие удаления файлов аватарки в брокер.
func (p *RabbitPublisher) PublishDelete(ctx context.Context, event domain.AvatarDeleteEvent) error {
	return p.publish(ctx, domain.RoutingKeyDelete, event)
}

func (p *RabbitPublisher) publish(ctx context.Context, routingKey string, event any) error {
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ch.PublishWithContext(ctx,
		ExchangeName,
		routingKey,
		false, // mandatory
		false, // immediate
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			MessageId:    newMessageID(),
			Timestamp:    time.Now(),
			Body:         body,
		})
}

// RetryPublish отправляет тело события в retry-обменник. Сообщение попадёт
// в avatar.retry.queue с per-message TTL, а после истечения вернётся в
// avatars.exchange с исходным routing key. Тот же message_id гарантирует,
// что после успешной обработки повторное появление события будет пропущено.
// Счётчик попыток x-retry-count увеличивается: когда он достигает лимита,
// RetryDelay возвращает ok=false и сообщение уходит в dead-letter.
func (p *RabbitPublisher) RetryPublish(ctx context.Context, routingKey string, body []byte, messageID string, retryCount int) error {
	delay, ok := RetryDelay(retryCount)
	if !ok {
		return fmt.Errorf("retry count %d exceeded", retryCount)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ch.PublishWithContext(ctx,
		RetryExchange,
		routingKey,
		false,
		false,
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			MessageId:    messageID,
			Expiration:   fmt.Sprintf("%d", delay.Milliseconds()),
			Headers:      amqp.Table{headerRetryCount: int32(retryCount + 1)},
			Body:         body,
		})
}

// RetryDelay возвращает задержку для попытки retryCount (0 — первая повторная)
// по экспоненциальной схеме 1с, 2с, 4с, 8с, 16с. ok=false — попытки исчерпаны.
func RetryDelay(retryCount int) (time.Duration, bool) {
	const maxRetries = 5
	if retryCount < 0 || retryCount >= maxRetries {
		return 0, false
	}
	return time.Duration(1<<retryCount) * time.Second, true
}

func newMessageID() string {
	return uuid.NewString()
}

// declareTopology объявляет обменники, очереди и привязки.
// Идемпотентна: при повторном объявлении параметры не изменятся.
func declareTopology(ch *amqp.Channel) error {
	exchanges := []struct{ name, kind string }{
		{ExchangeName, amqp.ExchangeTopic},
		{RetryExchange, amqp.ExchangeDirect},
		{DeadExchange, amqp.ExchangeDirect},
	}
	for _, e := range exchanges {
		if err := ch.ExchangeDeclare(e.name, e.kind, true, false, false, false, nil); err != nil {
			return fmt.Errorf("declare exchange %s: %w", e.name, err)
		}
	}

	// Рабочие очереди: при reject (без requeue) сообщение уходит в DeadExchange.
	queues := []struct {
		name       string
		routingKey string
	}{
		{UploadQueue, domain.RoutingKeyUpload},
		{ProcessQueue, domain.RoutingKeyProcess},
		{DeleteQueue, domain.RoutingKeyDelete},
	}
	for _, q := range queues {
		_, err := ch.QueueDeclare(q.name, true, false, false, false, amqp.Table{
			"x-dead-letter-exchange": DeadExchange,
		})
		if err != nil {
			return fmt.Errorf("declare queue %s: %w", q.name, err)
		}
		if err := ch.QueueBind(q.name, q.routingKey, ExchangeName, false, nil); err != nil {
			return fmt.Errorf("bind queue %s: %w", q.name, err)
		}
	}

	// Retry-очередь: per-message TTL, после истечения сообщение возвращается
	// в avatars.exchange с исходным routing key.
	if _, err := ch.QueueDeclare(RetryQueue, true, false, false, false, amqp.Table{
		"x-dead-letter-exchange": ExchangeName,
	}); err != nil {
		return fmt.Errorf("declare retry queue: %w", err)
	}
	if err := ch.QueueBind(RetryQueue, "#", RetryExchange, false, nil); err != nil {
		return fmt.Errorf("bind retry queue: %w", err)
	}

	// Очередь для сообщений с исчерпанными попытками.
	if _, err := ch.QueueDeclare(DeadQueue, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare dead queue: %w", err)
	}
	if err := ch.QueueBind(DeadQueue, "#", DeadExchange, false, nil); err != nil {
		return fmt.Errorf("bind dead queue: %w", err)
	}
	return nil
}
