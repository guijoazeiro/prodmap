package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/deployment"
	prodmapsqlite "github.com/guijoazeiro/prodmap/internal/sqlite"
	"github.com/guijoazeiro/prodmap/internal/telemetry"
)

func TestRegressionHelpAndInvalidInputDoNotOpenInventory(t *testing.T) {
	project := t.TempDir()
	app, stdout, stderr := testApp(project)
	if code := app.Run(t.Context(), []string{"regression", "--help"}); code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "usage: prodmap regression") {
		t.Fatalf("help code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(t.Context(), []string{"regression", "--deployment", "anything", "--metric", "unknown", "--json", "--project-dir", project}); code == 0 || stderr.Len() != 0 {
		t.Fatalf("invalid code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), ".prodmap") {
		t.Fatalf("invalid output leaked project state: %q", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(project, ".prodmap")); !os.IsNotExist(err) {
		t.Fatalf("invalid input created local state: %v", err)
	}
}

func TestRegressionRejectsUnequalRequestCountDurationsBeforeOpeningInventory(t *testing.T) {
	project := t.TempDir()
	app, stdout, stderr := testApp(project)
	code := app.Run(t.Context(), []string{"regression", "--deployment", "01a049db-14e4-7798-983b-2946718efaa7", "--metric", "request_count", "--before", "30m", "--after", "45m", "--json", "--project-dir", project})
	if code == 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(project, ".prodmap")); !os.IsNotExist(err) {
		t.Fatalf("invalid request_count comparison opened inventory: %v", err)
	}
}

func TestRegressionCLIProducesAvailableAndFutureUnknownJSON(t *testing.T) {
	project := t.TempDir()
	deploymentID, deployedAt := seedRegressionCLIData(t, project)
	app, stdout, stderr := testApp(project)
	args := []string{"regression", "--deployment", deploymentID, "--metric", "request_count", "--before", "30m", "--after", "30m", "--project-dir", project, "--json"}
	generatedAt := deployedAt.Add(time.Hour)
	nowCalls := 0
	app.Now = func() time.Time {
		nowCalls++
		return generatedAt
	}
	if code := app.Run(t.Context(), args); code != 0 || stderr.Len() != 0 {
		t.Fatalf("available code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	envelope := decodeEnvelope(t, stdout.Bytes())
	data := envelope["data"].(map[string]any)
	if envelope["generated_at"] != generatedAt.Format(time.RFC3339Nano) || data["status"] != "AVAILABLE" || data["classification"].(map[string]any)["result"] != "UNKNOWN" || data["causality_claimed"] != false || data["absolute_delta"] != float64(10) || data["relative_delta"] != float64(.5) {
		t.Fatalf("available envelope=%#v", envelope)
	}
	if data["before"].(map[string]any)["accepted_windows"] == nil || data["after"].(map[string]any)["rejected_windows"] == nil {
		t.Fatalf("comparison arrays must be non-null: %#v", data)
	}
	if nowCalls != 1 {
		t.Fatalf("generated_at must be captured once, calls=%d", nowCalls)
	}
	comparisonKey := data["comparison_key"]
	if strings.Contains(stdout.String(), "checkout:stable") || strings.Contains(stdout.String(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") || strings.Contains(stdout.String(), "HIGH") || strings.Contains(stdout.String(), "EXACT") {
		t.Fatalf("comparison output exposed non-contract data: %q", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	nowCalls = 0
	if code := app.Run(t.Context(), args); code != 0 || stderr.Len() != 0 || decodeEnvelope(t, stdout.Bytes())["data"].(map[string]any)["comparison_key"] != comparisonKey || nowCalls != 1 {
		t.Fatalf("replay code=%d stdout=%q stderr=%q now_calls=%d", code, stdout.String(), stderr.String(), nowCalls)
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(t.Context(), args[:len(args)-1]); code != 0 || !strings.Contains(stdout.String(), "Regression comparison: AVAILABLE request_count") || !strings.Contains(stdout.String(), "Comparison confidence: LOW") || !strings.Contains(stderr.String(), "temporal proximity does not establish causality") {
		t.Fatalf("human code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	nowCalls = 0
	app.Now = func() time.Time { return deployedAt.Add(15 * time.Minute) }
	if code := app.Run(t.Context(), args); code != 0 || stderr.Len() != 0 {
		t.Fatalf("future code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	data = decodeEnvelope(t, stdout.Bytes())["data"].(map[string]any)
	if data["status"] != "UNKNOWN" || data["absolute_delta"] != nil || data["relative_delta"] != nil || data["regression_confidence"].(map[string]any)["level"] != "UNKNOWN" {
		t.Fatalf("future comparison=%#v", data)
	}
}

func seedRegressionCLIData(t *testing.T, project string) (string, time.Time) {
	t.Helper()
	ctx := context.Background()
	store, err := prodmapsqlite.Open(ctx, filepath.Join(project, ".prodmap", "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	deployedAt := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
	coverage := 1.0
	for _, entry := range []struct {
		start, end time.Time
		hash       string
		requests   int64
	}{
		{deployedAt.Add(-30 * time.Minute), deployedAt, "a", 20},
		{deployedAt, deployedAt.Add(30 * time.Minute), "b", 30},
	} {
		snapshot := telemetry.Snapshot{SourceKey: "file:regression-" + entry.hash, SourceHash: "sha256:" + strings.Repeat(entry.hash, 64), Environment: "reference", WindowStart: entry.start, WindowEnd: entry.end, ObservedAt: entry.end, Stats: telemetry.Stats{Services: 1, TelemetryWindows: 1}, Services: []telemetry.Service{{LogicalKey: "checkout", DisplayName: "checkout"}}, Windows: []telemetry.Window{{Key: "checkout\x00", ServiceKey: "checkout", WindowStart: entry.start, WindowEnd: entry.end, RequestCount: entry.requests, ErrorCount: 1, P50NS: 10, P95NS: 20, P99NS: 30, CoverageRatio: &coverage, IsComplete: true}}}
		if _, err := store.SaveTelemetry(ctx, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	ledger := filepath.Join(t.TempDir(), "deployment.jsonl")
	record := `{"schema_version":"1.0","deployment_id":"regression-cli","deployed_at":"` + deployedAt.Format(time.RFC3339) + `","build_started_at":"` + deployedAt.Add(-time.Minute).Format(time.RFC3339) + `","build_date":"` + deployedAt.Add(-2*time.Minute).Format(time.RFC3339) + `","environment":"reference","service":"checkout","version":"abc123","scenario_profile":"healthy","git_head":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","git_dirty":false,"vcs_revision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","vcs_revision_verified":true,"image_reference":"checkout:stable","image_id":"sha256:1111111111111111111111111111111111111111111111111111111111111111","repo_digest":null,"compose_project":"reference-project","status":"running"}` + "\n"
	if err := os.WriteFile(ledger, []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := deployment.LoadFrozenFile(ctx, ledger, deployedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveDeployment(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	items, err := store.Deployments(ctx, deployment.Query{Environment: "reference", Service: "checkout", Since: deployedAt.Add(-time.Minute), Until: deployedAt.Add(time.Minute), Limit: 10})
	if err != nil || len(items.Items) != 1 {
		t.Fatalf("deployments=%+v err=%v", items, err)
	}
	return items.Items[0].ID, deployedAt
}
