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

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/prometheus/client_golang/prometheus"

	"goph-profile/internal/config"
	"goph-profile/internal/db/migrations"
	"goph-profile/internal/handlers"
	"goph-profile/internal/messaging"
	"goph-profile/internal/metrics"
	"goph-profile/internal/repository"
	"goph-profile/internal/services"
	"goph-profile/internal/storage"
	"goph-profile/internal/telemetry"
)

// serviceName — имя сервиса в трейсах.
const serviceName = "avatar-server"

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
		log.Error("server stopped", "error", err)
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

	// Метрики процесса: состояние своего пула соединений. Объём данных в
	// хранилище отдаёт только воркер — значение не зависит от процесса,
	// а два источника одного gauge дали бы двойной счёт в sum().
	prometheus.MustRegister(metrics.NewPGXPoolCollector(pool))

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
