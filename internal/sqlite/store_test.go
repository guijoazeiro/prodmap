package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

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
	if status.AppliedVersion != 1 || status.Available != 1 || !status.Current {
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
	if count != 1 {
		t.Fatalf("migration count = %d, want 1", count)
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
