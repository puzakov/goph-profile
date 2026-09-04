// HTTP-сервер сервиса GophProfile: REST API + веб-интерфейс.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"goph-profile/internal/config"
	"goph-profile/internal/db/migrations"
	"goph-profile/internal/handlers"
	"goph-profile/internal/messaging"
	"goph-profile/internal/repository"
	"goph-profile/internal/services"
	"goph-profile/internal/storage"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(log)

	if err := run(log, config.Load()); err != nil {
		log.Error("server stopped", "error", err)
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

	// Брокер сообщений: публикация событий обработки для воркера.
	publisher, err := messaging.NewRabbitPublisher(cfg.RabbitMQURL)
	if err != nil {
		return fmt.Errorf("init rabbitmq publisher: %w", err)
	}
	defer func() { _ = publisher.Close() }()

	svc := services.NewAvatarService(repository.NewAvatarRepository(pool, log), st, publisher, log)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handlers.NewRouter(svc, cfg.StaticDir, log),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()
	log.Info("server started", "addr", cfg.HTTPAddr)

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		return nil
	}
}
