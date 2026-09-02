package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"goph-profile/internal/domain"
	"goph-profile/internal/messaging"
	"goph-profile/internal/storage"
)

// ---- фейки зависимостей ----

type fakeRepo struct {
	created     []*domain.Avatar
	byID        map[string]*domain.Avatar
	softDeleted []string
}

func (f *fakeRepo) Create(_ context.Context, a *domain.Avatar) error {
	f.created = append(f.created, a)
	return nil
}
func (f *fakeRepo) GetByID(_ context.Context, id string) (*domain.Avatar, error) {
	a, ok := f.byID[id]
	if !ok || a.DeletedAt != nil {
		return nil, domain.ErrNotFound
	}
	return a, nil
}
func (f *fakeRepo) GetCurrentByUser(_ context.Context, _ string) (*domain.Avatar, error) {
	return nil, domain.ErrNotFound
}
func (f *fakeRepo) ListByUser(_ context.Context, userID string) ([]domain.Avatar, error) {
	return nil, nil
}
func (f *fakeRepo) SoftDelete(_ context.Context, id string) error {
	f.softDeleted = append(f.softDeleted, id)
	return nil
}
func (f *fakeRepo) UpdateStatus(_ context.Context, _, status string) error { return nil }
func (f *fakeRepo) UpdateStatusAndThumbnails(_ context.Context, _, status string, thumbnails []domain.Thumbnail) error {
	return nil
}
func (f *fakeRepo) MarkEventProcessed(_ context.Context, _ string) (bool, error) {
	return true, nil
}
func (f *fakeRepo) Ping(_ context.Context) error { return nil }

type fakeStorage struct {
	putKeys     []string
	deletedKeys []string
	objects     map[string][]byte
}

func (f *fakeStorage) EnsureBucket(_ context.Context) error { return nil }
func (f *fakeStorage) Put(_ context.Context, key string, r io.Reader, _ int64, contentType string) error {
	f.putKeys = append(f.putKeys, key)
	if f.objects == nil {
		f.objects = map[string][]byte{}
	}
	buf, _ := io.ReadAll(r)
	f.objects[key] = buf
	return nil
}
func (f *fakeStorage) Get(_ context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	buf, ok := f.objects[key]
	if !ok {
		return nil, storage.ObjectInfo{}, storage.ErrObjectNotFound
	}
	return io.NopCloser(bytes.NewReader(buf)), storage.ObjectInfo{ETag: "etag", Size: int64(len(buf))}, nil
}
func (f *fakeStorage) Delete(_ context.Context, key string) error {
	f.deletedKeys = append(f.deletedKeys, key)
	delete(f.objects, key)
	return nil
}
func (f *fakeStorage) Ping(_ context.Context) error { return nil }

type fakePublisher struct {
	uploads []domain.AvatarUploadEvent
	deletes []domain.AvatarDeleteEvent
	process []domain.AvatarProcessEvent
	pingErr error
}

func (f *fakePublisher) PublishUpload(_ context.Context, e domain.AvatarUploadEvent) error {
	f.uploads = append(f.uploads, e)
	return nil
}
func (f *fakePublisher) PublishProcess(_ context.Context, e domain.AvatarProcessEvent) error {
	f.process = append(f.process, e)
	return nil
}
func (f *fakePublisher) PublishDelete(_ context.Context, e domain.AvatarDeleteEvent) error {
	f.deletes = append(f.deletes, e)
	return nil
}
func (f *fakePublisher) RetryPublish(_ context.Context, _ string, body []byte, messageID string, retryCount int) error {
	return nil
}
func (f *fakePublisher) Close() error                 { return nil }
func (f *fakePublisher) Ping(_ context.Context) error { return f.pingErr }

// ---- данные ----

var png1x1 = decodeTestPNG()

