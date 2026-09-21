// Package worker реализует фоновую обработку изображений:
// конвейер avatar.uploaded -> avatar.process (создание миниатюр)
// и avatar.deleted (удаление файлов из S3).
package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"goph-profile/internal/domain"
	"goph-profile/internal/imaging"
	"goph-profile/internal/messaging"
	"goph-profile/internal/repository"
	"goph-profile/internal/storage"
	"goph-profile/internal/telemetry"
)

// tracer — инструмент создания спанов обработки сообщений: имя то же, что
// у сервиса в ресурсе трейсов, иначе спаны одного процесса выглядели бы
// как спаны разных сервисов.
var tracer = otel.Tracer(telemetry.ServiceWorker)

// jpegQuality — качество кодирования миниатюр JPEG.
const jpegQuality = 85

// thumbSpec — параметры миниатюры для операции обработки.
type thumbSpec struct {
	name   string
	width  int
	height int
}

// opThumbs — соответствие операций обработки и параметров миниатюр.
var opThumbs = map[domain.ProcessingOp]thumbSpec{
	domain.OpResize100: {name: "100x100", width: 100, height: 100},
	domain.OpResize300: {name: "300x300", width: 300, height: 300},
}

// handlerError — повторяемая (retryable) ошибка обработчика.
// avatarID нужен, чтобы пометить аватарку failed при исчерпании попыток.
type handlerError struct {
	avatarID string
	err      error
}

func (e *handlerError) Error() string { return e.err.Error() }
func (e *handlerError) Unwrap() error { return e.err }

// Worker — сервис фоновой обработки аватарок.
type Worker struct {
	repo      repository.AvatarRepository
	storage   storage.AvatarStorage
	publisher messaging.EventPublisher
	log       *slog.Logger
}

// NewWorker создаёт воркер с заданными зависимостями.
func NewWorker(repo repository.AvatarRepository, st storage.AvatarStorage,
	publisher messaging.EventPublisher, log *slog.Logger) *Worker {
	return &Worker{repo: repo, storage: st, publisher: publisher, log: log}
}

// Run запускает консьюмеры всех очередей и ждёт отмены ctx.
func (w *Worker) Run(ctx context.Context, rabbitURL string) error {
	conn, err := amqp.Dial(rabbitURL)
	if err != nil {
		return fmt.Errorf("dial rabbitmq: %w", err)
	}
	defer func() { _ = conn.Close() }()

	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("open channel: %w", err)
	}
	defer func() { _ = ch.Close() }()

	// Обрабатываем по одному сообщению за раз: ретраи не переупорядочиваются.
	if err := ch.Qos(1, 0, false); err != nil {
		return fmt.Errorf("set qos: %w", err)
	}

	consumers := []struct {
		queue   string
		handler func(context.Context, amqp.Delivery) error
	}{
		{messaging.UploadQueue, w.handleUploadDelivery},
		{messaging.ProcessQueue, w.handleProcessDelivery},
		{messaging.DeleteQueue, w.handleDeleteDelivery},
	}

	errCh := make(chan error, len(consumers))
	for _, c := range consumers {
		go func() {
			errCh <- w.consumeQueue(ctx, ch, c.queue, c.handler)
		}()
	}

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

// consumeQueue читает сообщения из очереди и обрабатывает их с ретраями.
func (w *Worker) consumeQueue(ctx context.Context, ch *amqp.Channel, queue string,
	handler func(context.Context, amqp.Delivery) error) error {
	msgs, err := ch.Consume(queue, "", false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("consume %s: %w", queue, err)
	}
	w.log.Info("consuming queue", "queue", queue)
	for {
		select {
		case <-ctx.Done():
			return nil
		case d, ok := <-msgs:
			if !ok {
				return fmt.Errorf("delivery channel closed for %s", queue)
			}
			w.handleDelivery(ctx, d, queue, handler)
		}
	}
}

