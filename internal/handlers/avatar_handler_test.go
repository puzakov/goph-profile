package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"goph-profile/internal/domain"
	"goph-profile/internal/storage"
)

// fakeService — заглушка AvatarService для тестов HTTP-слоя.
type fakeService struct {
	uploadFn     func(_ context.Context, userID, fileName string, data io.Reader) (*domain.Avatar, error)
	getFn        func(_ context.Context, id, size, format string) (io.ReadCloser, storage.ObjectInfo, *domain.Avatar, error)
	metadataFn   func(_ context.Context, id string) (*domain.Avatar, error)
	listFn       func(_ context.Context, userID string) ([]domain.Avatar, error)
	deleteFn     func(_ context.Context, userID, id string) error
	deleteCurFn  func(_ context.Context, ownerID, userID string) error
	getCurrentFn func(_ context.Context, userID string) (io.ReadCloser, storage.ObjectInfo, *domain.Avatar, error)
	healthFn     func(ctx context.Context) map[string]string
}

func (f *fakeService) Upload(ctx context.Context, userID, fileName string, data io.Reader) (*domain.Avatar, error) {
	if f.uploadFn == nil {
		return nil, domain.ErrNotFound
	}
	return f.uploadFn(ctx, userID, fileName, data)
}
func (f *fakeService) GetAvatar(ctx context.Context, id, size, format string) (io.ReadCloser, storage.ObjectInfo, *domain.Avatar, error) {
	if f.getFn == nil {
		return nil, storage.ObjectInfo{}, nil, domain.ErrNotFound
	}
	return f.getFn(ctx, id, size, format)
}
func (f *fakeService) GetCurrentByUser(ctx context.Context, userID string) (io.ReadCloser, storage.ObjectInfo, *domain.Avatar, error) {
	if f.getCurrentFn == nil {
		return nil, storage.ObjectInfo{}, nil, domain.ErrNotFound
	}
	return f.getCurrentFn(ctx, userID)
}
func (f *fakeService) Metadata(ctx context.Context, id string) (*domain.Avatar, error) {
	if f.metadataFn == nil {
		return nil, domain.ErrNotFound
	}
	return f.metadataFn(ctx, id)
}
func (f *fakeService) ListByUser(ctx context.Context, userID string) ([]domain.Avatar, error) {
	if f.listFn == nil {
		return []domain.Avatar{}, nil
	}
	return f.listFn(ctx, userID)
}
func (f *fakeService) Delete(ctx context.Context, userID, id string) error {
	if f.deleteFn == nil {
		return nil
	}
	return f.deleteFn(ctx, userID, id)
}
func (f *fakeService) DeleteCurrent(ctx context.Context, ownerID, userID string) error {
	if f.deleteCurFn == nil {
		return nil
	}
	return f.deleteCurFn(ctx, ownerID, userID)
}
func (f *fakeService) Health(ctx context.Context) map[string]string {
	if f.healthFn == nil {
		return map[string]string{"database": "ok", "storage": "ok"}
	}
	return f.healthFn(ctx)
}

