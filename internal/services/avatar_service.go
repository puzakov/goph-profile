// Package services содержит бизнес-логику сервиса аватарок.
package services

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"

	"github.com/google/uuid"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	// Регистрация декодеров форматов для image.DecodeConfig.
	_ "golang.org/x/image/webp"

	"goph-profile/internal/domain"
	"goph-profile/internal/messaging"
	"goph-profile/internal/repository"
	"goph-profile/internal/storage"
)

// MaxUploadSize — максимальный размер загружаемого файла (10 МБ).
const MaxUploadSize = 10 << 20

// Размеры миниатюр, создаваемых воркером.
const (
	ThumbSize100 = "100x100"
	ThumbSize300 = "300x300"
)

// Ключ оригинального файла в S3: avatars/{avatarID}/original.{ext}.
// Миниатюры воркер кладёт в thumbnails/{avatarID}/{size}.jpg.
const s3Prefix = "avatars"

var mimeToExt = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
}

var formatToMime = map[string]string{
	"jpeg": "image/jpeg",
	"png":  "image/png",
	"webp": "image/webp",
}

// AvatarService реализует бизнес-логику работы с аватарками.
type AvatarService struct {
	repo      repository.AvatarRepository
	storage   storage.AvatarStorage
	publisher messaging.EventPublisher // может быть nil: публикация пропускается
	log       *slog.Logger
}

// NewAvatarService создаёт сервис с заданными зависимостями.
func NewAvatarService(repo repository.AvatarRepository, st storage.AvatarStorage,
	publisher messaging.EventPublisher, log *slog.Logger) *AvatarService {
	if log == nil {
		log = slog.Default()
	}
	return &AvatarService{repo: repo, storage: st, publisher: publisher, log: log}
}

// Upload сохраняет аватарку: файл — в S3, метаданные — в PostgreSQL,
// затем публикует AvatarUploadEvent. Обработка миниатюр выполняется
// воркером асинхронно, поэтому статус аватарки — pending.
func (s *AvatarService) Upload(ctx context.Context, userID, fileName string, data io.Reader) (*domain.Avatar, error) {
	// Ограничиваем чтение, чтобы не съесть всю память: файл больше лимита считаем ошибкой.
	buf, err := io.ReadAll(io.LimitReader(data, MaxUploadSize+1))
	if err != nil {
		return nil, fmt.Errorf("read upload: %w", err)
	}
	if len(buf) > MaxUploadSize {
		return nil, domain.ErrFileTooLarge
	}

	mime := detectImageType(buf)
	if _, ok := domain.SupportedMimeTypes[mime]; !ok {
		return nil, domain.ErrInvalidFormat
	}

	width, height := decodeDimensions(buf)

	id := uuid.NewString()
	key := fmt.Sprintf("%s/%s/original%s", s3Prefix, id, mimeToExt[mime])
	if err := s.storage.Put(ctx, key, bytes.NewReader(buf), int64(len(buf)), mime); err != nil {
		return nil, fmt.Errorf("put object: %w", err)
	}

	avatar := &domain.Avatar{
		ID:        id,
		UserID:    userID,
		FileName:  filepath.Base(fileName),
		MimeType:  mime,
		SizeBytes: int64(len(buf)),
		Width:     width,
		Height:    height,
		S3Key:     key,
		Status:    domain.StatusPending,
	}
	if err := s.repo.Create(ctx, avatar); err != nil {
		// Не удалось сохранить метаданные — убираем файл, чтобы не мусорить в S3.
		_ = s.storage.Delete(ctx, key)
		return nil, fmt.Errorf("create avatar metadata: %w", err)
	}

	s.publishUpload(ctx, avatar)
	return avatar, nil
}

// publishUpload отправляет событие загрузки воркеру. Потеря события не
// ломает загрузку: аватарка останется в статусе pending.
func (s *AvatarService) publishUpload(ctx context.Context, avatar *domain.Avatar) {
	if s.publisher == nil {
		return
	}
	event := domain.AvatarUploadEvent{
		AvatarID: avatar.ID,
		UserID:   avatar.UserID,
		S3Key:    avatar.S3Key,
	}
	if err := s.publisher.PublishUpload(ctx, event); err != nil {
		s.log.Error("publish upload event failed",
			"avatar_id", avatar.ID, "error", err)
		return
	}
	s.log.Info("upload event published", "avatar_id", avatar.ID)
}

// GetAvatar возвращает файл аватарки по id.
// size: original (по умолчанию), 100x100 или 300x300 (миниатюры воркера).
// format: jpeg, png или webp — только для оригинала, миниатюры всегда JPEG.
func (s *AvatarService) GetAvatar(ctx context.Context, id, size, format string) (io.ReadCloser, storage.ObjectInfo, *domain.Avatar, error) {
	avatar, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, storage.ObjectInfo{}, nil, err
	}

	key, contentType := avatar.S3Key, avatar.MimeType
	switch size {
	case "", "original":
		if format != "" {
			mime, ok := formatToMime[format]
			if !ok || mime != avatar.MimeType {
				return nil, storage.ObjectInfo{}, nil, domain.ErrThumbnailNotFound
			}
			contentType = mime
		}
	case ThumbSize100, ThumbSize300:
		thumb := findThumbnail(avatar.Thumbnails, size)
		if thumb == nil {
			return nil, storage.ObjectInfo{}, nil, domain.ErrThumbnailNotFound
		}
		if format != "" && format != "jpeg" {
			return nil, storage.ObjectInfo{}, nil, domain.ErrThumbnailNotFound
		}
		key, contentType = thumb.Key, "image/jpeg"
	default:
		return nil, storage.ObjectInfo{}, nil, domain.ErrThumbnailNotFound
	}

	rc, info, err := s.storage.Get(ctx, key)
	if err != nil {
		return nil, storage.ObjectInfo{}, nil, mapStorageError(err)
	}
	info.ContentType = contentType
	return rc, info, avatar, nil
}