// handleDelivery обрабатывает одно сообщение: при retryable-ошибке публикует
// его в retry-обменник с экспоненциальной задержкой, при исчерпании попыток
// отправляет в dead-letter и помечает аватарку failed.
func (w *Worker) handleDelivery(ctx context.Context, d amqp.Delivery, queue string,
	handler func(context.Context, amqp.Delivery) error) {
	// Уникальный идентификатор сообщения нужен для идемпотентной обработки.
	msgID := d.MessageId
	if msgID == "" {
		msgID = uuid.NewString()
		w.log.Warn("message without id, generated one", "queue", d.RoutingKey)
	}
	d.MessageId = msgID

	// Продолжаем трейс издателя: его контекст пришёл в заголовках сообщения.
	ctx, span := tracer.Start(messaging.ExtractTraceContext(ctx, d.Headers), "consume_message",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			semconv.MessagingSystemRabbitMQ,
			semconv.MessagingDestinationName(queue),
			semconv.MessagingRabbitMQDestinationRoutingKey(d.RoutingKey),
			semconv.MessagingMessageID(msgID),
		))
	var err error
	defer func() { telemetry.EndSpan(span, err) }()

	w.log.Info("message received",
		"queue", queue, "message_id", msgID, "redelivered", d.Redelivered,
		telemetry.TraceIDAttr(ctx))

	err = handler(ctx, d)
	if err == nil {
		_ = d.Ack(false)
		return
	}

	var he *handlerError
	if errors.As(err, &he) {
		retryCount := headerRetryCount(d)
		if _, ok := messaging.RetryDelay(retryCount); ok {
			pubErr := w.publisher.RetryPublish(ctx, d.RoutingKey, d.Body, msgID, retryCount)
			if pubErr == nil {
				w.log.Warn("retry scheduled",
					"queue", d.RoutingKey, "attempt", retryCount+1, "error", err,
					telemetry.TraceIDAttr(ctx))
				_ = d.Ack(false)
				return
			}
			w.log.Error("retry publish failed", "error", pubErr, telemetry.TraceIDAttr(ctx))
		}
	}

	// Попытки исчерпаны (или сообщение некорректно): отправляем в dead-letter.
	w.log.Error("message dead-lettered",
		"queue", d.RoutingKey, "message_id", msgID, "error", err,
		telemetry.TraceIDAttr(ctx))
	_ = d.Reject(false)
	if he != nil && he.avatarID != "" {
		if err := w.repo.UpdateStatus(ctx, he.avatarID, domain.StatusFailed); err != nil {
			w.log.Error("mark avatar failed", "avatar_id", he.avatarID, "error", err)
		}
	}
}

// headerRetryCount — число предыдущих повторных попыток сообщения.
func headerRetryCount(d amqp.Delivery) int {
	if v, ok := d.Headers["x-retry-count"].(int32); ok {
		return int(v)
	}
	return 0
}

// ---- обработчики событий ----

// handleUploadDelivery разбирает AvatarUploadEvent и запускает конвейер.
func (w *Worker) handleUploadDelivery(ctx context.Context, d amqp.Delivery) error {
	var event domain.AvatarUploadEvent
	if err := json.Unmarshal(d.Body, &event); err != nil {
		return fmt.Errorf("unmarshal upload event: %w", err)
	}
	return w.ProcessUploadEvent(ctx, d.MessageId, event)
}

// ProcessUploadEvent переводит аватарку из pending в processing и публикует
// AvatarProcessEvent с операциями создания миниатюр. Идемпотентно:
// дубликаты по message_id и по статусу аватарки пропускаются.
func (w *Worker) ProcessUploadEvent(ctx context.Context, messageID string, event domain.AvatarUploadEvent) error {
	processed, err := w.repo.MarkEventProcessed(ctx, messageID)
	if err != nil {
		return &handlerError{event.AvatarID, err}
	}
	if !processed {
		w.log.Info("duplicate event skipped", "message_id", messageID, "event", "upload")
		return nil
	}

	avatar, err := w.repo.GetByID(ctx, event.AvatarID)
	if errors.Is(err, domain.ErrNotFound) {
		// Аватарка удалена раньше, чем событие дошло до воркера.
		w.log.Warn("avatar not found, skip upload event", "avatar_id", event.AvatarID)
		return nil
	}
	if err != nil {
		return &handlerError{event.AvatarID, err}
	}

	// Проверка статуса перед обработкой — защита от повторной обработки.
	if avatar.Status != domain.StatusPending {
		w.log.Info("avatar not pending, skip upload event",
			"avatar_id", event.AvatarID, "status", avatar.Status)
		return nil
	}

	if err := w.repo.UpdateStatus(ctx, event.AvatarID, domain.StatusProcessing); err != nil {
		return &handlerError{event.AvatarID, err}
	}

	processEvent := domain.AvatarProcessEvent{
		AvatarID:   event.AvatarID,
		Operations: []domain.ProcessingOp{domain.OpResize100, domain.OpResize300},
	}
	if err := w.publisher.PublishProcess(ctx, processEvent); err != nil {
		return &handlerError{event.AvatarID, err}
	}
	w.log.Info("process event published", "avatar_id", event.AvatarID)
	return nil
}

// handleProcessDelivery разбирает AvatarProcessEvent и выполняет операции.
func (w *Worker) handleProcessDelivery(ctx context.Context, d amqp.Delivery) error {
	var event domain.AvatarProcessEvent
	if err := json.Unmarshal(d.Body, &event); err != nil {
		return fmt.Errorf("unmarshal process event: %w", err)
	}
	return w.ProcessProcessEvent(ctx, d.MessageId, event)
}