// newTestHandler создаёт обработчик с логгером, пишущим в никуда.
func newTestHandler(svc AvatarService) *AvatarHandler {
	return NewAvatarHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// testPNG — валидный PNG 1x1 (прозрачный).
var testPNG = decodeBase64("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")

func decodeBase64(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

func newUploadRequest(t *testing.T, userID, fieldName string, content []byte, contentType string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile(fieldName, "test."+extForContentType(contentType))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/avatars", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if userID != "" {
		req.Header.Set("X-User-ID", userID)
	}
	return req
}

func extForContentType(ct string) string {
	switch ct {
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpg"
	default:
		return "txt"
	}
}

func TestUpload_MissingUserID(t *testing.T) {
	h := newTestHandler(&fakeService{})
	req := newUploadRequest(t, "", "image", testPNG, "image/png")
	w := httptest.NewRecorder()

	h.Upload(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "X-User-ID") {
		t.Fatalf("body = %s, want error about X-User-ID", w.Body.String())
	}
}

func TestUpload_InvalidFormat(t *testing.T) {
	h := newTestHandler(&fakeService{
		uploadFn: func(_ context.Context, userID, fileName string, data io.Reader) (*domain.Avatar, error) {
			return nil, domain.ErrInvalidFormat
		},
	})
	req := newUploadRequest(t, "user-1", "image", []byte("not an image"), "text/plain")
	w := httptest.NewRecorder()

	h.Upload(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Invalid file format") {
		t.Fatalf("body = %s, want 'Invalid file format'", w.Body.String())
	}
}

func TestUpload_TooLarge(t *testing.T) {
	h := newTestHandler(&fakeService{})
	// 11 МБ — больше лимита 10 МБ.
	req := newUploadRequest(t, "user-1", "image", bytes.Repeat([]byte("a"), 11<<20), "image/png")
	w := httptest.NewRecorder()

	h.Upload(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", w.Code)
	}
	if !strings.Contains(w.Body.String(), "max_size") {
		t.Fatalf("body = %s, want max_size in body", w.Body.String())
	}
}

func TestUpload_Success(t *testing.T) {
	h := newTestHandler(&fakeService{
		uploadFn: func(_ context.Context, userID, fileName string, data io.Reader) (*domain.Avatar, error) {
			got, _ := io.ReadAll(data)
			if !bytes.Equal(got, testPNG) {
				t.Errorf("service received %d bytes, want %d", len(got), len(testPNG))
			}
			return &domain.Avatar{
				ID: "11111111-1111-1111-1111-111111111111", UserID: userID,
				FileName: fileName, MimeType: "image/png", SizeBytes: int64(len(got)),
				Status: domain.StatusReady,
			}, nil
		},
	})
	req := newUploadRequest(t, "user-1", "image", testPNG, "image/png")
	w := httptest.NewRecorder()

	h.Upload(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		`"id":"11111111-1111-1111-1111-111111111111"`,
		`"user_id":"user-1"`,
		`"url":"/api/v1/avatars/11111111-1111-1111-1111-111111111111"`,
		`"status":"ready"`,
		`"created_at"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body = %s, want to contain %s", body, want)
		}
	}
}

func TestUpload_FileFieldName(t *testing.T) {
	// Спецификация API использует поле "file" — оно тоже должно приниматься.
	var received string
	h := newTestHandler(&fakeService{
		uploadFn: func(_ context.Context, userID, fileName string, data io.Reader) (*domain.Avatar, error) {
			received = userID
			return &domain.Avatar{ID: "1", UserID: userID, Status: domain.StatusReady}, nil
		},
	})
	req := newUploadRequest(t, "user-1", "file", testPNG, "image/png")
	w := httptest.NewRecorder()

	h.Upload(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", w.Code, w.Body.String())
	}
	if received != "user-1" {
		t.Fatalf("service got user %q, want user-1", received)
	}
}

func TestGetAvatar_NotFound(t *testing.T) {
	h := newTestHandler(&fakeService{
		getFn: func(_ context.Context, id, size, format string) (io.ReadCloser, storage.ObjectInfo, *domain.Avatar, error) {
			return nil, storage.ObjectInfo{}, nil, domain.ErrNotFound
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/avatars/00000000-0000-0000-0000-000000000000", nil)
	w := httptest.NewRecorder()

	h.GetAvatar(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Avatar not found") {
		t.Fatalf("body = %s, want 'Avatar not found'", w.Body.String())
	}
}

func TestGetAvatar_SuccessHeaders(t *testing.T) {
	h := newTestHandler(&fakeService{
		getFn: func(_ context.Context, id, size, format string) (io.ReadCloser, storage.ObjectInfo, *domain.Avatar, error) {
			return io.NopCloser(bytes.NewReader(testPNG)),
				storage.ObjectInfo{ETag: "abc123", Size: int64(len(testPNG))},
				&domain.Avatar{ID: id, MimeType: "image/png"}, nil
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/avatars/1", nil)
	w := httptest.NewRecorder()

	h.GetAvatar(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "max-age=86400" {
		t.Errorf("Cache-Control = %q, want max-age=86400", cc)
	}
	if etag := w.Header().Get("ETag"); etag != `"abc123"` {
		t.Errorf("ETag = %q, want \"abc123\"", etag)
	}
	if got := w.Body.Bytes(); !bytes.Equal(got, testPNG) {
		t.Errorf("body = %d bytes, want %d", len(got), len(testPNG))
	}
}

func TestGetAvatar_IfNoneMatch(t *testing.T) {
	h := newTestHandler(&fakeService{
		getFn: func(_ context.Context, id, size, format string) (io.ReadCloser, storage.ObjectInfo, *domain.Avatar, error) {
			return io.NopCloser(bytes.NewReader(testPNG)),
				storage.ObjectInfo{ETag: "abc123", Size: int64(len(testPNG))},
				&domain.Avatar{ID: id, MimeType: "image/png"}, nil
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/avatars/1", nil)
	req.Header.Set("If-None-Match", `"abc123"`)
	w := httptest.NewRecorder()

	h.GetAvatar(w, req)

	if w.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Errorf("body should be empty on 304, got %d bytes", w.Body.Len())
	}
}

func TestGetAvatar_ThumbnailSizeParams(t *testing.T) {
	// Query-параметры size/format должны доходить до сервиса.
	var gotSize, gotFormat string
	h := newTestHandler(&fakeService{
		getFn: func(_ context.Context, id, size, format string) (io.ReadCloser, storage.ObjectInfo, *domain.Avatar, error) {
			gotSize, gotFormat = size, format
			return io.NopCloser(bytes.NewReader(testPNG)),
				storage.ObjectInfo{ETag: "e", Size: int64(len(testPNG)), ContentType: "image/jpeg"},
				&domain.Avatar{ID: id, MimeType: "image/jpeg"}, nil
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/avatars/1?size=100x100&format=jpeg", nil)
	w := httptest.NewRecorder()

	h.GetAvatar(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if gotSize != "100x100" || gotFormat != "jpeg" {
		t.Errorf("service got size=%q format=%q, want 100x100/jpeg", gotSize, gotFormat)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("Content-Type = %q, want image/jpeg", ct)
	}
}

func TestGetAvatar_ThumbnailNotFound(t *testing.T) {
	h := newTestHandler(&fakeService{
		getFn: func(_ context.Context, id, size, format string) (io.ReadCloser, storage.ObjectInfo, *domain.Avatar, error) {
			return nil, storage.ObjectInfo{}, nil, domain.ErrThumbnailNotFound
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/avatars/1?size=300x300", nil)
	w := httptest.NewRecorder()

	h.GetAvatar(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Thumbnail not found") {
		t.Fatalf("body = %s, want 'Thumbnail not found'", w.Body.String())
	}
}

func TestMetadata_OK(t *testing.T) {
	h := newTestHandler(&fakeService{
		metadataFn: func(_ context.Context, id string) (*domain.Avatar, error) {
			return &domain.Avatar{
				ID: "a1", UserID: "user-1", FileName: "photo.png", MimeType: "image/png",
				SizeBytes: 100, Width: 10, Height: 20,
				Thumbnails: []domain.Thumbnail{{Size: "100x100", Key: "thumbnails/a1/100x100.jpg"}},
				Status:     domain.StatusReady,
			}, nil
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/avatars/a1/metadata", nil)
	w := httptest.NewRecorder()

	h.Metadata(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		`"id":"a1"`, `"file_name":"photo.png"`, `"mime_type":"image/png"`,
		`"status":"ready"`,
		`"dimensions":{"height":20,"width":10}`,
		`"size":"100x100"`, `"url":"/api/v1/avatars/a1?size=100x100"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %s; body = %s", want, body)
		}
	}
}