// GetCurrentByUser возвращает текущую аватарку пользователя (самую свежую).
func (s *AvatarService) GetCurrentByUser(ctx context.Context, userID string) (io.ReadCloser, storage.ObjectInfo, *domain.Avatar, error) {
	avatar, err := s.repo.GetCurrentByUser(ctx, userID)
	if err != nil {
		return nil, storage.ObjectInfo{}, nil, err
	}
	rc, info, err := s.storage.Get(ctx, avatar.S3Key)
	if err != nil {
		return nil, storage.ObjectInfo{}, nil, mapStorageError(err)
	}
	return rc, info, avatar, nil
}

// Metadata возвращает метаданные аватарки.
func (s *AvatarService) Metadata(ctx context.Context, id string) (*domain.Avatar, error) {
	return s.repo.GetByID(ctx, id)
}

// ListByUser возвращает список аватарок пользователя (без удалённых).
func (s *AvatarService) ListByUser(ctx context.Context, userID string) ([]domain.Avatar, error) {
	return s.repo.ListByUser(ctx, userID)
}

// Delete удаляет аватарку по id: мягко в БД и публикует AvatarDeleteEvent —
// файлы из S3 удалит воркер. Удалять можно только свою аватарку.
func (s *AvatarService) Delete(ctx context.Context, userID, id string) error {
	avatar, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if avatar.UserID != userID {
		return domain.ErrForbidden
	}
	if err := s.repo.SoftDelete(ctx, id); err != nil {
		return err
	}
	s.publishDelete(ctx, avatar)
	return nil
}

// DeleteCurrent удаляет текущую аватарку пользователя. ownerID — из заголовка
// X-User-ID, userID — из пути: удалять можно только свою аватарку.
func (s *AvatarService) DeleteCurrent(ctx context.Context, ownerID, userID string) error {
	if ownerID != userID {
		return domain.ErrForbidden
	}
	avatar, err := s.repo.GetCurrentByUser(ctx, userID)
	if err != nil {
		return err
	}
	if err := s.repo.SoftDelete(ctx, avatar.ID); err != nil {
		return err
	}
	s.publishDelete(ctx, avatar)
	return nil
}

// publishDelete отправляет воркеру событие удаления файлов из S3.
func (s *AvatarService) publishDelete(ctx context.Context, avatar *domain.Avatar) {
	if s.publisher == nil {
		return
	}
	keys := []string{avatar.S3Key}
	for _, t := range avatar.Thumbnails {
		keys = append(keys, t.Key)
	}
	event := domain.AvatarDeleteEvent{AvatarID: avatar.ID, S3Keys: keys}
	if err := s.publisher.PublishDelete(ctx, event); err != nil {
		s.log.Error("publish delete event failed",
			"avatar_id", avatar.ID, "error", err)
	}
}

// Health проверяет доступность компонентов системы.
func (s *AvatarService) Health(ctx context.Context) map[string]string {
	components := map[string]string{}
	if err := s.repo.Ping(ctx); err != nil {
		s.log.Error("health check failed", "component", "database", "error", err)
		components["database"] = "unavailable"
	} else {
		components["database"] = "ok"
	}
	if err := s.storage.Ping(ctx); err != nil {
		s.log.Error("health check failed", "component", "storage", "error", err)
		components["storage"] = "unavailable"
	} else {
		components["storage"] = "ok"
	}
	// Брокер — опциональный компонент: без publisher'a он не проверяется.
	if p, ok := s.publisher.(interface{ Ping(context.Context) error }); ok {
		if err := p.Ping(ctx); err != nil {
			s.log.Error("health check failed", "component", "broker", "error", err)
			components["broker"] = "unavailable"
		} else {
			components["broker"] = "ok"
		}
	}
	return components
}

func findThumbnail(thumbnails []domain.Thumbnail, size string) *domain.Thumbnail {
	for i := range thumbnails {
		if thumbnails[i].Size == size {
			return &thumbnails[i]
		}
	}
	return nil
}

// detectImageType определяет MIME-тип изображения по сигнатуре.
func detectImageType(buf []byte) string {
	// http.DetectContentType не распознаёт WebP на некоторых версиях Go,
	// поэтому проверяем сигнатуру RIFF/WEBP вручную.
	if len(buf) > 12 && bytes.Equal(buf[:4], []byte("RIFF")) && bytes.Equal(buf[8:12], []byte("WEBP")) {
		return "image/webp"
	}
	return http.DetectContentType(buf)
}

// decodeDimensions извлекает размеры изображения без полной декодировки.
func decodeDimensions(buf []byte) (width, height int) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(buf))
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

func mapStorageError(err error) error {
	if err == storage.ErrObjectNotFound {
		return domain.ErrNotFound
	}
	return err
}
