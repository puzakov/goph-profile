package repository

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// migrationLockKey — произвольный ключ pg_advisory_lock, сериализующий миграции
// между процессами (server и worker могут стартовать одновременно).
const migrationLockKey = 747734731

// Migrate применяет SQL-миграции из каталога dir в порядке имён файлов.
// Прогресс отслеживается в таблице schema_migrations, так что миграции идемпотентны.
// Advisory lock защищает от гонки параллельного запуска из нескольких процессов.
func Migrate(ctx context.Context, pool *pgxpool.Pool, dir string) error {
	// pg_advisory_lock привязан к сессии — берём выделенное соединение
	// и держим его до завершения всех миграций.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection for migrations: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockKey); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		// Unlock без ctx: освобождаем блокировку даже при отмене контекста.
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrationLockKey)
	}()

	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(files)

	for _, file := range files {
		version := filepath.Base(file)
		applied, err := isApplied(ctx, pool, version)
		if err != nil {
			return err
		}
		if applied {
			continue
		}

		sql, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", version, err)
		}

		if err := applyMigration(ctx, pool, version, string(sql)); err != nil {
			return err
		}
	}
	return nil
}

func isApplied(ctx context.Context, pool *pgxpool.Pool, version string) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)`, version,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check migration %s: %w", version, err)
	}
	return exists, nil
}

func applyMigration(ctx context.Context, pool *pgxpool.Pool, version, sql string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx for %s: %w", version, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Simple protocol позволяет выполнять несколько SQL-операторов за раз.
	if _, err := tx.Exec(ctx, sql, pgx.QueryExecModeSimpleProtocol); err != nil {
		return fmt.Errorf("apply migration %s: %w", version, err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
		return fmt.Errorf("record migration %s: %w", version, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration %s: %w", version, err)
	}
	return nil
}
