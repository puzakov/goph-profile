// Worker сервиса GophProfile: асинхронная обработка изображений.
//
// Читает события из RabbitMQ:
//   - avatar.uploaded  — инициирует конвейер обработки (pending -> processing);
//   - avatar.process    — создаёт миниатюры 100x100 и 300x300 в формате JPEG;
//   - avatar.deleted    — удаляет файлы аватарки из S3.
//
// Обработка идемпотентна: дубликаты сообщений отсеиваются по message_id
// (таблица event_dedup) и по статусу аватарки. Retryable-ошибки уходят
// в retry-очередь с экспоненциальной задержкой, после 5 попыток сообщение
// попадает в dead-letter, а аватарка помечается failed.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"goph-profile/internal/config"
	"goph-profile/internal/db/migrations"
	"goph-profile/internal/messaging"
	"goph-profile/internal/metrics"
	"goph-profile/internal/repository"
	"goph-profile/internal/storage"
	"goph-profile/internal/telemetry"
	"goph-profile/internal/worker"
)

// serviceName — имя сервиса в трейсах.
const serviceName = "avatar-worker"

func main() {
	// Настройки загружаются первыми: формат и уровень логов заданы в них,
	// а на ошибке конфигурации логгер ещё не сконфигурирован.
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	cfg, err := config.Load()
	if err != nil {
		log.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	configured, err := telemetry.NewLogger(os.Stdout, cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		log.Error("invalid logger configuration", "error", err)
		os.Exit(1)
	}
	log = configured
	slog.SetDefault(log)

	if err := run(log, cfg); err != nil {
		log.Error("worker stopped", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, cfg *config.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownTracer, err := telemetry.InitTracerProvider(ctx, cfg.OTLPEndpoint, serviceName)
	if err != nil {
		return fmt.Errorf("init tracer provider: %w", err)
	}
	defer func() {
		// Остановка провайдера сбрасывает буфер спанов в коллектор.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTracer(shutdownCtx); err != nil {
			log.Error("shutdown tracer provider", "error", err)
		}
	}()

	// PostgreSQL: каждый запрос — спан в текущем трейсе.
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("parse database url: %w", err)
	}
	// Имя SQL-спана усечено до операции (INSERT/UPDATE/...): параметры
	// и значения в трейс не попадают.
	poolCfg.ConnConfig.Tracer = otelpgx.NewTracer()
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer pool.Close()

	registerMetrics(pool, cfg, log)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		return fmt.Errorf("ping postgres: %w", err)
	}

	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() { _ = sqlDB.Close() }()
	if err := migrations.Up(sqlDB); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	// S3-хранилище (MinIO).
	st, err := storage.NewMinioStorage(
		cfg.Storage.Endpoint, cfg.Storage.AccessKey, cfg.Storage.SecretKey,
		cfg.Storage.Bucket, cfg.Storage.Region, cfg.Storage.UseSSL,
	)
	if err != nil {
		return fmt.Errorf("init storage: %w", err)
	}
	if err := st.EnsureBucket(ctx); err != nil {
		return fmt.Errorf("ensure bucket: %w", err)
	}

	// Брокер сообщений: публикация process-событий и ретраи.
	publisher, err := messaging.NewRabbitPublisher(cfg.RabbitMQURL)
	if err != nil {
		return fmt.Errorf("init rabbitmq publisher: %w", err)
	}
	defer func() { _ = publisher.Close() }()

	// /metrics на отдельном порту: Prometheus опрашивает его независимо
	// от обработки сообщений.
	metricsSrv := startMetricsServer(cfg.MetricsAddr, log)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
			log.Error("shutdown metrics server", "error", err)
		}
	}()

	w := worker.NewWorker(repository.NewAvatarRepository(pool, log), st, publisher, log)
	log.Info("worker started", "rabbitmq", cfg.RabbitMQURL)

	// Run блокируется до отмены контекста или фатальной ошибки консьюмера.
	return w.Run(ctx, cfg.RabbitMQURL)
}

// registerMetrics регистрирует коллекторы метрик воркера.
func registerMetrics(pool *pgxpool.Pool, cfg *config.Config, log *slog.Logger) {
	prometheus.MustRegister(
		metrics.NewPGXPoolCollector(pool),
		metrics.NewStorageCollector(pool, log),
	)
	// Глубина очередей: коллектор включается только при заданном адресе
	// Management API — иначе учётные данные брокера не нужны вовсе.
	if cfg.RabbitMQManagementURL != "" {
		prometheus.MustRegister(metrics.NewQueueCollector(
			cfg.RabbitMQManagementURL,
			cfg.RabbitMQManagementUser,
			cfg.RabbitMQManagementPassword,
			log,
		))
	}
}

// startMetricsServer поднимает HTTP-сервер с /metrics.
func startMetricsServer(addr string, log *slog.Logger) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics server stopped", "error", err, "addr", addr)
		}
	}()
	log.Info("metrics server started", "addr", addr)
	return srv
}
