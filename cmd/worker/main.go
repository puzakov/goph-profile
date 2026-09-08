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
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"goph-profile/internal/config"
	"goph-profile/internal/db/migrations"
	"goph-profile/internal/messaging"
	"goph-profile/internal/repository"
	"goph-profile/internal/storage"
	"goph-profile/internal/worker"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(log)

	cfg, err := config.Load()
	if err != nil {
		log.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	if err := run(log, cfg); err != nil {
		log.Error("worker stopped", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, cfg *config.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// PostgreSQL.
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer pool.Close()

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

	w := worker.NewWorker(repository.NewAvatarRepository(pool, log), st, publisher, log)
	log.Info("worker started", "rabbitmq", cfg.RabbitMQURL)

	// Run блокируется до отмены контекста или фатальной ошибки консьюмера.
	return w.Run(ctx, cfg.RabbitMQURL)
}
