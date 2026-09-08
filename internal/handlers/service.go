// Package handlers содержит HTTP-обработчики REST API и веб-интерфейса.
package handlers

import (
	"context"
	"io"

	"goph-profile/internal/domain"
	"goph-profile/internal/storage"
)

// AvatarService — контракт бизнес-логики для HTTP-слоя.
// Выделен в интерфейс, чтобы подменять фейками в тестах.
type AvatarService interface {
	Upload(ctx context.Context, userID, fileName string, data io.Reader) (*domain.Avatar, error)
	GetAvatar(ctx context.Context, id, size, format string) (io.ReadCloser, storage.ObjectInfo, *domain.Avatar, error)
	GetCurrentByUser(ctx context.Context, userID string) (io.ReadCloser, storage.ObjectInfo, *domain.Avatar, error)
	Metadata(ctx context.Context, id string) (*domain.Avatar, error)
	ListByUser(ctx context.Context, userID string) ([]domain.Avatar, error)
	Delete(ctx context.Context, userID, id string) error
	DeleteCurrent(ctx context.Context, ownerID, userID string) error
	Health(ctx context.Context) map[string]string
}
