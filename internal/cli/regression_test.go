package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/deployment"
	"github.com/guijoazeiro/prodmap/internal/regression"
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

func TestRegressionCLIClassificationCandidateNoSignalAndHumanOutput(t *testing.T) {
	project := t.TempDir()
	deploymentID, deployedAt := seedRegressionCLIDataWithMetrics(t, project, regressionCLIMetrics{requests: 20, errors: 1, p50NS: 250_000_000, p95NS: 250_000_000, p99NS: 250_000_000}, regressionCLIMetrics{requests: 30, errors: 1, p50NS: 300_000_000, p95NS: 300_000_000, p99NS: 300_000_000})
	app, stdout, stderr := testApp(project)
	generatedAt := deployedAt.Add(time.Hour)
	nowCalls := 0
	app.Now = func() time.Time {
		nowCalls++
		return generatedAt
	}
	candidateArgs := regressionCLIArgs(deploymentID, "latency_p50", project, true)
	if code := app.Run(t.Context(), candidateArgs); code != 0 || stderr.Len() != 0 {
		t.Fatalf("candidate code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	candidateEnvelope := decodeEnvelope(t, stdout.Bytes())
	candidate := candidateEnvelope["data"].(map[string]any)
	classification := candidate["classification"].(map[string]any)
	if candidateEnvelope["schema_version"] == "" || candidate["comparison_key"] == "" || classification["classification_key"] == "" || classification["result"] != "CANDIDATE" || classification["direction"] != "INCREASE" || classification["algorithm"] != "regression-threshold" || classification["algorithm_version"] != "regression-threshold/v1-experimental" || classification["causality_claimed"] != false {
		t.Fatalf("candidate classification=%#v", classification)
	}
	thresholds := classification["thresholds"].(map[string]any)
	effect := classification["observed_effect"].(map[string]any)
	confidence := classification["confidence"].(map[string]any)
	if thresholds["absolute_min"] != float64(50_000_000) || thresholds["relative_min"] != .2 || thresholds["require_all"] != true || thresholds["unit"] != "nanoseconds" || effect["absolute_delta"] != float64(50_000_000) || effect["relative_delta"] != .2 || confidence["level"] != "LOW" || confidence["limitations"] == nil {
		t.Fatalf("candidate contract thresholds=%#v effect=%#v confidence=%#v", thresholds, effect, confidence)
	}
	assertRegressionOutputDoesNotLeak(t, stdout.String())
	if nowCalls != 1 {
		t.Fatalf("generated_at calls=%d, want 1", nowCalls)
	}
	comparisonKey, classificationKey := candidate["comparison_key"], classification["classification_key"]

	stdout.Reset()
	stderr.Reset()
	nowCalls = 0
	app.Now = func() time.Time {
		nowCalls++
		return generatedAt.Add(time.Minute)
	}
	if code := app.Run(t.Context(), candidateArgs); code != 0 || stderr.Len() != 0 {
		t.Fatalf("candidate replay code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	replay := decodeEnvelope(t, stdout.Bytes())["data"].(map[string]any)
	if replay["comparison_key"] != comparisonKey || replay["classification"].(map[string]any)["classification_key"] != classificationKey || nowCalls != 1 {
		t.Fatalf("candidate replay=%#v now_calls=%d", replay, nowCalls)
	}

	stdout.Reset()
	stderr.Reset()
	if code := app.Run(t.Context(), regressionCLIArgs(deploymentID, "latency_p50", project, false)); code != 0 {
		t.Fatalf("human candidate code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "Classification: CANDIDATE") || !strings.Contains(stdout.String(), "Classification direction: INCREASE") || !strings.Contains(stdout.String(), "Classification confidence: LOW") || !strings.Contains(stdout.String(), "Classification algorithm: regression-threshold regression-threshold/v1-experimental") || !strings.Contains(stdout.String(), "Classification observed effect:") || !strings.Contains(stdout.String(), "Classification thresholds:") || !strings.Contains(stdout.String(), "Classification key: sha256:") || strings.Contains(stdout.String(), "{") || strings.Count(stderr.String(), "warning: temporal proximity does not establish causality\n") != 1 {
		t.Fatalf("human stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	assertRegressionOutputDoesNotLeak(t, stdout.String()+stderr.String())
}

func TestRegressionCLIClassificationNoSignal(t *testing.T) {
	project := t.TempDir()
	deploymentID, deployedAt := seedRegressionCLIDataWithMetrics(t, project, regressionCLIMetrics{requests: 20, errors: 1, p50NS: 250_000_000, p95NS: 250_000_000, p99NS: 250_000_000}, regressionCLIMetrics{requests: 30, errors: 1, p50NS: 299_999_999, p95NS: 299_999_999, p99NS: 299_999_999})
	app, stdout, stderr := testApp(project)
	app.Now = func() time.Time { return deployedAt.Add(time.Hour) }
	if code := app.Run(t.Context(), regressionCLIArgs(deploymentID, "latency_p95", project, true)); code != 0 || stderr.Len() != 0 {
		t.Fatalf("no-signal code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	noSignal := decodeEnvelope(t, stdout.Bytes())["data"].(map[string]any)["classification"].(map[string]any)
	if noSignal["result"] != "NO_SIGNAL" || noSignal["direction"] != "INCREASE" || noSignal["confidence"].(map[string]any)["level"] != "LOW" || noSignal["causality_claimed"] != false {
		t.Fatalf("no-signal classification=%#v", noSignal)
	}
}

func TestRegressionCLIClassificationUnknownForFutureMissingAndRequestCount(t *testing.T) {
	t.Run("future interval", func(t *testing.T) {
		project := t.TempDir()
		deploymentID, deployedAt := seedRegressionCLIDataWithMetrics(t, project, regressionCLIMetrics{requests: 20, errors: 1, p50NS: 250_000_000, p95NS: 250_000_000, p99NS: 250_000_000}, regressionCLIMetrics{requests: 30, errors: 1, p50NS: 300_000_000, p95NS: 300_000_000, p99NS: 300_000_000})
		app, stdout, stderr := testApp(project)
		app.Now = func() time.Time { return deployedAt.Add(15 * time.Minute) }
		if code := app.Run(t.Context(), regressionCLIArgs(deploymentID, "latency_p50", project, true)); code != 0 || stderr.Len() != 0 {
			t.Fatalf("future code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		data := decodeEnvelope(t, stdout.Bytes())["data"].(map[string]any)
		classification := data["classification"].(map[string]any)
		if data["status"] != "UNKNOWN" || classification["result"] != "UNKNOWN" || classification["confidence"].(map[string]any)["level"] != "UNKNOWN" || classification["observed_effect"].(map[string]any)["absolute_delta"] != nil {
			t.Fatalf("future data=%#v", data)
		}
	})
	t.Run("missing observation", func(t *testing.T) {
		project := t.TempDir()
		deployedAt := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
		deploymentID := seedRegressionCLIDeployment(t, project, deployedAt)
		app, stdout, stderr := testApp(project)
		app.Now = func() time.Time { return deployedAt.Add(time.Hour) }
		if code := app.Run(t.Context(), regressionCLIArgs(deploymentID, "latency_p50", project, true)); code != 0 || stderr.Len() != 0 {
			t.Fatalf("missing code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		classification := decodeEnvelope(t, stdout.Bytes())["data"].(map[string]any)["classification"].(map[string]any)
		if classification["result"] != "UNKNOWN" || classification["confidence"].(map[string]any)["level"] != "UNKNOWN" {
			t.Fatalf("missing classification=%#v", classification)
		}
	})
	t.Run("request count", func(t *testing.T) {
		project := t.TempDir()
		deploymentID, deployedAt := seedRegressionCLIData(t, project)
		app, stdout, stderr := testApp(project)
		app.Now = func() time.Time { return deployedAt.Add(time.Hour) }
		if code := app.Run(t.Context(), regressionCLIArgs(deploymentID, "request_count", project, true)); code != 0 || stderr.Len() != 0 {
			t.Fatalf("request_count code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		classification := decodeEnvelope(t, stdout.Bytes())["data"].(map[string]any)["classification"].(map[string]any)
		if classification["result"] != "UNKNOWN" || classification["confidence"].(map[string]any)["basis"] != "request_count is not classifiable by the experimental threshold algorithm" {
			t.Fatalf("request_count classification=%#v", classification)
		}
	})
}

func TestRegressionOutputCopiesClassification(t *testing.T) {
	value := 50_000_000.0
	result := regression.Result{Classification: &regression.Classification{ClassificationKey: "sha256:key", Result: "CANDIDATE", Direction: "INCREASE", AlgorithmVersion: regression.ClassificationAlgorithmVersion, Thresholds: regression.Thresholds{AbsoluteMin: &value}, ObservedEffect: regression.Effect{AbsoluteDelta: &value}, Confidence: regression.Confidence{Limitations: []string{"once"}}}}
	output := regressionOutputFrom(result)
	*result.Classification.Thresholds.AbsoluteMin = 1
	result.Classification.Confidence.Limitations[0] = "changed"
	if output.Classification == nil || *output.Classification.Thresholds.AbsoluteMin != 50_000_000 || output.Classification.Confidence.Limitations[0] != "once" {
		t.Fatalf("classification output shares mutable domain state: %#v", output.Classification)
	}
}

func regressionCLIArgs(deploymentID, metric, project string, json bool) []string {
	args := []string{"regression", "--deployment", deploymentID, "--metric", metric, "--before", "30m", "--after", "30m", "--project-dir", project}
	if json {
		args = append(args, "--json")
	}
	return args
}

func assertRegressionOutputDoesNotLeak(t *testing.T, output string) {
	t.Helper()
	for _, forbidden := range []string{"token", "Authorization", "Bearer", "password", "sqlite:", "checkout:stable", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "payload", "deployment.jsonl"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("regression output leaked %q: %q", forbidden, output)
		}
	}
}

func seedRegressionCLIData(t *testing.T, project string) (string, time.Time) {
	return seedRegressionCLIDataWithMetrics(t, project, regressionCLIMetrics{requests: 20, errors: 1, p50NS: 10, p95NS: 20, p99NS: 30}, regressionCLIMetrics{requests: 30, errors: 1, p50NS: 20, p95NS: 30, p99NS: 40})
}

type regressionCLIMetrics struct {
	requests, errors    int64
	p50NS, p95NS, p99NS int64
}

func seedRegressionCLIDataWithMetrics(t *testing.T, project string, before, after regressionCLIMetrics) (string, time.Time) {
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
		metrics    regressionCLIMetrics
	}{
		{deployedAt.Add(-30 * time.Minute), deployedAt, "a", before},
		{deployedAt, deployedAt.Add(30 * time.Minute), "b", after},
	} {
		snapshot := telemetry.Snapshot{SourceKey: "file:regression-" + entry.hash, SourceHash: "sha256:" + strings.Repeat(entry.hash, 64), Environment: "reference", WindowStart: entry.start, WindowEnd: entry.end, ObservedAt: entry.end, Stats: telemetry.Stats{Services: 1, TelemetryWindows: 1}, Services: []telemetry.Service{{LogicalKey: "checkout", DisplayName: "checkout"}}, Windows: []telemetry.Window{{Key: "checkout\x00", ServiceKey: "checkout", WindowStart: entry.start, WindowEnd: entry.end, RequestCount: entry.metrics.requests, ErrorCount: entry.metrics.errors, P50NS: entry.metrics.p50NS, P95NS: entry.metrics.p95NS, P99NS: entry.metrics.p99NS, CoverageRatio: &coverage, IsComplete: true}}}
		if _, err := store.SaveTelemetry(ctx, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	return seedRegressionCLIDeployment(t, project, deployedAt), deployedAt
}

func seedRegressionCLIDeployment(t *testing.T, project string, deployedAt time.Time) string {
	t.Helper()
	ctx := context.Background()
	store, err := prodmapsqlite.Open(ctx, filepath.Join(project, ".prodmap", "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
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
	return items.Items[0].ID
}
