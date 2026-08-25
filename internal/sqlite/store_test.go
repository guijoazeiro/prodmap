package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/errs"
)

func TestOpenConfiguresDatabaseAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "prodmap.db")
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	assertPragma(t, store, "foreign_keys", "1")
	assertPragma(t, store, "journal_mode", "wal")
	assertPragma(t, store, "busy_timeout", "5000")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("database permissions = %o, want 600", info.Mode().Perm())
	}
	status, err := store.MigrationStatus(context.Background())
	if err != nil {
		t.Fatalf("MigrationStatus() error = %v", err)
	}
	if status.AppliedVersion != 3 || status.Available != 3 || !status.Current {
		t.Fatalf("MigrationStatus() = %+v", status)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	store, err = Open(context.Background(), path)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	defer store.Close()
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if count != 3 {
		t.Fatalf("migration count = %d, want 3", count)
	}
}

func TestOpenRejectsInvalidPathAndCancelledContext(t *testing.T) {
	if _, err := Open(context.Background(), " "); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("Open(empty) error = %v, want ErrInvalid", err)
	}
	if _, err := Open(context.Background(), t.TempDir()); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("Open(directory) error = %v, want ErrInvalid", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Open(ctx, filepath.Join(t.TempDir(), "cancelled.db")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Open(cancelled) error = %v, want context.Canceled", err)
	}
}

func TestOpenSafelyEncodesURIReservedAndUnicodePathCharacters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory with spaces ? # % café", "prodmap ?#% 日本.db")
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open(reserved path) error = %v", err)
	}
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	if _, err := store.SaveRuntimeSnapshot(context.Background(), exactSnapshot(now)); err != nil {
		store.Close()
		t.Fatalf("SaveRuntimeSnapshot(reserved path) error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database was not created at the exact supplied path: %v", err)
	}

	reopened, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen reserved path: %v", err)
	}
	defer reopened.Close()
	status, err := reopened.Status(context.Background())
	if err != nil || status.RuntimeInstances != 1 || status.Services != 1 {
		t.Fatalf("reopened status = %+v, err = %v", status, err)
	}
}

func TestReplacementConnectionsPreservePragmasAndImmediateTransactions(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "prodmap.db")
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	for _, store := range []*Store{first, second} {
		forceReplacementConnection(t, store)
		assertPragma(t, store, "foreign_keys", "1")
		assertPragma(t, store, "busy_timeout", "5000")
		assertPragma(t, store, "journal_mode", "wal")
		if got := store.db.Stats().MaxOpenConnections; got != 1 {
			t.Fatalf("MaxOpenConnections = %d, want 1", got)
		}
	}

	lock, err := first.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin first immediate transaction: %v", err)
	}
	if _, err := second.db.ExecContext(ctx, "PRAGMA busy_timeout = 0"); err != nil {
		lock.Rollback()
		t.Fatal(err)
	}
	blocked, beginErr := second.db.BeginTx(ctx, nil)
	if blocked != nil {
		_ = blocked.Rollback()
	}
	if !isSQLiteBusy(beginErr) {
		lock.Rollback()
		t.Fatalf("second BeginTx while writer lock held = %v, want SQLITE_BUSY from BEGIN IMMEDIATE", beginErr)
	}
	if _, err := second.db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		lock.Rollback()
		t.Fatal(err)
	}
	if err := lock.Rollback(); err != nil {
		t.Fatal(err)
	}

	verification, err := second.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("replacement connection did not recover after cancellation: %v", err)
	}
	if err := verification.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestCancelledContextWaitingForWriteLockLeavesNoPartialSnapshot(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "prodmap.db")
	locker, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Close()
	writer, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	lock, err := locker.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, saveErr := writer.SaveRuntimeSnapshot(waitCtx, exactSnapshot(time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)))
	if !errors.Is(saveErr, context.DeadlineExceeded) {
		lock.Rollback()
		t.Fatalf("SaveRuntimeSnapshot while lock held = %v, want context deadline", saveErr)
	}
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		lock.Rollback()
		t.Fatalf("cancelled lock wait returned after %s, want prompt deadline propagation", elapsed)
	}
	if err := lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertPragma(t, writer, "busy_timeout", "5000")

	for _, table := range []string{"sources", "repositories", "commits", "services", "artifacts", "runtime_instances", "correlations", "evidence"} {
		var count int
		if err := writer.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("cancelled save persisted %d rows in %s", count, table)
		}
	}
}

func forceReplacementConnection(t *testing.T, store *Store) {
	t.Helper()
	store.db.SetMaxIdleConns(0)
	store.db.SetMaxIdleConns(1)
	if err := store.db.PingContext(context.Background()); err != nil {
		t.Fatalf("open replacement connection: %v", err)
	}
}

func assertPragma(t *testing.T, store *Store, pragma, want string) {
	t.Helper()
	var got string
	if err := store.db.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil {
		t.Fatalf("PRAGMA %s: %v", pragma, err)
	}
	if got != want {
		t.Fatalf("PRAGMA %s = %q, want %q", pragma, got, want)
	}
}