func decodeTestPNG() []byte {
	b, err := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	if err != nil {
		panic(err)
	}
	return b
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newService(repo *fakeRepo, st *fakeStorage, pub *fakePublisher) *AvatarService {
	if repo == nil {
		repo = &fakeRepo{}
	}
	if st == nil {
		st = &fakeStorage{}
	}
	// nil *fakePublisher нельзя оборачивать в интерфейс напрямую:
	// типизированный nil не равен nil-интерфейсу.
	var publisher messaging.EventPublisher
	if pub != nil {
		publisher = pub
	}
	return NewAvatarService(repo, st, publisher, testLogger())
}

// ---- тесты ----

func TestUpload_RejectsInvalidFormat(t *testing.T) {
	st := &fakeStorage{}
	svc := newService(nil, st, nil)

	_, err := svc.Upload(context.Background(), "user-1", "file.txt", strings.NewReader("hello, not an image"))

	require.ErrorIs(t, err, domain.ErrInvalidFormat)
	require.Empty(t, st.putKeys)
}

func TestUpload_RejectsTooLarge(t *testing.T) {
	st := &fakeStorage{}
	svc := newService(nil, st, nil)

	_, err := svc.Upload(context.Background(), "user-1", "big.png",
		io.LimitReader(bytes.NewReader(make([]byte, MaxUploadSize*2)), MaxUploadSize*2))

	require.ErrorIs(t, err, domain.ErrFileTooLarge)
	require.Empty(t, st.putKeys)
}

func TestUpload_SuccessPublishesEventWithPendingStatus(t *testing.T) {
	repo := &fakeRepo{}
	st := &fakeStorage{}
	pub := &fakePublisher{}
	svc := newService(repo, st, pub)

	avatar, err := svc.Upload(context.Background(), "user-1", "photo.png", bytes.NewReader(png1x1))
	require.NoError(t, err)

	require.Equal(t, "user-1", avatar.UserID)
	require.Equal(t, "image/png", avatar.MimeType)
	require.Equal(t, domain.StatusPending, avatar.Status,
		"аватарка ждёт асинхронной обработки")
	require.Equal(t, 1, avatar.Width)
	require.Equal(t, 1, avatar.Height)
	require.Equal(t, int64(len(png1x1)), avatar.SizeBytes)

	require.Len(t, st.putKeys, 1)
	require.True(t, strings.HasPrefix(st.putKeys[0], "avatars/"+avatar.ID+"/original.png"))
	require.Equal(t, avatar.S3Key, st.putKeys[0])
	require.Len(t, repo.created, 1)

	require.Len(t, pub.uploads, 1, "событие загрузки должно уйти в брокер")
	require.Equal(t, domain.AvatarUploadEvent{
		AvatarID: avatar.ID, UserID: "user-1", S3Key: avatar.S3Key,
	}, pub.uploads[0])
}

func TestUpload_WorksWithoutPublisher(t *testing.T) {
	// Без брокера загрузка должна работать: аватарка просто останется pending.
	repo := &fakeRepo{}
	svc := newService(repo, &fakeStorage{}, nil)

	avatar, err := svc.Upload(context.Background(), "user-1", "photo.png", bytes.NewReader(png1x1))
	require.NoError(t, err)
	require.Equal(t, domain.StatusPending, avatar.Status)
}

func TestDelete_ForbiddenForForeignAvatar(t *testing.T) {
	repo := &fakeRepo{byID: map[string]*domain.Avatar{
		"a1": {ID: "a1", UserID: "owner-1"},
	}}
	svc := newService(repo, &fakeStorage{}, &fakePublisher{})

	err := svc.Delete(context.Background(), "attacker", "a1")

	require.ErrorIs(t, err, domain.ErrForbidden)
	require.Empty(t, repo.softDeleted)
}

func TestDelete_PublishesDeleteEventInsteadOfSyncRemoval(t *testing.T) {
	repo := &fakeRepo{byID: map[string]*domain.Avatar{
		"a1": {ID: "a1", UserID: "owner-1", S3Key: "avatars/a1/original.png",
			Thumbnails: []domain.Thumbnail{{Size: "100x100", Key: "thumbnails/a1/100x100.jpg"}}},
	}}
	st := &fakeStorage{}
	pub := &fakePublisher{}
	svc := newService(repo, st, pub)

	require.NoError(t, svc.Delete(context.Background(), "owner-1", "a1"))

	require.Equal(t, []string{"a1"}, repo.softDeleted)
	require.Empty(t, st.deletedKeys, "файлы удаляет воркер, а не сервис")

	require.Len(t, pub.deletes, 1)
	require.Equal(t, domain.AvatarDeleteEvent{
		AvatarID: "a1",
		S3Keys:   []string{"avatars/a1/original.png", "thumbnails/a1/100x100.jpg"},
	}, pub.deletes[0])
}

func TestDelete_NotFound(t *testing.T) {
	svc := newService(&fakeRepo{byID: map[string]*domain.Avatar{}}, &fakeStorage{}, &fakePublisher{})

	err := svc.Delete(context.Background(), "user-1", "missing")

	require.ErrorIs(t, err, domain.ErrNotFound)
}

func TestDeleteCurrent_ForbiddenForForeignUser(t *testing.T) {
	svc := newService(&fakeRepo{}, &fakeStorage{}, &fakePublisher{})

	err := svc.DeleteCurrent(context.Background(), "attacker", "victim")
	require.ErrorIs(t, err, domain.ErrForbidden)
}

func TestMetadata_OK(t *testing.T) {
	repo := &fakeRepo{byID: map[string]*domain.Avatar{
		"a1": {ID: "a1", UserID: "user-1", Status: domain.StatusReady},
	}}
	svc := newService(repo, &fakeStorage{}, &fakePublisher{})

	avatar, err := svc.Metadata(context.Background(), "a1")
	require.NoError(t, err)
	require.Equal(t, "a1", avatar.ID)
}

func TestMetadata_NotFound(t *testing.T) {
	svc := newService(&fakeRepo{byID: map[string]*domain.Avatar{}}, &fakeStorage{}, &fakePublisher{})

	_, err := svc.Metadata(context.Background(), "missing")
	require.ErrorIs(t, err, domain.ErrNotFound)
}

func TestListByUser_OK(t *testing.T) {
	svc := newService(&fakeRepo{byID: map[string]*domain.Avatar{}}, &fakeStorage{}, &fakePublisher{})

	avatars, err := svc.ListByUser(context.Background(), "user-1")
	require.NoError(t, err)
	require.Empty(t, avatars)
}

func TestHealth_Components(t *testing.T) {
	svc := newService(&fakeRepo{}, &fakeStorage{}, &fakePublisher{})

	components := svc.Health(context.Background())
	require.Equal(t, "ok", components["database"])
	require.Equal(t, "ok", components["storage"])
	require.Equal(t, "ok", components["broker"])
}

func TestHealth_BrokerDown(t *testing.T) {
	pub := &fakePublisher{pingErr: errors.New("connection refused")}
	svc := newService(&fakeRepo{}, &fakeStorage{}, pub)

	components := svc.Health(context.Background())
	require.Contains(t, components["broker"], "connection refused")
}

func TestHealth_WithoutPublisher(t *testing.T) {
	// Без брокера компонент просто не проверяется.
	svc := newService(&fakeRepo{}, &fakeStorage{}, nil)

	components := svc.Health(context.Background())
	require.Equal(t, "ok", components["database"])
	require.Equal(t, "ok", components["storage"])
	require.NotContains(t, components, "broker")
}

func TestGetAvatar_ServesOriginal(t *testing.T) {
	repo := &fakeRepo{byID: map[string]*domain.Avatar{
		"a1": {ID: "a1", UserID: "user-1", MimeType: "image/png", S3Key: "avatars/a1/original.png"},
	}}
	st := &fakeStorage{objects: map[string][]byte{"avatars/a1/original.png": png1x1}}
	svc := newService(repo, st, &fakePublisher{})

	rc, info, avatar, err := svc.GetAvatar(context.Background(), "a1", "original", "")
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()

	got, _ := io.ReadAll(rc)
	require.Equal(t, png1x1, got)
	require.Equal(t, "etag", info.ETag)
	require.Equal(t, "image/png", info.ContentType)
	require.Equal(t, "a1", avatar.ID)
}

func TestGetAvatar_ServesThumbnail(t *testing.T) {
	repo := &fakeRepo{byID: map[string]*domain.Avatar{
		"a1": {ID: "a1", UserID: "user-1", MimeType: "image/png", S3Key: "avatars/a1/original.png",
			Thumbnails: []domain.Thumbnail{
				{Size: "100x100", Key: "thumbnails/a1/100x100.jpg"},
			}},
	}}
	st := &fakeStorage{objects: map[string][]byte{
		"thumbnails/a1/100x100.jpg": {0xFF, 0xD8, 0xFF, 0xD9},
	}}
	svc := newService(repo, st, &fakePublisher{})

	rc, info, _, err := svc.GetAvatar(context.Background(), "a1", "100x100", "")
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()

	require.Equal(t, "image/jpeg", info.ContentType, "миниатюры всегда JPEG")
	got, _ := io.ReadAll(rc)
	require.Equal(t, []byte{0xFF, 0xD8, 0xFF, 0xD9}, got)
}

func TestGetAvatar_ThumbnailNotYetCreated(t *testing.T) {
	repo := &fakeRepo{byID: map[string]*domain.Avatar{
		"a1": {ID: "a1", UserID: "user-1", MimeType: "image/png", S3Key: "avatars/a1/original.png"},
	}}
	svc := newService(repo, &fakeStorage{}, &fakePublisher{})

	_, _, _, err := svc.GetAvatar(context.Background(), "a1", "100x100", "")
	require.ErrorIs(t, err, domain.ErrThumbnailNotFound)
}

func TestGetAvatar_UnknownSize(t *testing.T) {
	repo := &fakeRepo{byID: map[string]*domain.Avatar{
		"a1": {ID: "a1", UserID: "user-1", MimeType: "image/png", S3Key: "avatars/a1/original.png"},
	}}
	svc := newService(repo, &fakeStorage{}, &fakePublisher{})

	_, _, _, err := svc.GetAvatar(context.Background(), "a1", "999x999", "")
	require.ErrorIs(t, err, domain.ErrThumbnailNotFound)
}

func TestGetAvatar_FormatConversionNotSupportedForThumbnail(t *testing.T) {
	repo := &fakeRepo{byID: map[string]*domain.Avatar{
		"a1": {ID: "a1", UserID: "user-1", MimeType: "image/png", S3Key: "avatars/a1/original.png",
			Thumbnails: []domain.Thumbnail{{Size: "100x100", Key: "thumbnails/a1/100x100.jpg"}}},
	}}
	svc := newService(repo, &fakeStorage{}, &fakePublisher{})

	_, _, _, err := svc.GetAvatar(context.Background(), "a1", "100x100", "webp")
	require.ErrorIs(t, err, domain.ErrThumbnailNotFound,
		"миниатюры всегда JPEG, конвертация не поддерживается")
}

func TestGetAvatar_NotFoundWhenObjectMissing(t *testing.T) {
	repo := &fakeRepo{byID: map[string]*domain.Avatar{
		"a1": {ID: "a1", UserID: "user-1", MimeType: "image/png", S3Key: "avatars/a1/original.png"},
	}}
	svc := newService(repo, &fakeStorage{}, &fakePublisher{})

	_, _, _, err := svc.GetAvatar(context.Background(), "a1", "", "")
	require.ErrorIs(t, err, domain.ErrNotFound)
}
