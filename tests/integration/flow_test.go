//go:build integration

// Интеграционные тесты полного сценария: HTTP-сервер + PostgreSQL + MinIO +
// RabbitMQ + воркер. Окружение поднимается testcontainers-go.
// Запуск: make test-integration (или go test -tags=integration ./tests/integration/).
package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	pg "github.com/testcontainers/testcontainers-go/modules/postgres"
	rmq "github.com/testcontainers/testcontainers-go/modules/rabbitmq"
	"github.com/testcontainers/testcontainers-go/wait"

	"goph-profile/internal/handlers"
	"goph-profile/internal/messaging"
	"goph-profile/internal/repository"
	"goph-profile/internal/services"
	"goph-profile/internal/storage"
	"goph-profile/internal/worker"
)

// minio модуль: импортируем без алиаса пакет модуля.
const (
	minioImage  = "minio/minio:latest"
	rabbitImage = "rabbitmq:3.13-management-alpine"
	pgImage     = "postgres:16-alpine"
)

type FlowSuite struct {
	suite.Suite

	pgC       *pg.PostgresContainer
	rmqC      *rmq.RabbitMQContainer
	minioC    testcontainers.Container
	pool      *pgxpool.Pool
	storage   storage.AvatarStorage
	publisher *messaging.RabbitPublisher
	server    *httptest.Server
	cancel    context.CancelFunc
}

func TestFlowSuite(t *testing.T) {
	suite.Run(t, new(FlowSuite))
}

func (s *FlowSuite) SetupSuite() {
	ctx := context.Background()
	t := s.T()

	// --- PostgreSQL ---
	pgC, err := pg.Run(ctx, pgImage,
		pg.WithUsername("goph"), pg.WithPassword("goph"), pg.WithDatabase("goph"))
	require.NoError(t, err)
	s.pgC = pgC

	dsn, err := pgC.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	s.pool = pool

	// PostgreSQL может перезапускать процесс сразу после initdb —
	// ждём устойчивого соединения, прежде чем применять миграции.
	require.NoError(t, waitForPostgres(ctx, pool, 60*time.Second))
	require.NoError(t, repository.Migrate(ctx, pool, "../../migrations"))

	// --- RabbitMQ ---
	rmqC, err := rmq.Run(ctx, rabbitImage)
	require.NoError(t, err)
	s.rmqC = rmqC
	amqpURL, err := rmqC.AmqpURL(ctx)
	require.NoError(t, err)

	// --- MinIO ---
	req := testcontainers.ContainerRequest{
		Image:        minioImage,
		ExposedPorts: []string{"9000/tcp"},
		Env: map[string]string{
			"MINIO_ROOT_USER":     "minioadmin",
			"MINIO_ROOT_PASSWORD": "minioadmin",
		},
		Cmd: []string{"server", "/data"},
		WaitingFor: wait.ForHTTP("/minio/health/live").
			WithPort("9000/tcp").
			WithStartupTimeout(60 * time.Second),
	}
	minioC, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err)
	s.minioC = minioC

	host, err := minioC.Host(ctx)
	require.NoError(t, err)
	port, err := minioC.MappedPort(ctx, "9000")
	require.NoError(t, err)
	endpoint := host + ":" + port.Port()

	st, err := storage.NewMinioStorage(endpoint, "minioadmin", "minioadmin",
		"avatars", "us-east-1", false)
	require.NoError(t, err)
	require.NoError(t, st.EnsureBucket(ctx))
	s.storage = st

	// --- Publisher + воркер ---
	publisher, err := messaging.NewRabbitPublisher(amqpURL)
	require.NoError(t, err)
	s.publisher = publisher

	workerCtx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	w := worker.NewWorker(repository.NewAvatarRepository(pool), st, publisher, log)
	go func() {
		if err := w.Run(workerCtx, amqpURL); err != nil {
			log.Error("worker stopped with error", "error", err)
		}
	}()

	// --- HTTP-сервер ---
	svc := services.NewAvatarService(repository.NewAvatarRepository(pool), st, publisher, log)
	s.server = httptest.NewServer(handlers.NewRouter(svc, "../../web/static", log))
}

func (s *FlowSuite) TearDownSuite() {
	ctx := context.Background()
	if s.cancel != nil {
		s.cancel()
	}
	if s.server != nil {
		s.server.Close()
	}
	if s.publisher != nil {
		_ = s.publisher.Close()
	}
	if s.pool != nil {
		s.pool.Close()
	}
	if s.pgC != nil {
		_ = s.pgC.Terminate(ctx)
	}
	if s.rmqC != nil {
		_ = s.rmqC.Terminate(ctx)
	}
	if s.minioC != nil {
		_ = s.minioC.Terminate(ctx)
	}
}

// ---- вспомогательные функции ----

// waitForPostgres опрашивает БД, пока соединение не станет стабильным.
func waitForPostgres(ctx context.Context, pool *pgxpool.Pool, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		lastErr = pool.Ping(pingCtx)
		cancel()
		if lastErr == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("postgres not ready: %w", lastErr)
}

var png1x1 = decodePNG()

func decodePNG() []byte {
	b, err := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	if err != nil {
		panic(err)
	}
	return b
}

// uploadAvatar загружает PNG от имени userID и возвращает id аватарки.
func (s *FlowSuite) uploadAvatar(userID string) string {
	t := s.T()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("image", "avatar.png")
	require.NoError(t, err)
	_, err = fw.Write(png1x1)
	require.NoError(t, err)
	require.NoError(t, mw.Close())

	req, err := http.NewRequest(http.MethodPost, s.server.URL+"/api/v1/avatars", &body)
	require.NoError(t, err)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-User-ID", userID)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var payload struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))
	require.Equal(t, "pending", payload.Status,
		"сразу после загрузки аватарка ждёт обработки")
	return payload.ID
}

