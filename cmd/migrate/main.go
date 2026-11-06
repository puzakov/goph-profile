// migrate применяет миграции схемы и завершается — отдельный шаг деплоя,
// не связанный с жизненным циклом server и worker.
//
// В Kubernetes запускается Job'ом (Helm-хук post-install/pre-upgrade):
// на обновлении схема меняется до выката новых подов, а не каждым подом.
// Доступ к БД перепроверяется с паузами: на первой установке Job идёт
// следом за созданием ресурсов релиза, когда PostgreSQL ещё инициализируется,
// поэтому первые попытки заведомо падают.
//
// Читается только DATABASE_URL: остальные обязательные переменные сервиса
// (S3, RabbitMQ) шагу миграций не нужны, и config.Load здесь не подходит.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // драйвер "pgx" для database/sql

	"goph-profile/internal/db/migrations"
)

const (
	// connectTimeout — предел ожидания одной попытки подключения.
	connectTimeout = 5 * time.Second
	// attemptInterval — пауза между попытками: PostgreSQL поднимается
	// десятки секунд, частые попытки только засоряют лог.
	attemptInterval = 3 * time.Second
	// maxAttempts даёт суммарное окно ожидания около двух минут.
	maxAttempts = 40
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Error("DATABASE_URL is not set")
		os.Exit(1)
	}

	if err := run(log, dsn); err != nil {
		log.Error("migrate failed", "error", err)
		os.Exit(1)
	}
	log.Info("migrations applied")
}

// run дожидается доступности БД и применяет миграции.
func run(log *slog.Logger, dsn string) error {
	db, err := connect(log, dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	if err := migrations.Up(db); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// connect открывает соединение с БД, повторяя попытки, пока сервер не ответит.
func connect(log *slog.Logger, dsn string) (*sql.DB, error) {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		db, err := sql.Open("pgx", dsn)
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
			err = db.PingContext(ctx)
			cancel()
			if err == nil {
				return db, nil
			}
			// Соединение не установлено — дескриптор не нужен.
			_ = db.Close()
		}
		lastErr = err

		if attempt < maxAttempts {
			log.Warn("database is not ready, retrying",
				"attempt", attempt, "interval", attemptInterval, "error", err)
			time.Sleep(attemptInterval)
		}
	}
	return nil, fmt.Errorf("database is unreachable after %d attempts: %w", maxAttempts, lastErr)
}
