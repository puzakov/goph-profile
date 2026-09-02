// Package domain содержит основные доменные сущности сервиса.
package domain

import (
	"errors"
	"time"
)

// Статусы обработки аватарки (processing_status в БД).
// На этапе 1 обработки ещё нет: после загрузки аватарка сразу готова (ready).
// На этапе 2 воркер будет переводить её pending -> processing -> ready.
const (
	StatusPending    = "pending"    // загружена, ожидает обработки
	StatusProcessing = "processing" // обрабатывается воркером
	StatusReady      = "ready"      // готова к выдаче
	StatusFailed     = "failed"     // обработка завершилась ошибкой
)

// Ошибки предметной области.
var (
	ErrNotFound          = errors.New("avatar not found")
	ErrForbidden         = errors.New("forbidden")
	ErrInvalidFormat     = errors.New("invalid file format")
	ErrFileTooLarge      = errors.New("file too large")
	ErrThumbnailNotFound = errors.New("thumbnail not found")
)

// SupportedMimeTypes — допустимые форматы изображений.
var SupportedMimeTypes = map[string]struct{}{
	"image/jpeg": {},
	"image/png":  {},
	"image/webp": {},
}

// Thumbnail — миниатюра аватарки (появляется на этапе 2).
type Thumbnail struct {
	Size string `json:"size"` // например "100x100"
	Key  string `json:"key"`  // ключ объекта в S3
}

// Avatar — доменная сущность аватарки.
type Avatar struct {
	ID         string
	UserID     string
	FileName   string
	MimeType   string
	SizeBytes  int64
	Width      int
	Height     int
	S3Key      string // ключ оригинального файла в S3
	Thumbnails []Thumbnail
	Status     string // processing_status
	CreatedAt  time.Time
	UpdatedAt  time.Time
	DeletedAt  *time.Time
}