// avatarStatus возвращает processing_status аватарки.
func (s *FlowSuite) avatarStatus(id string) string {
	t := s.T()
	resp, err := http.Get(s.server.URL + "/api/v1/avatars/" + id + "/metadata")
	require.NoError(t, err)
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "not_found"
	}
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var payload struct {
		Status string `json:"status"`
		ID     string `json:"id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))
	require.Equal(t, id, payload.ID)
	return payload.Status
}

// waitFor опрашивает condition до наступления или до таймаута.
func (s *FlowSuite) waitFor(description string, timeout time.Duration, condition func() bool) {
	t := s.T()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", description)
}

// ---- тесты ----

func (s *FlowSuite) TestFullFlow_UploadProcessServeDelete() {
	t := s.T()
	ctx := context.Background()

	avatarID := s.uploadAvatar("user-1")

	// Воркер должен обработать аватарку асинхронно.
	s.waitFor("воркер обработал аватарку", 30*time.Second, func() bool {
		return s.avatarStatus(avatarID) == "ready"
	})

	// Миниатюра 100x100: JPEG, размер 100x100.
	resp, err := http.Get(s.server.URL + "/api/v1/avatars/" + avatarID + "?size=100x100")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "image/jpeg", resp.Header.Get("Content-Type"))
	thumbData, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.True(t, bytes.HasPrefix(thumbData, []byte{0xFF, 0xD8}), "миниатюра должна быть JPEG")
	thumbImg, _, err := image.Decode(bytes.NewReader(thumbData))
	require.NoError(t, err)
	require.Equal(t, 100, thumbImg.Bounds().Dx())
	require.Equal(t, 100, thumbImg.Bounds().Dy())

	// Миниатюра 300x300.
	resp300, err := http.Get(s.server.URL + "/api/v1/avatars/" + avatarID + "?size=300x300")
	require.NoError(t, err)
	defer resp300.Body.Close()
	require.Equal(t, http.StatusOK, resp300.StatusCode)
	require.Equal(t, "image/jpeg", resp300.Header.Get("Content-Type"))

	// Оригинал: тот же PNG с заголовками кэширования.
	respOrig, err := http.Get(s.server.URL + "/api/v1/avatars/" + avatarID)
	require.NoError(t, err)
	defer respOrig.Body.Close()
	require.Equal(t, http.StatusOK, respOrig.StatusCode)
	require.Equal(t, "image/png", respOrig.Header.Get("Content-Type"))
	require.Equal(t, "max-age=86400", respOrig.Header.Get("Cache-Control"))
	origData, err := io.ReadAll(respOrig.Body)
	require.NoError(t, err)
	require.Equal(t, png1x1, origData)

	// Метаданные содержат две миниатюры.
	respMeta, err := http.Get(s.server.URL + "/api/v1/avatars/" + avatarID + "/metadata")
	require.NoError(t, err)
	defer respMeta.Body.Close()
	var meta struct {
		Thumbnails []struct {
			Size string `json:"size"`
			URL  string `json:"url"`
		} `json:"thumbnails"`
	}
	require.NoError(t, json.NewDecoder(respMeta.Body).Decode(&meta))
	require.Len(t, meta.Thumbnails, 2)

	// Удаление: 204, после чего 404 и файлы уходят из S3.
	req, err := http.NewRequest(http.MethodDelete, s.server.URL+"/api/v1/avatars/"+avatarID, nil)
	require.NoError(t, err)
	req.Header.Set("X-User-ID", "user-1")
	respDel, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	respDel.Body.Close()
	require.Equal(t, http.StatusNoContent, respDel.StatusCode)

	respAfter, err := http.Get(s.server.URL + "/api/v1/avatars/" + avatarID)
	require.NoError(t, err)
	respAfter.Body.Close()
	require.Equal(t, http.StatusNotFound, respAfter.StatusCode)

	// Воркер удаляет файлы из MinIO асинхронно.
	origKey := "avatars/" + avatarID + "/original.png"
	s.waitFor("файлы удалены из S3", 30*time.Second, func() bool {
		rc, _, err := s.storage.Get(ctx, origKey)
		if err == nil {
			_ = rc.Close()
		}
		return err != nil
	})
}

func (s *FlowSuite) TestDelete_ForeignAvatarForbidden() {
	t := s.T()

	avatarID := s.uploadAvatar("owner")
	s.waitFor("воркер обработал аватарку", 30*time.Second, func() bool {
		return s.avatarStatus(avatarID) == "ready"
	})

	req, err := http.NewRequest(http.MethodDelete, s.server.URL+"/api/v1/avatars/"+avatarID, nil)
	require.NoError(t, err)
	req.Header.Set("X-User-ID", "attacker")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	// Аватарка на месте.
	require.Equal(t, "ready", s.avatarStatus(avatarID))
}

func (s *FlowSuite) TestUpload_RejectsInvalidFormat() {
	t := s.T()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("image", "file.txt")
	require.NoError(t, err)
	_, err = fw.Write([]byte("not an image"))
	require.NoError(t, err)
	require.NoError(t, mw.Close())

	req, err := http.NewRequest(http.MethodPost, s.server.URL+"/api/v1/avatars", &body)
	require.NoError(t, err)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-User-ID", "user-1")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var payload map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))
	require.Equal(t, "Invalid file format", payload["error"])
}

func (s *FlowSuite) TestHealth_OK() {
	t := s.T()
	resp, err := http.Get(s.server.URL + "/health")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var payload struct {
		Status     string            `json:"status"`
		Components map[string]string `json:"components"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))
	require.Equal(t, "ok", payload.Status)
	require.Equal(t, "ok", payload.Components["database"])
	require.Equal(t, "ok", payload.Components["storage"])
}
