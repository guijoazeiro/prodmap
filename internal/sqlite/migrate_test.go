package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/guijoazeiro/prodmap/internal/errs"
	_ "modernc.org/sqlite"
)

const foundationSQL = `CREATE TABLE schema_migrations (
version INTEGER PRIMARY KEY,
name TEXT NOT NULL,
checksum TEXT NOT NULL,
applied_at TEXT NOT NULL
);`

func TestMigrationMetadataHasChecksumAndUTCTimestamp(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var name, checksum, appliedAt string
	if err := store.db.QueryRow("SELECT name, checksum, applied_at FROM schema_migrations WHERE version = 1").Scan(&name, &checksum, &appliedAt); err != nil {
		t.Fatal(err)
	}
	if name != "foundation" || len(checksum) != 64 {
		t.Fatalf("metadata name=%q checksum=%q", name, checksum)
	}
	stamp, err := time.Parse(time.RFC3339Nano, appliedAt)
	if err != nil || stamp.Location() != time.UTC {
		t.Fatalf("applied_at = %q, parse error = %v", appliedAt, err)
	}
}

func TestOpenDetectsMigrationChecksumConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conflict.db")
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("UPDATE schema_migrations SET checksum = 'changed' WHERE version = 1"); err != nil {
		t.Fatal(err)
	}
	store.Close()

	if _, err := Open(context.Background(), path); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("Open() error = %v, want ErrConflict", err)
	}
}

func TestOpenUpgradesFoundationDatabaseAppendOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	foundation, err := embeddedMigrations.ReadFile("migrations/000001_foundation.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := applyMigrations(context.Background(), db, fstest.MapFS{
		"migrations/000001_foundation.sql": {Data: foundation},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("upgrade Open() error = %v", err)
	}
	defer store.Close()
	status, err := store.MigrationStatus(context.Background())
	if err != nil || status.AppliedVersion != 2 || !status.Current {
		t.Fatalf("upgraded migration status = %+v, err=%v", status, err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='runtime_instances'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("Phase 1 runtime_instances table was not created during upgrade")
	}
}

func TestFailedMigrationRollsBackEntireBatch(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "rollback.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	source := fstest.MapFS{
		"migrations/000001_foundation.sql": {Data: []byte(foundationSQL)},
		"migrations/000002_broken.sql":     {Data: []byte("CREATE TABLE incomplete (")},
	}
	if err := applyMigrations(context.Background(), db, source); !errors.Is(err, errs.ErrIncompatible) {
		t.Fatalf("applyMigrations() error = %v, want ErrIncompatible", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("schema_migrations exists after rollback")
	}
}

func TestApplyMigrationsHonorsCancelledContext(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "cancel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = applyMigrations(ctx, db, fstest.MapFS{"migrations/000001_foundation.sql": {Data: []byte(foundationSQL)}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("applyMigrations() error = %v, want context.Canceled", err)
	}
}
