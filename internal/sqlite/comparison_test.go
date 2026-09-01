package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/baseline"
	"github.com/guijoazeiro/prodmap/internal/deployment"
	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/regression"
	"github.com/guijoazeiro/prodmap/internal/telemetry"
)

func TestComparisonResolvesDeploymentAndExactServiceWindowsReadOnly(t *testing.T) {
	ctx := t.Context()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	deploymentAt := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
	coverage := 1.0
	for _, item := range []struct {
		start, end time.Time
		hash       string
		requests   int64
	}{
		{deploymentAt.Add(-30 * time.Minute), deploymentAt, "a", 20},
		{deploymentAt, deploymentAt.Add(30 * time.Minute), "b", 30},
	} {
		snapshot := telemetry.Snapshot{SourceKey: "file:comparison-" + item.hash, SourceHash: "sha256:" + strings.Repeat(item.hash, 64), Environment: "reference", WindowStart: item.start, WindowEnd: item.end, ObservedAt: item.end, Stats: telemetry.Stats{Services: 1, TelemetryWindows: 1}, Services: []telemetry.Service{{LogicalKey: "checkout", DisplayName: "checkout"}}, Windows: []telemetry.Window{{Key: "checkout\x00", ServiceKey: "checkout", WindowStart: item.start, WindowEnd: item.end, RequestCount: item.requests, ErrorCount: 1, P50NS: 10, P95NS: 20, P99NS: 30, CoverageRatio: &coverage, IsComplete: true}}}
		if _, err := store.SaveTelemetry(ctx, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	selected := saveComparisonDeployment(t, ctx, store, "selected", deploymentAt)
	input, err := store.Comparison(ctx, regression.Query{DeploymentID: selected, Metric: baseline.LatencyP95, Before: 30 * time.Minute, After: 30 * time.Minute, MinSamples: 10, MinCoverage: .8})
	if err != nil || input.Deployment.ID != selected || len(input.Before) != 1 || len(input.After) != 1 || len(input.Contamination.BeforeDeployments) != 0 || len(input.Contamination.AfterDeployments) != 0 || len(input.Contamination.ConcurrentDeployments) != 0 {
		t.Fatalf("input=%+v err=%v", input, err)
	}
	input.GeneratedAt = deploymentAt.Add(time.Hour)
	result, err := regression.Evaluate(ctx, input)
	if err != nil || result.Status != "AVAILABLE" || result.RegressionConfidence.Level != "LOW" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var persisted int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND (name LIKE '%regression%' OR name LIKE '%comparison%')`).Scan(&persisted); err != nil || persisted != 0 {
		t.Fatalf("comparison read persisted analytical state rows=%d err=%v", persisted, err)
	}
	if _, err := store.Comparison(ctx, regression.Query{DeploymentID: "01a049db-14e4-7798-983b-2946718efaa7", Metric: baseline.LatencyP95, Before: 30 * time.Minute, After: 30 * time.Minute, MinSamples: 10, MinCoverage: .8}); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("missing deployment err=%v", err)
	}
}

func TestComparisonContaminationUsesHalfOpenBoundaries(t *testing.T) {
	ctx := t.Context()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	deploymentAt := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
	selected := saveComparisonDeployment(t, ctx, store, "selected", deploymentAt)
	beforeID := saveComparisonDeployment(t, ctx, store, "before", deploymentAt.Add(-30*time.Minute))
	concurrentID := saveComparisonDeployment(t, ctx, store, "concurrent", deploymentAt)
	afterID := saveComparisonDeployment(t, ctx, store, "after", deploymentAt.Add(time.Minute))
	_ = saveComparisonDeployment(t, ctx, store, "outside", deploymentAt.Add(30*time.Minute))
	_ = saveComparisonDeploymentFor(t, ctx, store, "other-service", deploymentAt, "reference", "payment")
	_ = saveComparisonDeploymentFor(t, ctx, store, "other-environment", deploymentAt, "staging", "checkout")
	input, err := store.Comparison(ctx, regression.Query{DeploymentID: selected, Metric: baseline.RequestCount, Before: 30 * time.Minute, After: 30 * time.Minute, MinSamples: 10, MinCoverage: .8})
	if err != nil || len(input.Contamination.BeforeDeployments) != 1 || input.Contamination.BeforeDeployments[0] != beforeID || len(input.Contamination.ConcurrentDeployments) != 1 || input.Contamination.ConcurrentDeployments[0] != concurrentID || len(input.Contamination.AfterDeployments) != 1 || input.Contamination.AfterDeployments[0] != afterID {
		t.Fatalf("contamination=%+v err=%v", input.Contamination, err)
	}
}

func saveComparisonDeployment(t *testing.T, ctx context.Context, store *Store, name string, at time.Time) string {
	t.Helper()
	return saveComparisonDeploymentFor(t, ctx, store, name, at, "reference", "checkout")
}

func saveComparisonDeploymentFor(t *testing.T, ctx context.Context, store *Store, name string, at time.Time, environment, service string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), name+".jsonl")
	value := `{"schema_version":"1.0","deployment_id":"` + name + `","deployed_at":"` + at.Format(time.RFC3339) + `","build_started_at":"` + at.Add(-time.Minute).Format(time.RFC3339) + `","build_date":"` + at.Add(-2*time.Minute).Format(time.RFC3339) + `","environment":"` + environment + `","service":"` + service + `","version":"abc123","scenario_profile":"healthy","git_head":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","git_dirty":false,"vcs_revision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","vcs_revision_verified":true,"image_reference":"checkout:stable","image_id":"sha256:1111111111111111111111111111111111111111111111111111111111111111","repo_digest":null,"compose_project":"reference-project","status":"running"}` + "\n"
	if err := os.WriteFile(file, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := deployment.LoadFrozenFile(ctx, file, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveDeployment(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := store.db.QueryRowContext(ctx, `SELECT id FROM deployments WHERE external_id=?`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
