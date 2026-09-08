package worker

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"

	"goph-profile/internal/domain"
	"goph-profile/internal/storage"
)

// ---- фейки зависимостей ----

type fakeRepo struct {
	byID          map[string]*domain.Avatar
	processedMsgs map[string]bool // messageID -> уже обработан
	statusUpdates []struct {
		id, status string
	}
	thumbUpdates []struct {
		id, status string
		thumbnails []domain.Thumbnail
	}
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{byID: map[string]*domain.Avatar{}, processedMsgs: map[string]bool{}}
}

func (f *fakeRepo) Create(_ context.Context, a *domain.Avatar) error { return nil }
func (f *fakeRepo) GetByID(_ context.Context, id string) (*domain.Avatar, error) {
	a, ok := f.byID[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return a, nil
}
func (f *fakeRepo) GetCurrentByUser(_ context.Context, userID string) (*domain.Avatar, error) {
	return nil, domain.ErrNotFound
}
func (f *fakeRepo) ListByUser(_ context.Context, userID string) ([]domain.Avatar, error) {
	return nil, nil
}
func (f *fakeRepo) SoftDelete(_ context.Context, id string) error { return nil }
func (f *fakeRepo) UpdateStatus(_ context.Context, id, status string) error {
	f.statusUpdates = append(f.statusUpdates, struct {
		id, status string
	}{id, status})
	return nil
}
func (f *fakeRepo) UpdateStatusAndThumbnails(_ context.Context, id, status string, thumbnails []domain.Thumbnail) error {
	f.thumbUpdates = append(f.thumbUpdates, struct {
		id, status string
		thumbnails []domain.Thumbnail
	}{id, status, thumbnails})
	return nil
}
func (f *fakeRepo) MarkEventProcessed(_ context.Context, messageID string) (bool, error) {
	if f.processedMsgs[messageID] {
		return false, nil
	}
	f.processedMsgs[messageID] = true
	return true, nil
}
func (f *fakeRepo) Ping(_ context.Context) error { return nil }

type fakeStorage struct {
	objects map[string][]byte
	deleted []string
	failGet bool
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{objects: map[string][]byte{}}
}

func (f *fakeStorage) EnsureBucket(_ context.Context) error { return nil }
func (f *fakeStorage) Put(_ context.Context, key string, r io.Reader, size int64, contentType string) error {
	buf, _ := io.ReadAll(r)
	f.objects[key] = buf
	return nil
}
func (f *fakeStorage) Get(_ context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	if f.failGet {
		return nil, storage.ObjectInfo{}, errors.New("storage down")
	}
	buf, ok := f.objects[key]
	if !ok {
		return nil, storage.ObjectInfo{}, storage.ErrObjectNotFound
	}
	return io.NopCloser(bytes.NewReader(buf)), storage.ObjectInfo{ETag: "etag", Size: int64(len(buf))}, nil
}
func (f *fakeStorage) Delete(_ context.Context, key string) error {
	f.deleted = append(f.deleted, key)
	delete(f.objects, key)
	return nil
}
func (f *fakeStorage) Ping(_ context.Context) error { return nil }

type fakePublisher struct {
	uploads  []domain.AvatarUploadEvent
	process  []domain.AvatarProcessEvent
	deletes  []domain.AvatarDeleteEvent
	retries  []string
	failNext bool
}

func (f *fakePublisher) PublishUpload(_ context.Context, e domain.AvatarUploadEvent) error {
	if f.failNext {
		f.failNext = false
		return errors.New("broker down")
	}
	f.uploads = append(f.uploads, e)
	return nil
}
func (f *fakePublisher) PublishProcess(_ context.Context, e domain.AvatarProcessEvent) error {
	if f.failNext {
		f.failNext = false
		return errors.New("broker down")
	}
	f.process = append(f.process, e)
	return nil
}
func (f *fakePublisher) PublishDelete(_ context.Context, e domain.AvatarDeleteEvent) error {
	f.deletes = append(f.deletes, e)
	return nil
}
func (f *fakePublisher) RetryPublish(_ context.Context, routingKey string, body []byte, messageID string, retryCount int) error {
	f.retries = append(f.retries, routingKey)
	return nil
}
func (f *fakePublisher) Close() error { return nil }

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ---- данные ----

var png1x1 = decodePNG()

func decodePNG() []byte {
	b, err := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	if err != nil {
		panic(err)
	}
	return b
}

// ---- тесты ProcessUploadEvent ----

func TestProcessUploadEvent_PublishesProcessAndSetsProcessing(t *testing.T) {
	repo := newFakeRepo()
	repo.byID["a1"] = &domain.Avatar{ID: "a1", Status: domain.StatusPending}
	st := newFakeStorage()
	pub := &fakePublisher{}
	w := NewWorker(repo, st, pub, testLogger())

	err := w.ProcessUploadEvent(context.Background(), "msg-1", domain.AvatarUploadEvent{
		AvatarID: "a1", UserID: "user-1", S3Key: "avatars/a1/original.png",
	})
	require.NoError(t, err)

	require.Len(t, pub.process, 1)
	require.Equal(t, "a1", pub.process[0].AvatarID)
	require.Equal(t, []domain.ProcessingOp{domain.OpResize100, domain.OpResize300},
		pub.process[0].Operations)

	require.Len(t, repo.statusUpdates, 1)
	require.Equal(t, "a1", repo.statusUpdates[0].id)
	require.Equal(t, domain.StatusProcessing, repo.statusUpdates[0].status)
}

func TestProcessUploadEvent_SkipsWhenReady(t *testing.T) {
	repo := newFakeRepo()
	repo.byID["a1"] = &domain.Avatar{ID: "a1", Status: domain.StatusReady}
	pub := &fakePublisher{}
	w := NewWorker(repo, newFakeStorage(), pub, testLogger())

	err := w.ProcessUploadEvent(context.Background(), "msg-1", domain.AvatarUploadEvent{AvatarID: "a1"})
	require.NoError(t, err)
	require.Empty(t, pub.process, "событие process не должно публиковаться")
	require.Empty(t, repo.statusUpdates)
}

func TestProcessUploadEvent_SkipsWhenProcessing(t *testing.T) {
	repo := newFakeRepo()
	repo.byID["a1"] = &domain.Avatar{ID: "a1", Status: domain.StatusProcessing}
	pub := &fakePublisher{}
	w := NewWorker(repo, newFakeStorage(), pub, testLogger())

	err := w.ProcessUploadEvent(context.Background(), "msg-1", domain.AvatarUploadEvent{AvatarID: "a1"})
	require.NoError(t, err)
	require.Empty(t, pub.process)
}

func TestProcessUploadEvent_SkipsWhenNotFound(t *testing.T) {
	pub := &fakePublisher{}
	w := NewWorker(newFakeRepo(), newFakeStorage(), pub, testLogger())

	err := w.ProcessUploadEvent(context.Background(), "msg-1", domain.AvatarUploadEvent{AvatarID: "missing"})
	require.NoError(t, err, "удалённая аватарка не должна давать ошибку")
	require.Empty(t, pub.process)
}

func TestProcessUploadEvent_DuplicateMessage(t *testing.T) {
	repo := newFakeRepo()
	repo.byID["a1"] = &domain.Avatar{ID: "a1", Status: domain.StatusPending}
	pub := &fakePublisher{}
	w := NewWorker(repo, newFakeStorage(), pub, testLogger())

	event := domain.AvatarUploadEvent{AvatarID: "a1", UserID: "user-1", S3Key: "k"}
	require.NoError(t, w.ProcessUploadEvent(context.Background(), "msg-1", event))
	require.Len(t, pub.process, 1)

	// Повторное доставление того же сообщения (retry после потери ack).
	require.NoError(t, w.ProcessUploadEvent(context.Background(), "msg-1", event))
	require.Len(t, pub.process, 1, "дубликат не должен обрабатываться повторно")
	require.Len(t, repo.statusUpdates, 1)
}

func TestProcessUploadEvent_RetryableWhenPublishFails(t *testing.T) {
	repo := newFakeRepo()
	repo.byID["a1"] = &domain.Avatar{ID: "a1", Status: domain.StatusPending}
	pub := &fakePublisher{failNext: true}
	w := NewWorker(repo, newFakeStorage(), pub, testLogger())

	err := w.ProcessUploadEvent(context.Background(), "msg-1", domain.AvatarUploadEvent{AvatarID: "a1"})
	var he *handlerError
	require.ErrorAs(t, err, &he, "сбой брокера — retryable ошибка")
	require.Equal(t, "a1", he.avatarID)
}

// ---- тесты ProcessProcessEvent ----

func TestProcessProcessEvent_CreatesThumbnailsAndMarksReady(t *testing.T) {
	repo := newFakeRepo()
	repo.byID["a1"] = &domain.Avatar{
		ID: "a1", Status: domain.StatusProcessing, S3Key: "avatars/a1/original.png",
	}
	st := newFakeStorage()
	st.objects["avatars/a1/original.png"] = png1x1
	pub := &fakePublisher{}
	w := NewWorker(repo, st, pub, testLogger())

	event := domain.AvatarProcessEvent{
		AvatarID:   "a1",
		Operations: []domain.ProcessingOp{domain.OpResize100, domain.OpResize300},
	}
	require.NoError(t, w.ProcessProcessEvent(context.Background(), "msg-1", event))

	// Миниатюры в S3.
	require.Contains(t, st.objects, "thumbnails/a1/100x100.jpg")
	require.Contains(t, st.objects, "thumbnails/a1/300x300.jpg")
	require.True(t, bytes.HasPrefix(st.objects["thumbnails/a1/100x100.jpg"], []byte{0xFF, 0xD8}),
		"миниатюра должна быть JPEG")

	// Статус ready + миниатюры в БД.
	require.Len(t, repo.thumbUpdates, 1)
	require.Equal(t, domain.StatusReady, repo.thumbUpdates[0].status)
	require.Len(t, repo.thumbUpdates[0].thumbnails, 2)
	require.Equal(t, "100x100", repo.thumbUpdates[0].thumbnails[0].Size)
	require.Equal(t, "thumbnails/a1/100x100.jpg", repo.thumbUpdates[0].thumbnails[0].Key)
}

func TestProcessProcessEvent_SkipsWhenReady(t *testing.T) {
	repo := newFakeRepo()
	repo.byID["a1"] = &domain.Avatar{ID: "a1", Status: domain.StatusReady}
	st := newFakeStorage()
	w := NewWorker(repo, st, &fakePublisher{}, testLogger())

	err := w.ProcessProcessEvent(context.Background(), "msg-1",
		domain.AvatarProcessEvent{AvatarID: "a1", Operations: []domain.ProcessingOp{domain.OpResize100}})
	require.NoError(t, err)
	require.Empty(t, st.objects, "миниатюры не должны создаваться повторно")
	require.Empty(t, repo.thumbUpdates)
}

func TestProcessProcessEvent_DecodeFailedMarksFailed(t *testing.T) {
	repo := newFakeRepo()
	repo.byID["a1"] = &domain.Avatar{ID: "a1", Status: domain.StatusProcessing, S3Key: "k"}
	st := newFakeStorage()
	st.objects["k"] = []byte("not an image")
	w := NewWorker(repo, st, &fakePublisher{}, testLogger())

	err := w.ProcessProcessEvent(context.Background(), "msg-1",
		domain.AvatarProcessEvent{AvatarID: "a1", Operations: []domain.ProcessingOp{domain.OpResize100}})
	require.NoError(t, err, "битый файл — фатальная ошибка, повтор не нужен")

	require.Len(t, repo.statusUpdates, 2)
	require.Equal(t, domain.StatusProcessing, repo.statusUpdates[0].status)
	require.Equal(t, domain.StatusFailed, repo.statusUpdates[1].status)
}

func TestProcessProcessEvent_RetryableWhenStorageDown(t *testing.T) {
	repo := newFakeRepo()
	repo.byID["a1"] = &domain.Avatar{ID: "a1", Status: domain.StatusProcessing, S3Key: "k"}
	st := newFakeStorage()
	st.objects["k"] = png1x1
	st.failGet = true
	w := NewWorker(repo, st, &fakePublisher{}, testLogger())

	err := w.ProcessProcessEvent(context.Background(), "msg-1",
		domain.AvatarProcessEvent{AvatarID: "a1", Operations: []domain.ProcessingOp{domain.OpResize100}})
	var he *handlerError
	require.ErrorAs(t, err, &he)
	require.Equal(t, "a1", he.avatarID)
}

func TestProcessProcessEvent_SkipsUnknownOperation(t *testing.T) {
	repo := newFakeRepo()
	repo.byID["a1"] = &domain.Avatar{ID: "a1", Status: domain.StatusProcessing, S3Key: "k"}
	st := newFakeStorage()
	st.objects["k"] = png1x1
	w := NewWorker(repo, st, &fakePublisher{}, testLogger())

	event := domain.AvatarProcessEvent{
		AvatarID:   "a1",
		Operations: []domain.ProcessingOp{"resize_unknown", domain.OpResize100},
	}
	require.NoError(t, w.ProcessProcessEvent(context.Background(), "msg-1", event))
	require.Contains(t, st.objects, "thumbnails/a1/100x100.jpg")
	require.NotContains(t, st.objects, "thumbnails/a1/unknown.jpg")
	require.Len(t, repo.thumbUpdates[0].thumbnails, 1)
}

// ---- тесты ProcessDeleteEvent ----

func TestProcessDeleteEvent_DeletesAllKeys(t *testing.T) {
	st := newFakeStorage()
	st.objects["avatars/a1/original.png"] = png1x1
	st.objects["thumbnails/a1/100x100.jpg"] = png1x1
	w := NewWorker(newFakeRepo(), st, &fakePublisher{}, testLogger())

	event := domain.AvatarDeleteEvent{
		AvatarID: "a1",
		S3Keys:   []string{"avatars/a1/original.png", "thumbnails/a1/100x100.jpg"},
	}
	require.NoError(t, w.ProcessDeleteEvent(context.Background(), "msg-1", event))

	require.ElementsMatch(t, event.S3Keys, st.deleted)
	require.Empty(t, st.objects, "все объекты должны быть удалены")
}

func TestProcessDeleteEvent_DuplicateMessage(t *testing.T) {
	st := newFakeStorage()
	st.objects["k"] = png1x1
	w := NewWorker(newFakeRepo(), st, &fakePublisher{}, testLogger())

	event := domain.AvatarDeleteEvent{AvatarID: "a1", S3Keys: []string{"k"}}
	require.NoError(t, w.ProcessDeleteEvent(context.Background(), "msg-1", event))
	require.NoError(t, w.ProcessDeleteEvent(context.Background(), "msg-1", event))
	require.Len(t, st.deleted, 1, "дубликат события не должен удалять повторно")
}

// ---- тесты handleDelivery ----

type fakeAck struct {
	acked    int
	rejected int
	requeued []bool
}

func (f *fakeAck) Ack(_ uint64, _ bool) error {
	f.acked++
	return nil
}
func (f *fakeAck) Nack(_ uint64, _ bool, requeue bool) error {
	f.requeued = append(f.requeued, requeue)
	return nil
}
func (f *fakeAck) Reject(_ uint64, requeue bool) error {
	f.rejected++
	f.requeued = append(f.requeued, requeue)
	return nil
}

func delivery(ack *fakeAck, msgID string, headers amqp.Table, body []byte) amqp.Delivery {
	return amqp.Delivery{
		Acknowledger: ack,
		MessageId:    msgID,
		RoutingKey:   domain.RoutingKeyUpload,
		Headers:      headers,
		Body:         body,
	}
}

func TestHandleDelivery_SuccessAcks(t *testing.T) {
	ack := &fakeAck{}
	w := NewWorker(newFakeRepo(), newFakeStorage(), &fakePublisher{}, testLogger())

	w.handleDelivery(context.Background(), delivery(ack, "m1", nil, nil),
		func(_ context.Context, _ amqp.Delivery) error { return nil })

	require.Equal(t, 1, ack.acked)
	require.Equal(t, 0, ack.rejected)
}

func TestHandleDelivery_RetryableErrorRepublishes(t *testing.T) {
	ack := &fakeAck{}
	pub := &fakePublisher{}
	w := NewWorker(newFakeRepo(), newFakeStorage(), pub, testLogger())

	w.handleDelivery(context.Background(), delivery(ack, "m1", nil, []byte("{}")),
		func(_ context.Context, d amqp.Delivery) error {
			return &handlerError{avatarID: "a1", err: errors.New("db down")}
		})

	require.Equal(t, 1, ack.acked, "сообщение подтверждается после постановки в retry")
	require.Equal(t, []string{domain.RoutingKeyUpload}, pub.retries)
}

func TestHandleDelivery_ExhaustedRetriesGoToDeadLetter(t *testing.T) {
	ack := &fakeAck{}
	pub := &fakePublisher{}
	repo := newFakeRepo()
	repo.byID["a1"] = &domain.Avatar{ID: "a1", Status: domain.StatusProcessing}
	w := NewWorker(repo, newFakeStorage(), pub, testLogger())

	// Попытки исчерпаны: x-retry-count = 5.
	headers := amqp.Table{"x-retry-count": int32(5)}
	w.handleDelivery(context.Background(), delivery(ack, "m1", headers, []byte("{}")),
		func(_ context.Context, d amqp.Delivery) error {
			return &handlerError{avatarID: "a1", err: errors.New("still down")}
		})

	require.Equal(t, 0, ack.acked)
	require.Equal(t, 1, ack.rejected, "сообщение уходит в dead-letter")
	require.Empty(t, pub.retries)
	require.Equal(t, domain.StatusFailed, repo.statusUpdates[0].status,
		"аватарка помечается failed")
}

func TestHandleDelivery_NonRetryableErrorRejects(t *testing.T) {
	ack := &fakeAck{}
	pub := &fakePublisher{}
	repo := newFakeRepo()
	w := NewWorker(repo, newFakeStorage(), pub, testLogger())

	w.handleDelivery(context.Background(), delivery(ack, "m1", nil, []byte("not json")),
		func(_ context.Context, d amqp.Delivery) error {
			return errors.New("unmarshal failed")
		})

	require.Equal(t, 1, ack.rejected)
	require.Empty(t, pub.retries)
	require.Empty(t, repo.statusUpdates, "не-retryable ошибка не меняет статус")
}
