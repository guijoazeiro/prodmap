package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/baseline"
	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/investigation"
	"github.com/guijoazeiro/prodmap/internal/regression"
	"github.com/guijoazeiro/prodmap/internal/telemetry"
	"github.com/guijoazeiro/prodmap/internal/timeline"
	"github.com/guijoazeiro/prodmap/internal/topology"
)

func TestBeginReadSnapshotIsReadOnlyAndReleasesConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prodmap.db")
	createReadOnlyFixture(t, path)
	before := databaseSemanticState(t, path)

	readOnly, err := OpenReadOnly(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	snapshot, err := readOnly.BeginReadSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var queryOnly string
	if err := snapshot.tx.QueryRowContext(t.Context(), "PRAGMA query_only").Scan(&queryOnly); err != nil || queryOnly != "1" {
		t.Fatalf("query_only=%q err=%v", queryOnly, err)
	}
	for _, statement := range []string{
		"CREATE TABLE snapshot_forbidden(id INTEGER)",
		"INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (999, 'forbidden', 'forbidden', 'forbidden')",
		"UPDATE schema_migrations SET name='forbidden' WHERE version=1",
		"DELETE FROM schema_migrations WHERE version=1",
		"PRAGMA user_version=9",
	} {
		if _, err := snapshot.tx.ExecContext(t.Context(), statement); err == nil {
			t.Fatalf("read-only snapshot accepted write %q", statement)
		}
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}

	// Store has one connection, so another snapshot proves Close released it.
	second, err := readOnly.BeginReadSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if after := databaseSemanticState(t, path); before != after {
		t.Fatalf("snapshot changed logical database state\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestBeginReadSnapshotRequiresReadOnlyStoreAndPreservesCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prodmap.db")
	writer, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.BeginReadSnapshot(t.Context()); !errors.Is(err, errs.ErrInvalid) {
		_ = writer.Close()
		t.Fatalf("BeginReadSnapshot on write store error = %v, want ErrInvalid", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	readOnly, err := OpenReadOnly(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := readOnly.BeginReadSnapshot(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("BeginReadSnapshot(cancelled) error = %v, want context.Canceled", err)
	}
	snapshot, err := readOnly.BeginReadSnapshot(t.Context())
	if err != nil {
		t.Fatalf("cancelled attempt left connection unusable: %v", err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReadSnapshotKeepsInvestigationOnOneSQLiteState(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "prodmap.db")
	writer, deploymentID, deployedAt := seedSnapshotInvestigation(t, path)
	defer writer.Close()
	readOnly, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()

	snapshot, err := readOnly.BeginReadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	inserted := false
	reader := writeAfterComparisonReader{
		ReadSnapshot: snapshot,
		afterComparison: func() error {
			inserted = true
			_ = saveComparisonDeployment(t, ctx, writer, "writer-visible-only-later", deployedAt.Add(time.Minute))
			return nil
		},
	}
	query := snapshotInvestigationQuery(deploymentID, deployedAt)
	first, err := investigation.Compose(ctx, reader, query)
	if err != nil {
		_ = snapshot.Close()
		t.Fatal(err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if !inserted || first.Status != "AVAILABLE" || len(first.Regression.Contamination.AfterDeployments) != 0 || timelineContains(first.Timeline, "writer-visible-only-later") {
		t.Fatalf("mixed snapshot result=%+v inserted=%v", first, inserted)
	}

	fresh, err := readOnly.BeginReadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := investigation.Compose(ctx, fresh, query)
	closeErr := fresh.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if len(second.Regression.Contamination.AfterDeployments) != 1 || !timelineContains(second.Timeline, "writer-visible-only-later") {
		t.Fatalf("fresh investigation did not observe committed writer state: %+v", second)
	}
}

func TestComparisonUsesOneSnapshotAcrossInternalSelects(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "prodmap.db")
	writer, deploymentID, deployedAt := seedSnapshotInvestigation(t, path)
	defer writer.Close()
	readOnly, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	snapshot, err := readOnly.BeginReadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hooked := &comparisonHookQueryer{queryer: snapshot.tx, hook: func() error {
		_ = saveComparisonDeployment(t, ctx, writer, "comparison-committed-after-first-select", deployedAt.Add(time.Minute))
		return nil
	}}
	input, err := comparison(ctx, hooked, snapshotInvestigationQuery(deploymentID, deployedAt).Comparison)
	if err != nil {
		_ = snapshot.Close()
		t.Fatal(err)
	}
	if !hooked.called || len(input.Before) != 1 || len(input.After) != 1 || len(input.Contamination.AfterDeployments) != 0 {
		_ = snapshot.Close()
		t.Fatalf("comparison mixed committed state: hook=%v input=%+v", hooked.called, input)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	fresh, err := readOnly.BeginReadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	freshInput, freshErr := fresh.Comparison(ctx, snapshotInvestigationQuery(deploymentID, deployedAt).Comparison)
	closeErr := fresh.Close()
	if freshErr != nil || closeErr != nil {
		t.Fatalf("fresh comparison errors = %v %v", freshErr, closeErr)
	}
	if len(freshInput.Contamination.AfterDeployments) != 1 {
		t.Fatalf("fresh comparison did not observe committed contamination: %+v", freshInput.Contamination)
	}
}

type writeAfterComparisonReader struct {
	*ReadSnapshot
	afterComparison func() error
}

func (r writeAfterComparisonReader) Comparison(ctx context.Context, query regression.Query) (regression.Input, error) {
	input, err := r.ReadSnapshot.Comparison(ctx, query)
	if err != nil {
		return regression.Input{}, err
	}
	if err := r.afterComparison(); err != nil {
		return regression.Input{}, err
	}
	return input, nil
}

type comparisonHookQueryer struct {
	queryer
	once   sync.Once
	hook   func() error
	called bool
	err    error
}

func (q *comparisonHookQueryer) QueryRowContext(ctx context.Context, statement string, args ...any) *sql.Row {
	row := q.queryer.QueryRowContext(ctx, statement, args...)
	if strings.Contains(statement, "SELECT id FROM services") {
		q.once.Do(func() {
			q.called = true
			q.err = q.hook()
		})
	}
	return row
}

func (q *comparisonHookQueryer) QueryContext(ctx context.Context, statement string, args ...any) (*sql.Rows, error) {
	if q.err != nil {
		return nil, q.err
	}
	return q.queryer.QueryContext(ctx, statement, args...)
}

func seedSnapshotInvestigation(t *testing.T, path string) (*Store, string, time.Time) {
	t.Helper()
	ctx := t.Context()
	writer, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	deployedAt := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	coverage := 1.0
	for index, item := range []struct{ start, end time.Time }{
		{deployedAt.Add(-5 * time.Minute), deployedAt},
		{deployedAt, deployedAt.Add(5 * time.Minute)},
	} {
		snapshot := telemetry.Snapshot{
			SourceKey:   fmt.Sprintf("file:snapshot-%d", index),
			SourceHash:  fmt.Sprintf("sha256:%064d", index+1),
			Environment: "reference",
			WindowStart: item.start,
			WindowEnd:   item.end,
			ObservedAt:  deployedAt.Add(10 * time.Minute),
			Stats:       telemetry.Stats{Services: 1, TelemetryWindows: 1},
			Services:    []telemetry.Service{{LogicalKey: "checkout", DisplayName: "checkout"}},
			Windows: []telemetry.Window{{Key: "checkout\x00", ServiceKey: "checkout", WindowStart: item.start, WindowEnd: item.end,
				RequestCount: 20, ErrorCount: 0, P50NS: 250_000_000, P95NS: 250_000_000, P99NS: 250_000_000, CoverageRatio: &coverage, IsComplete: true}},
		}
		if _, err := writer.SaveTelemetry(ctx, snapshot); err != nil {
			_ = writer.Close()
			t.Fatal(err)
		}
	}
	deploymentID := saveComparisonDeployment(t, ctx, writer, "snapshot-selected", deployedAt)
	return writer, deploymentID, deployedAt
}

func snapshotInvestigationQuery(deploymentID string, deployedAt time.Time) investigation.Query {
	return investigation.Query{Comparison: regression.Query{DeploymentID: deploymentID, Metric: baseline.LatencyP95, Before: 5 * time.Minute, After: 5 * time.Minute, MinSamples: 1, MinCoverage: 0.8}, GeneratedAt: deployedAt.Add(time.Hour)}
}

func timelineContains(result timeline.Result, externalID string) bool {
	for _, item := range result.Items {
		if item.Subject.Name == externalID {
			return true
		}
	}
	return false
}

var _ topology.Reader = writeAfterComparisonReader{}
