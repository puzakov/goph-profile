package handlers

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"goph-profile/internal/domain"
	"goph-profile/internal/storage"
)

// maxUploadSize — максимальный размер загружаемого файла (10 МБ), совпадает с services.MaxUploadSize.
const maxUploadSize = 10 << 20

// AvatarHandler обрабатывает запросы REST API.
type AvatarHandler struct {
	svc AvatarService
}

// NewAvatarHandler создаёт обработчик REST API.
func NewAvatarHandler(svc AvatarService) *AvatarHandler {
	return &AvatarHandler{svc: svc}
}

// Upload обрабатывает POST /api/v1/avatars — загрузку аватарки.
func (h *AvatarHandler) Upload(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeError(w, http.StatusBadRequest, "X-User-ID header is required", nil)
		return
	}

	// Жёсткий лимит размера тела запроса.
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "File too large",
				map[string]int{"max_size": maxUploadSize})
			return
		}
		writeError(w, http.StatusBadRequest, "Invalid multipart form", err.Error())
		return
	}

	// Готовый фронтенд шлёт поле "image", спецификация API — "file".
	file, header, err := r.FormFile("image")
	if err != nil {
		file, header, err = r.FormFile("file")
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "Missing image file",
			"expected multipart field: image or file")
		return
	}
	defer func() { _ = file.Close() }()

	avatar, err := h.svc.Upload(r.Context(), userID, header.Filename, file)
	switch {
	case errors.Is(err, domain.ErrInvalidFormat):
		writeError(w, http.StatusBadRequest, "Invalid file format",
			"Supported formats: jpeg, png, webp")
		return
	case errors.Is(err, domain.ErrFileTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "File too large",
			map[string]int{"max_size": maxUploadSize})
		return
	case err != nil:
		writeInternalError(w, r, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         avatar.ID,
		"user_id":    avatar.UserID,
		"url":        avatarURL(avatar.ID),
		"status":     avatar.Status,
		"created_at": avatar.CreatedAt.Format(time.RFC3339),
	})
}

// GetAvatar обрабатывает GET /api/v1/avatars/{avatarID}.
func (h *AvatarHandler) GetAvatar(w http.ResponseWriter, r *http.Request) {
	rc, info, avatar, err := h.svc.GetAvatar(r.Context(),
		chi.URLParam(r, "avatarID"),
		r.URL.Query().Get("size"),
		r.URL.Query().Get("format"),
	)
	if err != nil {
		h.writeAvatarError(w, r, err)
		return
	}
	defer func() { _ = rc.Close() }()

	h.serveImage(w, r, rc, contentTypeOr(info.ContentType, avatar.MimeType), info)
}

// GetByUserAvatar обрабатывает GET /api/v1/users/{userID}/avatar — текущую аватарку пользователя.
func (h *AvatarHandler) GetByUserAvatar(w http.ResponseWriter, r *http.Request) {
	rc, info, avatar, err := h.svc.GetCurrentByUser(r.Context(), chi.URLParam(r, "userID"))
	if err != nil {
		h.writeAvatarError(w, r, err)
		return
	}
	defer func() { _ = rc.Close() }()

	h.serveImage(w, r, rc, contentTypeOr(info.ContentType, avatar.MimeType), info)
}

// Metadata обрабатывает GET /api/v1/avatars/{avatarID}/metadata.
func (h *AvatarHandler) Metadata(w http.ResponseWriter, r *http.Request) {
	avatar, err := h.svc.Metadata(r.Context(), chi.URLParam(r, "avatarID"))
	if err != nil {
		h.writeAvatarError(w, r, err)
		return
	}

	thumbnails := make([]map[string]string, 0, len(avatar.Thumbnails))
	for _, t := range avatar.Thumbnails {
		thumbnails = append(thumbnails, map[string]string{
			"size": t.Size,
			"url":  fmt.Sprintf("%s?size=%s", avatarURL(avatar.ID), t.Size),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":         avatar.ID,
		"user_id":    avatar.UserID,
		"file_name":  avatar.FileName,
		"mime_type":  avatar.MimeType,
		"size":       avatar.SizeBytes,
		"status":     avatar.Status,
		"dimensions": map[string]int{"width": avatar.Width, "height": avatar.Height},
		"thumbnails": thumbnails,
		"created_at": avatar.CreatedAt.Format(time.RFC3339),
		"updated_at": avatar.UpdatedAt.Format(time.RFC3339),
	})
}

// ListByUser обрабатывает GET /api/v1/users/{userID}/avatars — список аватарок пользователя.
func (h *AvatarHandler) ListByUser(w http.ResponseWriter, r *http.Request) {
	avatars, err := h.svc.ListByUser(r.Context(), chi.URLParam(r, "userID"))
	if err != nil {
		h.writeAvatarError(w, r, err)
		return
	}

	list := make([]map[string]any, 0, len(avatars))
	for _, a := range avatars {
		list = append(list, map[string]any{
			"id":         a.ID,
			"user_id":    a.UserID,
			"url":        avatarURL(a.ID),
			"status":     a.Status,
			"mime_type":  a.MimeType,
			"size":       a.SizeBytes,
			"created_at": a.CreatedAt.Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, list)
}

// DeleteAvatar обрабатывает DELETE /api/v1/avatars/{avatarID}.
func (h *AvatarHandler) DeleteAvatar(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeError(w, http.StatusBadRequest, "X-User-ID header is required", nil)
		return
	}

	if err := h.svc.Delete(r.Context(), userID, chi.URLParam(r, "avatarID")); err != nil {
		h.writeAvatarError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteByUser обрабатывает DELETE /api/v1/users/{userID}/avatar.
func (h *AvatarHandler) DeleteByUser(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeError(w, http.StatusBadRequest, "X-User-ID header is required", nil)
		return
	}

	if err := h.svc.DeleteCurrent(r.Context(), userID, chi.URLParam(r, "userID")); err != nil {
		h.writeAvatarError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// serveImage отдаёт файл аватарки с заголовками кэширования и поддержкой ETag.
func (h *AvatarHandler) serveImage(w http.ResponseWriter, r *http.Request, rc io.Reader, contentType string, info storage.ObjectInfo) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "max-age=86400")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))

	etag := `"` + info.ETag + `"`
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	if _, err := io.Copy(w, rc); err != nil {
		// Заголовки уже отправлены — остаётся только залогировать.
		slog.Error("stream avatar", "path", r.URL.Path, "error", err)
	}
}

// writeAvatarError преобразует ошибки предметной области в HTTP-ответы.
func (h *AvatarHandler) writeAvatarError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, http.StatusNotFound, "Avatar not found", nil)
	case errors.Is(err, domain.ErrThumbnailNotFound):
		writeError(w, http.StatusNotFound, "Thumbnail not found", nil)
	case errors.Is(err, domain.ErrForbidden):
		writeError(w, http.StatusForbidden, "Forbidden",
			"You can only delete your own avatars")
	default:
		writeInternalError(w, r, err)
	}
}

// avatarURL формирует URL аватарки.
func avatarURL(id string) string {
	return "/api/v1/avatars/" + id
}

// contentTypeOr возвращает приоритетный content-type или запасной, если первый пуст.
func contentTypeOr(primary, fallback string) string {
	if primary != "" {
		return primary
	}
	return fallback
}
