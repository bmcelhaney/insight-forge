package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	_ "github.com/marcboeker/go-duckdb"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type DB struct {
	*sql.DB
	path string
}

func New(path string) (*DB, error) {
	db, err := sql.Open("duckdb", path)
	if err != nil {
		return nil, fmt.Errorf("open duckdb at %s: %w", path, err)
	}

	// Recommended pragmas for analytical workload
	pragmas := []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA synchronous=NORMAL;",
		"PRAGMA memory_limit='4GB';",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("pragma failed (%s): %w", p, err)
		}
	}

	return &DB{DB: db, path: path}, nil
}

func (d *DB) Close() error {
	return d.DB.Close()
}

// Migrate runs all .sql files in migrations/ in lexical order.
func (d *DB) Migrate(ctx context.Context) error {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	for _, f := range files {
		content, err := fs.ReadFile(migrationsFS, "migrations/"+f)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", f, err)
		}

		// Split on semicolons for simple multi-statement support
		stmts := splitStatements(string(content))
		for _, stmt := range stmts {
			stmt = strings.TrimSpace(stmt)
			if stmt == "" {
				continue
			}
			if _, err := d.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("execute migration %s: %w", f, err)
			}
		}
	}
	return nil
}

func splitStatements(s string) []string {
	return strings.Split(s, ";")
}