// ProcessProcessEvent выполняет операции обработки: скачивает оригинал,
// создаёт миниатюры JPEG, загружает их в S3 и помечает аватарку ready.
func (w *Worker) ProcessProcessEvent(ctx context.Context, messageID string, event domain.AvatarProcessEvent) error {
	processed, err := w.repo.MarkEventProcessed(ctx, messageID)
	if err != nil {
		return &handlerError{event.AvatarID, err}
	}
	if !processed {
		w.log.Info("duplicate event skipped", "message_id", messageID, "event", "process")
		return nil
	}

	avatar, err := w.repo.GetByID(ctx, event.AvatarID)
	if errors.Is(err, domain.ErrNotFound) {
		w.log.Warn("avatar not found, skip process event", "avatar_id", event.AvatarID)
		return nil
	}
	if err != nil {
		return &handlerError{event.AvatarID, err}
	}
	if avatar.Status == domain.StatusReady {
		w.log.Info("avatar already ready, skip process event", "avatar_id", event.AvatarID)
		return nil
	}

	if err := w.repo.UpdateStatus(ctx, event.AvatarID, domain.StatusProcessing); err != nil {
		return &handlerError{event.AvatarID, err}
	}

	rc, _, err := w.storage.Get(ctx, avatar.S3Key)
	if err != nil {
		return &handlerError{event.AvatarID, fmt.Errorf("download original: %w", err)}
	}
	defer func() { _ = rc.Close() }()

	img, err := imaging.Decode(rc)
	if err != nil {
		// Битый файл не починят повторные попытки — фиксируем failed.
		if serr := w.repo.UpdateStatus(ctx, event.AvatarID, domain.StatusFailed); serr != nil {
			w.log.Error("mark avatar failed", "avatar_id", event.AvatarID, "error", serr)
		}
		w.log.Error("decode original failed", "avatar_id", event.AvatarID, "error", err)
		return nil
	}

	thumbnails := make([]domain.Thumbnail, 0, len(event.Operations))
	for _, op := range event.Operations {
		spec, ok := opThumbs[op]
		if !ok {
			w.log.Warn("unknown operation skipped", "operation", op)
			continue
		}
		data, err := imaging.ResizeToJPEG(img, spec.width, spec.height, jpegQuality)
		if err != nil {
			return &handlerError{event.AvatarID, fmt.Errorf("resize %s: %w", spec.name, err)}
		}
		key := fmt.Sprintf("thumbnails/%s/%s.jpg", event.AvatarID, spec.name)
		if err := w.storage.Put(ctx, key, bytes.NewReader(data), int64(len(data)), "image/jpeg"); err != nil {
			return &handlerError{event.AvatarID, fmt.Errorf("upload %s: %w", key, err)}
		}
		thumbnails = append(thumbnails, domain.Thumbnail{Size: spec.name, Key: key})
	}

	if err := w.repo.UpdateStatusAndThumbnails(ctx, event.AvatarID, domain.StatusReady, thumbnails); err != nil {
		return &handlerError{event.AvatarID, err}
	}
	w.log.Info("avatar processed", "avatar_id", event.AvatarID,
		"thumbnails", len(thumbnails))
	return nil
}

// handleDeleteDelivery разбирает AvatarDeleteEvent и удаляет файлы.
func (w *Worker) handleDeleteDelivery(ctx context.Context, d amqp.Delivery) error {
	var event domain.AvatarDeleteEvent
	if err := json.Unmarshal(d.Body, &event); err != nil {
		return fmt.Errorf("unmarshal delete event: %w", err)
	}
	return w.ProcessDeleteEvent(ctx, d.MessageId, event)
}

// ProcessDeleteEvent удаляет ключи аватарки из S3. Идемпотентно:
// удаление отсутствующего объекта не считается ошибкой.
func (w *Worker) ProcessDeleteEvent(ctx context.Context, messageID string, event domain.AvatarDeleteEvent) error {
	processed, err := w.repo.MarkEventProcessed(ctx, messageID)
	if err != nil {
		return &handlerError{event.AvatarID, err}
	}
	if !processed {
		w.log.Info("duplicate event skipped", "message_id", messageID, "event", "delete")
		return nil
	}

	for _, key := range event.S3Keys {
		if key == "" {
			continue
		}
		err := w.storage.Delete(ctx, key)
		if err != nil && !errors.Is(err, storage.ErrObjectNotFound) {
			return &handlerError{event.AvatarID, fmt.Errorf("delete %s: %w", key, err)}
		}
	}
	w.log.Info("avatar files deleted", "avatar_id", event.AvatarID, "keys", len(event.S3Keys))
	return nil
}
