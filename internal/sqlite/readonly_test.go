package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/guijoazeiro/prodmap/internal/errs"
)

func TestOpenReadOnlyCompatibleDatabasePreventsLogicalWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prodmap.db")
	createReadOnlyFixture(t, path)
	before := databaseSemanticState(t, path)

	store, err := OpenReadOnly(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenReadOnly() error = %v", err)
	}
	assertPragma(t, store, "query_only", "1")
	forceReplacementConnection(t, store)
	assertPragma(t, store, "query_only", "1")
	for _, statement := range []string{
		"CREATE TABLE forbidden(id INTEGER)",
		"INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (999, 'forbidden', 'forbidden', 'forbidden')",
		"UPDATE schema_migrations SET name = 'forbidden' WHERE version = 1",
		"DELETE FROM schema_migrations WHERE version = 1",
		"PRAGMA user_version = 1",
	} {
		if _, err := store.db.ExecContext(t.Context(), statement); err == nil {
			_ = store.Close()
			t.Fatalf("read-only statement succeeded: %s", statement)
		}
	}
	if err := applyMigrations(t.Context(), store.db, embeddedMigrations); err == nil {
		_ = store.Close()
		t.Fatal("applyMigrations succeeded on read-only Store")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	after := databaseSemanticState(t, path)
	if before != after {
		t.Fatalf("read-only open changed logical database state\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestOpenReadOnlyRejectsDatabaseWithoutMigrationTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty-schema.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE unrelated(id INTEGER)"); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReadOnly(t.Context(), path); !errors.Is(err, errs.ErrIncompatible) {
		t.Fatalf("OpenReadOnly(database without migrations) error = %v, want ErrIncompatible", err)
	}
}

func TestOpenReadOnlyRejectsAbsentAndInvalidPathsWithoutCreatingState(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "missing", "parent")
	path := filepath.Join(parent, "prodmap.db")
	if _, err := OpenReadOnly(t.Context(), path); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("OpenReadOnly(absent) error = %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(parent); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenReadOnly(absent) created parent directory: %v", err)
	}
	if _, err := OpenReadOnly(t.Context(), t.TempDir()); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("OpenReadOnly(directory) error = %v, want ErrInvalid", err)
	}
	if _, err := OpenReadOnly(t.Context(), ""); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("OpenReadOnly(empty) error = %v, want ErrInvalid", err)
	}
	if _, err := OpenReadOnly(t.Context(), ":memory:"); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("OpenReadOnly(memory) error = %v, want ErrInvalid", err)
	}
}

func TestOpenReadOnlyRejectsIncompatibleSchemaWithoutMigration(t *testing.T) {
	for name, mutate := range map[string]func(*testing.T, *Store){
		"partial": func(t *testing.T, store *Store) {
			t.Helper()
			if _, err := store.db.Exec("DELETE FROM schema_migrations WHERE version = 5"); err != nil {
				t.Fatal(err)
			}
		},
		"checksum": func(t *testing.T, store *Store) {
			t.Helper()
			if _, err := store.db.Exec("UPDATE schema_migrations SET checksum = 'different' WHERE version = 1"); err != nil {
				t.Fatal(err)
			}
		},
		"unknown": func(t *testing.T, store *Store) {
			t.Helper()
			if _, err := store.db.Exec("INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (999, 'future', 'future', '2026-01-01T00:00:00Z')"); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "prodmap.db")
			writer, err := Open(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			mutate(t, writer)
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := OpenReadOnly(t.Context(), path); !errors.Is(err, errs.ErrIncompatible) {
				t.Fatalf("OpenReadOnly(%s) error = %v, want ErrIncompatible", name, err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if sha256.Sum256(before) != sha256.Sum256(after) {
				t.Fatalf("read-only validation migrated or changed %s database", name)
			}
		})
	}
}

func TestOpenReadOnlyPreservesCancelledContextAndWorksWithoutWritePermission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prodmap.db")
	createReadOnlyFixture(t, path)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := OpenReadOnly(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("OpenReadOnly(cancelled) error = %v, want context.Canceled", err)
	}
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits are not portable on Windows")
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	store, err := OpenReadOnly(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenReadOnly(non-writable) error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReadOnlyDSNUsesReadOnlyModeWithoutWriteTransactionConfiguration(t *testing.T) {
	dsn, err := sqliteReadOnlyDSN(filepath.Join(t.TempDir(), "a ?#%.db"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dsn, "mode=ro") || !strings.Contains(dsn, "_query_only=1") || strings.Contains(dsn, "_txlock=immediate") {
		t.Fatalf("sqliteReadOnlyDSN() = %q", dsn)
	}
}

func createReadOnlyFixture(t *testing.T, path string) {
	t.Helper()
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

type databaseState struct {
	MainHash        [sha256.Size]byte
	SchemaVersion   int64
	UserVersion     int64
	Migrations      string
	Sources         int64
	Runtime         int64
	Deployments     int64
	TelemetryWindow int64
}

func databaseSemanticState(t *testing.T, path string) databaseState {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenReadOnly(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state := databaseState{MainHash: sha256.Sum256(contents)}
	if err := store.db.QueryRow("PRAGMA schema_version").Scan(&state.SchemaVersion); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow("PRAGMA user_version").Scan(&state.UserVersion); err != nil {
		t.Fatal(err)
	}
	rows, err := store.db.Query("SELECT version || ':' || name || ':' || checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var migrations []string
	for rows.Next() {
		var item string
		if err := rows.Scan(&item); err != nil {
			t.Fatal(err)
		}
		migrations = append(migrations, item)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	state.Migrations = strings.Join(migrations, ",")
	for query, target := range map[string]*int64{
		"SELECT COUNT(*) FROM sources":           &state.Sources,
		"SELECT COUNT(*) FROM runtime_instances": &state.Runtime,
		"SELECT COUNT(*) FROM deployments":       &state.Deployments,
		"SELECT COUNT(*) FROM telemetry_windows": &state.TelemetryWindow,
	} {
		if err := store.db.QueryRow(query).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	return state
}
