package cob

import (
	"database/sql"
	"embed"
)

// The single schema source: wrangler d1 migrations applies these files, and
// Migrate applies them for in-process databases (tests, future tools).
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate applies every migration in lexical order. Idempotent.
func Migrate(db *sql.DB) error {
	files, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	for _, f := range files {
		sqlText, err := migrationsFS.ReadFile("migrations/" + f.Name())
		if err != nil {
			return err
		}
		if _, err := db.Exec(string(sqlText)); err != nil {
			return err
		}
	}
	return nil
}
