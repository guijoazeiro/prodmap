package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/baseline"
	"github.com/guijoazeiro/prodmap/internal/deployment"
	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/telemetry"
)

func TestBaselineResolvesExactServiceAndEndpointWindowsReadOnly(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 8, 28, 11, 30, 0, 0, time.UTC)
	end := start.Add(30 * time.Minute)
	store, err := Open(ctx, filepath.Join(t.TempDir(), "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	coverage := .9
	endpoint := telemetry.Endpoint{Key: "checkout\x00http\x00POST /checkout", ServiceKey: "checkout", Protocol: "http", Operation: "POST /checkout"}
	snapshot := endpointSnapshot(start, end, "b", "reference", []telemetry.Endpoint{endpoint}, []telemetry.Window{
		{Key: "checkout\x00", ServiceKey: "checkout", WindowStart: start, WindowEnd: end, RequestCount: 20, ErrorCount: 2, P50NS: 10, P95NS: 20, P99NS: 30, CoverageRatio: &coverage, IsComplete: true},
		{Key: endpoint.Key, ServiceKey: "checkout", EndpointKey: endpoint.Key, WindowStart: start, WindowEnd: end, RequestCount: 12, ErrorCount: 3, P50NS: 11, P95NS: 21, P99NS: 31, CoverageRatio: &coverage, IsComplete: true},
	})
	if _, err := store.SaveTelemetry(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	serviceInput, err := store.Baseline(ctx, baseline.Query{Environment: "reference", ServiceKey: "checkout", Metric: baseline.ErrorRate, At: end, Window: 30 * time.Minute, MinSamples: 10, MinCoverage: .8, GeneratedAt: end.Add(time.Minute)})
	if err != nil || len(serviceInput.Candidates) != 1 || serviceInput.Target.Kind != baseline.ServiceTarget || serviceInput.Candidates[0].ErrorCount != 2 {
		t.Fatalf("service input=%+v err=%v", serviceInput, err)
	}
	var endpointID string
	if err := store.db.QueryRowContext(ctx, `SELECT id FROM endpoints WHERE protocol='http' AND operation='POST /checkout'`).Scan(&endpointID); err != nil {
		t.Fatal(err)
	}
	endpointInput, err := store.Baseline(ctx, baseline.Query{Environment: "reference", EndpointID: endpointID, Metric: baseline.LatencyP95, At: end, Window: 30 * time.Minute, MinSamples: 10, MinCoverage: .8, GeneratedAt: end.Add(time.Minute)})
	if err != nil || len(endpointInput.Candidates) != 1 || endpointInput.Target.Kind != baseline.EndpointTarget || endpointInput.Candidates[0].P95NS != 21 {
		t.Fatalf("endpoint input=%+v err=%v", endpointInput, err)
	}
	if _, err := store.Baseline(ctx, baseline.Query{Environment: "reference", ServiceKey: "missing", At: end, Window: 30 * time.Minute}); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("missing target err=%v", err)
	}
	var baselineRows int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name LIKE '%baseline%'`).Scan(&baselineRows); err != nil || baselineRows != 0 {
		t.Fatalf("baseline query persisted state rows=%d err=%v", baselineRows, err)
	}
}

func TestBaselineReturnsAllExactCandidatesForConservativeAmbiguity(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 8, 28, 11, 30, 0, 0, time.UTC)
	end := start.Add(30 * time.Minute)
	store, err := Open(ctx, filepath.Join(t.TempDir(), "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, hash := range []string{"c", "d"} {
		snapshot := endpointSnapshot(start, end, hash, "reference", nil, []telemetry.Window{{Key: "checkout\x00", ServiceKey: "checkout", WindowStart: start, WindowEnd: end, RequestCount: 12}})
		if _, err := store.SaveTelemetry(ctx, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	input, err := store.Baseline(ctx, baseline.Query{Environment: "reference", ServiceKey: "checkout", Metric: baseline.RequestCount, At: end, Window: 30 * time.Minute, MinSamples: 10, MinCoverage: .8, GeneratedAt: end.Add(time.Minute)})
	if err != nil || len(input.Candidates) != 2 {
		t.Fatalf("input=%+v err=%v", input, err)
	}
	result, err := baseline.Evaluate(ctx, input)
	if err != nil || result.Status != "UNKNOWN" || result.Confidence.Level != "UNKNOWN" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestBaselineMarksDeploymentWithinExactHalfOpenWindowAsContaminated(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 8, 28, 11, 30, 0, 0, time.UTC)
	end := start.Add(30 * time.Minute)
	store, err := Open(ctx, filepath.Join(t.TempDir(), "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot := endpointSnapshot(start, end, "e", "reference", nil, []telemetry.Window{{Key: "checkout\x00", ServiceKey: "checkout", WindowStart: start, WindowEnd: end, RequestCount: 12}})
	if _, err := store.SaveTelemetry(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	ledger := filepath.Join(t.TempDir(), "deployments.jsonl")
	contents := `{"schema_version":"1.0","deployment_id":"checkout-1","deployed_at":"2026-08-28T11:45:00Z","build_started_at":"2026-08-28T11:44:00Z","build_date":"2026-08-28T11:43:00Z","environment":"reference","service":"checkout","version":"abc123","scenario_profile":"healthy","git_head":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","git_dirty":false,"vcs_revision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","vcs_revision_verified":true,"image_reference":"checkout:stable","image_id":"sha256:1111111111111111111111111111111111111111111111111111111111111111","repo_digest":null,"compose_project":"reference-project","status":"running"}` + "\n"
	if err := os.WriteFile(ledger, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	deploymentSnapshot, err := deployment.LoadFrozenFile(ctx, ledger, end)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveDeployment(ctx, deploymentSnapshot); err != nil {
		t.Fatal(err)
	}
	input, err := store.Baseline(ctx, baseline.Query{Environment: "reference", ServiceKey: "checkout", Metric: baseline.RequestCount, At: end, Window: 30 * time.Minute, MinSamples: 10, MinCoverage: .8, GeneratedAt: end.Add(time.Minute)})
	if err != nil || len(input.Candidates) != 1 || !input.Candidates[0].Contaminated {
		t.Fatalf("input=%+v err=%v", input, err)
	}
	result, err := baseline.Evaluate(ctx, input)
	if err != nil || result.Status != "UNKNOWN" || result.Confidence.Level != "UNKNOWN" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
