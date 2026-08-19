package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/guijoazeiro/prodmap/internal/errs"
)

//go:embed migrations/*.sql
var embeddedMigrations embed.FS

var migrationFilename = regexp.MustCompile(`^(\d+)_([^/]+)\.sql$`)

type migration struct {
	version  int64
	name     string
	checksum string
	sql      string
}

// MigrationStatus describes the database schema migration state.
type MigrationStatus struct {
	AppliedVersion int64
	Available      int
	Current        bool
}

// MigrationStatus reports the highest applied version and embedded migration count.
func (s *Store) MigrationStatus(ctx context.Context) (MigrationStatus, error) {
	migrations, err := loadMigrations(embeddedMigrations)
	if err != nil {
		return MigrationStatus{}, err
	}
	var applied int64
	if err := s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&applied); err != nil {
		return MigrationStatus{}, fmt.Errorf("%w: read migration status: %w", errs.ErrUnavailable, err)
	}
	return MigrationStatus{
		AppliedVersion: applied,
		Available:      len(migrations),
		Current:        len(migrations) == 0 || applied == migrations[len(migrations)-1].version,
	}, nil
}

func loadMigrations(source fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(source, "migrations")
	if err != nil {
		return nil, fmt.Errorf("%w: read migrations: %w", errs.ErrIncompatible, err)
	}
	result := make([]migration, 0, len(entries))
	seen := make(map[int64]bool)
	for _, entry := range entries {
		if entry.IsDir() || path.Ext(entry.Name()) != ".sql" {
			continue
		}
		matches := migrationFilename.FindStringSubmatch(entry.Name())
		if matches == nil {
			return nil, fmt.Errorf("%w: invalid migration filename %q", errs.ErrIncompatible, entry.Name())
		}
		version, err := strconv.ParseInt(matches[1], 10, 64)
		if err != nil || version <= 0 || seen[version] {
			return nil, fmt.Errorf("%w: invalid or duplicate migration version in %q", errs.ErrIncompatible, entry.Name())
		}
		contents, err := fs.ReadFile(source, path.Join("migrations", entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("%w: read migration %q: %w", errs.ErrIncompatible, entry.Name(), err)
		}
		digest := sha256.Sum256(contents)
		result = append(result, migration{version: version, name: matches[2], checksum: hex.EncodeToString(digest[:]), sql: string(contents)})
		seen[version] = true
	}
	sort.Slice(result, func(i, j int) bool { return result[i].version < result[j].version })
	return result, nil
}

func applyMigrations(ctx context.Context, db *sql.DB, source fs.FS) error {
	migrations, err := loadMigrations(source)
	if err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("%w: acquire migration connection: %w", errs.ErrUnavailable, err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		return fmt.Errorf("%w: begin migration transaction: %w", errs.ErrUnavailable, err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	tableExists, err := schemaMigrationsExists(ctx, conn)
	if err != nil {
		return err
	}
	if tableExists {
		if err := validateApplied(ctx, conn, migrations); err != nil {
			return err
		}
	}

	for _, item := range migrations {
		var count int
		if tableExists {
			if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = ?", item.version).Scan(&count); err != nil {
				return fmt.Errorf("%w: inspect migration %d: %w", errs.ErrUnavailable, item.version, err)
			}
		}
		if count != 0 {
			continue
		}
		if _, err := conn.ExecContext(ctx, item.sql); err != nil {
			return fmt.Errorf("%w: apply migration %d (%s): %w", errs.ErrIncompatible, item.version, item.name, err)
		}
		tableExists = true
		if _, err := conn.ExecContext(ctx, `INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`, item.version, item.name, item.checksum, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("%w: record migration %d: %w", errs.ErrIncompatible, item.version, err)
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("%w: commit migrations: %w", errs.ErrUnavailable, err)
	}
	committed = true
	return nil
}

func schemaMigrationsExists(ctx context.Context, conn *sql.Conn) (bool, error) {
	var count int
	err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("%w: inspect migration table: %w", errs.ErrUnavailable, err)
	}
	return count != 0, nil
}

func validateApplied(ctx context.Context, conn *sql.Conn, available []migration) error {
	byVersion := make(map[int64]migration, len(available))
	for _, item := range available {
		byVersion[item.version] = item
	}
	rows, err := conn.QueryContext(ctx, "SELECT version, name, checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return fmt.Errorf("%w: read applied migrations: %w", errs.ErrUnavailable, err)
	}
	defer rows.Close()
	for rows.Next() {
		var version int64
		var name, checksum string
		if err := rows.Scan(&version, &name, &checksum); err != nil {
			return fmt.Errorf("%w: scan applied migration: %w", errs.ErrUnavailable, err)
		}
		item, ok := byVersion[version]
		if !ok {
			return fmt.Errorf("%w: applied migration %d is unavailable", errs.ErrIncompatible, version)
		}
		if item.name != name || item.checksum != checksum {
			return fmt.Errorf("%w: migration %d checksum or name differs", errs.ErrConflict, version)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%w: iterate applied migrations: %w", errs.ErrUnavailable, err)
	}
	return nil
}
