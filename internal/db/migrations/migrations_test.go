package migrations

import (
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib" // регистрирует драйвер "pgx"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

// TestEmbeddedMigrations_ParseAndOrdered проверяет, что встроенные миграции
// разбираются goose и упорядочены по версиям (без подключения к БД).
func TestEmbeddedMigrations_ParseAndOrdered(t *testing.T) {
	// Ленивое соединение: ListSources не обращается к БД, поэтому
	// подойдёт sql.DB, не устанавливающая реальных подключений.
	db, err := sql.Open("pgx", "postgres://localhost/nonexistent")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	provider, err := goose.NewProvider(goose.DialectPostgres, db, fs)
	require.NoError(t, err)

	sources := provider.ListSources()
	require.Len(t, sources, 2)

	paths := make([]string, 0, len(sources))
	for _, s := range sources {
		paths = append(paths, s.Path)
	}
	require.Equal(t, []string{
		"00001_create_avatars.sql",
		"00002_create_event_dedup.sql",
	}, paths)
}
