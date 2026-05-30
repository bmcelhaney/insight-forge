package db

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/bmcelhaney/insight-forge/internal/models"
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

// === Insight Forge specific helpers (prototype) ===

func (d *DB) StoreSnapshots(ctx context.Context, snaps []models.DataSnapshot) error {
	// Simplified bulk insert for prototype
	for _, s := range snaps {
		_, err := d.ExecContext(ctx, `
			INSERT INTO data_snapshots (entity_id, source_code, snapshot_at, raw_response, quality_score, is_outlier, created_by)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, s.EntityID, s.SourceCode, s.SnapshotAt, toJSON(s.RawResponse), s.QualityScore, s.IsOutlier, s.CreatedBy)
		if err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) StoreResult(ctx context.Context, r models.InsightResult) error {
	_, err := d.ExecContext(ctx, `
		INSERT INTO processed_results (entity_id, viability_score, risk_score, summary, flags, based_on_snapshot_ids, generated_at, generated_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, r.EntityID, r.ViabilityScore, r.RiskScore, r.Summary, toJSON(r.Flags), toJSON(r.BasedOnSnapshotIDs), r.GeneratedAt, r.GeneratedBy)
	return err
}

func (d *DB) GetLatestResult(ctx context.Context, entityID string) (models.InsightResult, error) {
	var r models.InsightResult
	err := d.QueryRowContext(ctx, `
		SELECT entity_id, viability_score, risk_score, summary, generated_at 
		FROM processed_results 
		WHERE entity_id = ? 
		ORDER BY generated_at DESC 
		LIMIT 1
	`, entityID).Scan(&r.EntityID, &r.ViabilityScore, &r.RiskScore, &r.Summary, &r.GeneratedAt)
	return r, err
}

func (d *DB) GetSnapshots(ctx context.Context, entityID string) ([]models.DataSnapshot, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT entity_id, source_code, snapshot_at, raw_response, quality_score 
		FROM data_snapshots 
		WHERE entity_id = ? 
		ORDER BY snapshot_at DESC
	`, entityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.DataSnapshot
	for rows.Next() {
		var s models.DataSnapshot
		var raw []byte
		if err := rows.Scan(&s.EntityID, &s.SourceCode, &s.SnapshotAt, &raw, &s.QualityScore); err != nil {
			continue
		}
		json.Unmarshal(raw, &s.RawResponse)
		out = append(out, s)
	}
	return out, nil
}

// GetRecentAnalyses returns the most recent NSNs that have been analyzed.
func (d *DB) GetRecentAnalyses(ctx context.Context, limit int) ([]string, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT DISTINCT entity_id 
		FROM processed_results 
		ORDER BY generated_at DESC 
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nsns []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			nsns = append(nsns, id)
		}
	}
	return nsns, nil
}

func toJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
