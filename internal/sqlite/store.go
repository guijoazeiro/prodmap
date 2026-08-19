// Package sqlite provides Prodmap's local SQLite persistence foundation.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/guijoazeiro/prodmap/internal/errs"
	_ "modernc.org/sqlite"
)

const busyTimeoutMilliseconds = 5000

// Store is a configured SQLite database.
type Store struct {
	db                    *sql.DB
	now                   func() time.Time
	evidenceBatchObserver func(int)
}

// Open opens path, configures SQLite, and applies all embedded migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(path) == "" || path == ":memory:" {
		return nil, fmt.Errorf("%w: database path must name a file", errs.ErrInvalid)
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return nil, fmt.Errorf("%w: database path names a directory", errs.ErrInvalid)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, classifyOpenError("inspect database path", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, classifyOpenError("create database directory", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, classifyOpenError("open database", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	store := &Store{db: db, now: time.Now}
	closeOnError := func(err error) (*Store, error) {
		_ = db.Close()
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		return closeOnError(classifyOpenError("connect to database", err))
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return closeOnError(classifyOpenError("secure database permissions", err))
	}
	if err := configure(ctx, db); err != nil {
		return closeOnError(fmt.Errorf("%w: configure sqlite: %w", errs.ErrUnavailable, err))
	}
	if err := applyMigrations(ctx, db, embeddedMigrations); err != nil {
		return closeOnError(err)
	}
	return store, nil
}

func (s *Store) clockNow() time.Time {
	if s != nil && s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

func configure(ctx context.Context, db *sql.DB) error {
	for _, statement := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		fmt.Sprintf("PRAGMA busy_timeout = %d", busyTimeoutMilliseconds),
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func classifyOpenError(operation string, err error) error {
	category := errs.ErrUnavailable
	if errors.Is(err, os.ErrPermission) {
		category = errs.ErrUnauthorized
	}
	return fmt.Errorf("%w: %s: %w", category, operation, err)
}

// Close closes the underlying database.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
