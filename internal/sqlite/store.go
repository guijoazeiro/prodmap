// Package sqlite provides Prodmap's local SQLite persistence foundation.
package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/guijoazeiro/prodmap/internal/errs"
	_ "modernc.org/sqlite"
)

const (
	busyTimeoutMilliseconds     = 5000
	sqlitePrimaryResultCodeMask = 0xff
	sqliteBusyPrimaryResultCode = 5
)

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

	dsn, err := sqliteDSN(path)
	if err != nil {
		return nil, classifyOpenError("construct database connection", err)
	}
	db, err := sql.Open("sqlite", dsn)
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

// OpenReadOnly opens an existing, fully compatible database without changing
// its schema, permissions, journal mode, or logical contents.
func OpenReadOnly(ctx context.Context, path string) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(path) == "" || path == ":memory:" {
		return nil, fmt.Errorf("%w: database path must name an existing file", errs.ErrInvalid)
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: database does not exist", errs.ErrNotFound)
	}
	if err != nil {
		return nil, classifyOpenError("inspect database path", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: database path must name a regular file", errs.ErrInvalid)
	}

	dsn, err := sqliteReadOnlyDSN(path)
	if err != nil {
		return nil, classifyOpenError("construct read-only database connection", err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, classifyOpenError("open read-only database", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	closeOnError := func(err error) (*Store, error) {
		_ = db.Close()
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		return closeOnError(classifyOpenError("connect to read-only database", err))
	}
	if err := validateReadOnlySchema(ctx, db, embeddedMigrations); err != nil {
		return closeOnError(err)
	}
	return &Store{db: db, now: time.Now}, nil
}

func (s *Store) clockNow() time.Time {
	if s != nil && s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

func configure(ctx context.Context, db *sql.DB) error {
	// WAL is persistent database state. Connection-local foreign key enforcement,
	// busy timeout, and transaction lock mode are configured in sqliteDSN so they
	// are reapplied whenever database/sql opens a replacement connection.
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode = WAL"); err != nil {
		return err
	}
	return nil
}

func sqliteDSN(path string) (string, error) {
	return sqliteURI(path, func(query url.Values) {
		query.Set("_busy_timeout", strconv.Itoa(busyTimeoutMilliseconds))
		query.Set("_foreign_keys", "1")
		query.Set("_txlock", "immediate")
	})
}

func sqliteReadOnlyDSN(path string) (string, error) {
	return sqliteURI(path, func(query url.Values) {
		query.Set("mode", "ro")
		query.Set("_busy_timeout", strconv.Itoa(busyTimeoutMilliseconds))
		query.Set("_query_only", "1")
	})
}

func sqliteURI(path string, configure func(url.Values)) (string, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	uriPath := filepath.ToSlash(absolutePath)
	if volume := filepath.VolumeName(absolutePath); volume != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	query := url.Values{}
	configure(query)
	return (&url.URL{Scheme: "file", Path: uriPath, RawQuery: query.Encode()}).String(), nil
}

func (s *Store) beginTransaction(ctx context.Context) (*sql.Tx, func(), error) {
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		tx, err := s.db.BeginTx(ctx, nil)
		return tx, func() {}, err
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		if err := ctx.Err(); err != nil {
			return nil, func() {}, err
		}
		return nil, func() {}, context.DeadlineExceeded
	}
	timeoutMilliseconds := int((remaining + time.Millisecond - 1) / time.Millisecond)
	if timeoutMilliseconds >= busyTimeoutMilliseconds {
		tx, err := s.db.BeginTx(ctx, nil)
		return tx, func() {}, err
	}

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, func() {}, err
	}
	cleanup := func() {
		if _, resetErr := conn.ExecContext(context.Background(), "PRAGMA busy_timeout = "+strconv.Itoa(busyTimeoutMilliseconds)); resetErr != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		_ = conn.Close()
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout = "+strconv.Itoa(timeoutMilliseconds)); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		cleanup()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, func() {}, ctxErr
		}
		return nil, func() {}, err
	}
	return tx, cleanup, nil
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
