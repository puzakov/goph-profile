// Package migrations применяет миграции схемы БД через goose
// с встроенными в бинарник SQL-файлами.
package migrations

import (
	"database/sql"
	"embed"

	"github.com/pressly/goose/v3"
)

//go:embed *.sql
var fs embed.FS

// Up применяет все неприменённые миграции к базе. Идемпотентна:
// goose пропускает версии, уже зафиксированные в goose_db_version.
func Up(db *sql.DB) error {
	goose.SetBaseFS(fs)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.Up(db, ".")
}