func TestListByUser_OK(t *testing.T) {
	h := newTestHandler(&fakeService{
		listFn: func(_ context.Context, userID string) ([]domain.Avatar, error) {
			return []domain.Avatar{
				{ID: "a1", UserID: userID, Status: domain.StatusReady},
			}, nil
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/user-1/avatars", nil)
	w := httptest.NewRecorder()

	h.ListByUser(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"id":"a1"`) || !strings.Contains(body, `"status":"ready"`) {
		t.Errorf("body = %s, want avatar a1 with status ready", body)
	}
}

func TestDeleteAvatar_Forbidden(t *testing.T) {
	h := newTestHandler(&fakeService{
		deleteFn: func(_ context.Context, userID, id string) error {
			return domain.ErrForbidden
		},
	})
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/avatars/a1", nil)
	req.Header.Set("X-User-ID", "attacker")
	w := httptest.NewRecorder()

	h.DeleteAvatar(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if !strings.Contains(w.Body.String(), "You can only delete your own avatars") {
		t.Errorf("body = %s, want 'You can only delete your own avatars'", w.Body.String())
	}
}

func TestDeleteAvatar_NoUserID(t *testing.T) {
	h := newTestHandler(&fakeService{})
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/avatars/a1", nil)
	w := httptest.NewRecorder()

	h.DeleteAvatar(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestDeleteAvatar_NoContent(t *testing.T) {
	h := newTestHandler(&fakeService{})
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/avatars/a1", nil)
	req.Header.Set("X-User-ID", "user-1")
	w := httptest.NewRecorder()

	h.DeleteAvatar(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %s", w.Code, w.Body.String())
	}
}

func TestDeleteByUser_ForbiddenForForeignUser(t *testing.T) {
	h := newTestHandler(&fakeService{
		deleteCurFn: func(_ context.Context, ownerID, userID string) error {
			return domain.ErrForbidden
		},
	})
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/users/victim/avatar", nil)
	req.Header.Set("X-User-ID", "attacker")
	w := httptest.NewRecorder()

	h.DeleteByUser(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestHealth_AllOK(t *testing.T) {
	h := newTestHandler(&fakeService{
		healthFn: func(ctx context.Context) map[string]string {
			return map[string]string{"database": "ok", "storage": "ok"}
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	h.Health(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"status":"ok"`) {
		t.Errorf("body = %s, want status ok", w.Body.String())
	}
}

func TestHealth_Degraded(t *testing.T) {
	h := newTestHandler(&fakeService{
		healthFn: func(ctx context.Context) map[string]string {
			return map[string]string{"database": "ok", "storage": "unavailable"}
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	h.Health(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"status":"degraded"`) {
		t.Errorf("body = %s, want status degraded", w.Body.String())
	}
}
