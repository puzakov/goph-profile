// Package repository реализует работу с PostgreSQL — хранение метаданных аватарок.
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"goph-profile/internal/domain"
)

// DBTX — подмножество методов пула pgx, используемое репозиторием.
// Реализуется *pgxpool.Pool в проде и pgxmock в тестах.
type DBTX interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Ping(ctx context.Context) error
}

// AvatarRepository — интерфейс хранилища метаданных аватарок.
type AvatarRepository interface {
	Create(ctx context.Context, a *domain.Avatar) error
	GetByID(ctx context.Context, id string) (*domain.Avatar, error)
	GetCurrentByUser(ctx context.Context, userID string) (*domain.Avatar, error)
	ListByUser(ctx context.Context, userID string) ([]domain.Avatar, error)
	SoftDelete(ctx context.Context, id string) error
	UpdateStatus(ctx context.Context, id, status string) error
	UpdateStatusAndThumbnails(ctx context.Context, id, status string, thumbnails []domain.Thumbnail) error
	// MarkEventProcessed фиксирует обработку события по message_id.
	// true — событие новое (обрабатываем), false — уже обрабатывалось (пропускаем).
	MarkEventProcessed(ctx context.Context, messageID string) (bool, error)
	Ping(ctx context.Context) error
}

type avatarRepo struct {
	db  DBTX
	log *slog.Logger
}

// NewAvatarRepository создаёт репозиторий поверх пула соединений pgx
// (в тестах вместо пула подойдёт pgxmock.PgxPoolIface) с явным логгером.
func NewAvatarRepository(db DBTX, log *slog.Logger) AvatarRepository {
	return &avatarRepo{db: db, log: log}
}

var _ DBTX = (*pgxpool.Pool)(nil)

const selectColumns = `id, user_id, file_name, mime_type, size_bytes,
	COALESCE(width, 0), COALESCE(height, 0),
	s3_key, thumbnail_s3_keys, processing_status, created_at, updated_at`

func (r *avatarRepo) Create(ctx context.Context, a *domain.Avatar) error {
	if a.Thumbnails == nil {
		a.Thumbnails = []domain.Thumbnail{}
	}
	thumbs, err := json.Marshal(a.Thumbnails)
	if err != nil {
		return fmt.Errorf("marshal thumbnails: %w", err)
	}
	err = r.db.QueryRow(ctx, `
		INSERT INTO avatars (id, user_id, file_name, mime_type, size_bytes, width, height,
			s3_key, thumbnail_s3_keys, upload_status, processing_status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'uploaded', $10)
		RETURNING created_at, updated_at`,
		a.ID, a.UserID, a.FileName, a.MimeType, a.SizeBytes, nullableInt(a.Width),
		nullableInt(a.Height), a.S3Key, thumbs, a.Status,
	).Scan(&a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert avatar: %w", err)
	}
	return nil
}

// nullableInt возвращает nil для 0 — в БД такие размеры хранятся как NULL.
func nullableInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

func (r *avatarRepo) GetByID(ctx context.Context, id string) (*domain.Avatar, error) {
	row := r.db.QueryRow(ctx, `
		SELECT `+selectColumns+`
		FROM avatars
		WHERE id = $1 AND deleted_at IS NULL`, id)
	return r.scanAvatar(row)
}

func (r *avatarRepo) GetCurrentByUser(ctx context.Context, userID string) (*domain.Avatar, error) {
	row := r.db.QueryRow(ctx, `
		SELECT `+selectColumns+`
		FROM avatars
		WHERE user_id = $1 AND deleted_at IS NULL
		ORDER BY created_at DESC
		LIMIT 1`, userID)
	return r.scanAvatar(row)
}

func (r *avatarRepo) ListByUser(ctx context.Context, userID string) ([]domain.Avatar, error) {
	rows, err := r.db.Query(ctx, `
		SELECT `+selectColumns+`
		FROM avatars
		WHERE user_id = $1 AND deleted_at IS NULL
		ORDER BY created_at DESC
		LIMIT 100`, userID)
	if err != nil {
		return nil, fmt.Errorf("query avatars: %w", err)
	}
	defer rows.Close()

	avatars := make([]domain.Avatar, 0)
	for rows.Next() {
		a, err := r.scanAvatar(rows)
		if err != nil {
			return nil, err
		}
		avatars = append(avatars, *a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate avatars: %w", err)
	}
	return avatars, nil
}

// SoftDelete помечает аватарку удалённой (deleted_at). Строка остаётся в БД,
// файл из S3 удаляет вызывающая сторона.
func (r *avatarRepo) SoftDelete(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE avatars
		SET deleted_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("soft delete avatar: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// UpdateStatus обновляет статус обработки аватарки.
func (r *avatarRepo) UpdateStatus(ctx context.Context, id, status string) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE avatars
		SET processing_status = $2, updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL`, id, status)
	if err != nil {
		return fmt.Errorf("update status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// UpdateStatusAndThumbnails обновляет статус и список миниатюр аватарки.
func (r *avatarRepo) UpdateStatusAndThumbnails(ctx context.Context, id, status string, thumbnails []domain.Thumbnail) error {
	thumbs, err := json.Marshal(thumbnails)
	if err != nil {
		return fmt.Errorf("marshal thumbnails: %w", err)
	}
	tag, err := r.db.Exec(ctx, `
		UPDATE avatars
		SET processing_status = $2, thumbnail_s3_keys = $3, updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL`, id, status, thumbs)
	if err != nil {
		return fmt.Errorf("update status and thumbnails: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// MarkEventProcessed идемпотентно фиксирует обработку события брокера.
func (r *avatarRepo) MarkEventProcessed(ctx context.Context, messageID string) (bool, error) {
	var inserted string
	err := r.db.QueryRow(ctx, `
		INSERT INTO event_dedup (message_id) VALUES ($1)
		ON CONFLICT (message_id) DO NOTHING
		RETURNING message_id`, messageID).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // событие уже обрабатывалось
	}
	if err != nil {
		return false, fmt.Errorf("mark event processed: %w", err)
	}
	return true, nil
}

func (r *avatarRepo) Ping(ctx context.Context) error {
	return r.db.Ping(ctx)
}

func (r *avatarRepo) scanAvatar(row pgx.Row) (*domain.Avatar, error) {
	var a domain.Avatar
	var thumbnails []byte
	if err := row.Scan(&a.ID, &a.UserID, &a.FileName, &a.MimeType, &a.SizeBytes,
		&a.Width, &a.Height, &a.S3Key, &thumbnails, &a.Status, &a.CreatedAt, &a.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("scan avatar: %w", err)
	}
	if len(thumbnails) > 0 {
		if err := json.Unmarshal(thumbnails, &a.Thumbnails); err != nil {
			r.log.Error("unmarshal thumbnail_s3_keys failed",
				"avatar_id", a.ID, "error", err)
		}
	}
	return &a, nil
}
