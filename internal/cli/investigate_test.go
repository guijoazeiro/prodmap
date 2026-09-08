package cli

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/baseline"
	"github.com/guijoazeiro/prodmap/internal/investigation"
	"github.com/guijoazeiro/prodmap/internal/regression"
	prodmapsqlite "github.com/guijoazeiro/prodmap/internal/sqlite"
	"github.com/guijoazeiro/prodmap/internal/timeline"
)

func TestInvestigateHelpAndInvalidArgumentsDoNotOpenInventory(t *testing.T) {
	project := t.TempDir()
	app, stdout, stderr := testApp(project)
	if code := app.Run(t.Context(), []string{"investigate", "--help"}); code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "usage: prodmap investigate") {
		t.Fatalf("help code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	args := []string{"investigate", "--deployment", "not-a-uuid", "--metric", "invalid", "--project-dir", project, "--json"}
	if code := app.Run(t.Context(), args); code == 0 || stderr.Len() != 0 {
		t.Fatalf("invalid code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(project, ".prodmap")); !os.IsNotExist(err) {
		t.Fatalf("invalid arguments opened inventory: %v", err)
	}
}

func TestInvestigateReadOnlyOpenDoesNotCreateAbsentInventory(t *testing.T) {
	project := t.TempDir()
	app, _, _ := testApp(project)
	args := []string{"investigate", "--deployment", "018f0000-0000-7000-8000-000000000000", "--metric", "latency_p95", "--project-dir", project}
	if code := app.Run(t.Context(), args); code == 0 {
		t.Fatal("investigate opened an absent inventory")
	}
	if _, err := os.Stat(filepath.Join(project, ".prodmap")); !os.IsNotExist(err) {
		t.Fatalf("investigate created absent inventory: %v", err)
	}
}

func TestComposeInvestigationSnapshotClosesAfterComposeError(t *testing.T) {
	project := t.TempDir()
	_, deployedAt := seedRegressionCLIDataWithMetrics(t, project, regressionCLIMetrics{requests: 20, p50NS: 250_000_000, p95NS: 250_000_000, p99NS: 250_000_000}, regressionCLIMetrics{requests: 20, p50NS: 250_000_000, p95NS: 250_000_000, p99NS: 250_000_000})
	store, err := prodmapsqlite.OpenReadOnly(t.Context(), filepath.Join(project, ".prodmap", "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	query := investigation.Query{Comparison: regression.Query{DeploymentID: "018f0000-0000-7000-8000-000000000000", Metric: baseline.LatencyP95, Before: 5 * time.Minute, After: 5 * time.Minute, MinSamples: 1, MinCoverage: 0.8}, GeneratedAt: deployedAt.Add(time.Hour)}
	if _, err := composeInvestigationSnapshot(t.Context(), store, query); err == nil {
		t.Fatal("composeInvestigationSnapshot succeeded with missing deployment")
	}
	// The Store has one pooled connection; this succeeds only if cleanup closed
	// the failed composition snapshot before the caller returned.
	snapshot, err := store.BeginReadSnapshot(t.Context())
	if err != nil {
		t.Fatalf("failed composition leaked its snapshot: %v", err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInvestigationTimelineEventUsesAllowlistedDeploymentAndConcurrencyDTOs(t *testing.T) {
	generatedAt := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	output := investigationTimelineEventFrom(timeline.Event{
		ID:          "event-1",
		Time:        generatedAt.Add(-time.Minute),
		Kind:        "deployment_running",
		Environment: "reference",
		Service:     "payment-api",
		Subject:     timeline.Subject{Type: "deployment", ID: "deployment-1", Name: "sensitive name"},
		Source:      timeline.Source{Kind: "ledger", ObservedAt: generatedAt.Add(-time.Minute)},
		Confidence:  timeline.Confidence{Level: "LOW", Basis: "declared deployment"},
		Deployment:  &timeline.Deployment{Status: "running", Strategy: "unknown", ProvenanceStatus: "MATCHED", ProvenanceConfidence: timeline.Confidence{Level: "LOW", Basis: "verified ledger"}},
		Runtime:     &timeline.Runtime{State: "running", Health: "healthy", RestartCount: 1, ArtifactIdentity: "sha256:sensitive"},
		Concurrency: timeline.Concurrency{Detected: true, Count: 2},
	}, generatedAt)
	if output.Deployment == nil || output.Deployment.ProvenanceConfidence.Level != "LOW" || output.Concurrency != (investigationConcurrencyOutput{Detected: true, Count: 2}) {
		t.Fatalf("output=%+v", output)
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if strings.Contains(text, "sensitive name") || strings.Contains(text, "sha256:sensitive") || !strings.Contains(text, `"concurrency":{"detected":true,"count":2}`) {
		t.Fatalf("timeline DTO exposed non-allowlisted data: %s", text)
	}
}

func TestInvestigateCLIComposesCandidateJSONAndHumanOutput(t *testing.T) {
	project := t.TempDir()
	deploymentID, deployedAt := seedRegressionCLIDataWithMetrics(t, project, regressionCLIMetrics{requests: 20, errors: 1, p50NS: 250_000_000, p95NS: 250_000_000, p99NS: 250_000_000}, regressionCLIMetrics{requests: 30, errors: 1, p50NS: 300_000_000, p95NS: 300_000_000, p99NS: 300_000_000})
	app, stdout, stderr := testApp(project)
	databasePath := filepath.Join(project, ".prodmap", "prodmap.db")
	beforeBytes, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	beforeHash := sha256.Sum256(beforeBytes)
	generatedAt := deployedAt.Add(time.Hour)
	calls := 0
	app.Now = func() time.Time {
		calls++
		return generatedAt
	}
	args := []string{"investigate", "--deployment", deploymentID, "--metric", "latency_p95", "--project-dir", project, "--json"}
	if code := app.Run(t.Context(), args); code != 0 || stderr.Len() != 0 {
		t.Fatalf("JSON code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	afterBytes, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if afterHash := sha256.Sum256(afterBytes); afterHash != beforeHash {
		t.Fatal("investigation changed the SQLite database")
	}
	envelope := decodeEnvelope(t, stdout.Bytes())
	data := envelope["data"].(map[string]any)
	if envelope["command"] != "investigate" || envelope["generated_at"] != generatedAt.Format(time.RFC3339Nano) || data["investigation_version"] != "investigation-view/v1" || data["status"] != "AVAILABLE" || data["causality_claimed"] != false || calls != 1 {
		t.Fatalf("envelope=%#v calls=%d", envelope, calls)
	}
	regression := data["regression"].(map[string]any)
	if regression["classification"].(map[string]any)["result"] != "CANDIDATE" || regression["classification"].(map[string]any)["confidence"].(map[string]any)["level"] != "LOW" {
		t.Fatalf("regression=%#v", regression)
	}
	for _, field := range []string{"accepted_window_ids", "rejected_window_ids", "graph_evidence_ids", "timeline_event_ids"} {
		if data["evidence_references"].(map[string]any)[field] == nil {
			t.Fatalf("evidence references=%#v", data["evidence_references"])
		}
	}
	if data["topology"].(map[string]any)["nodes"] == nil || data["topology"].(map[string]any)["edges"] == nil || data["timeline"].(map[string]any)["items"] == nil || data["limitations"] == nil {
		t.Fatalf("non-null arrays required: %#v", data)
	}
	if _, found := data["deployment"].(map[string]any)["external_id"]; found || strings.Contains(stdout.String(), "regression-cli") {
		t.Fatalf("investigation exposed ledger-derived external identity: %q", stdout.String())
	}
	assertRegressionOutputDoesNotLeak(t, stdout.String())
	investigationKey := data["investigation_key"]
	comparisonKey := regression["comparison_key"]
	stdout.Reset()
	stderr.Reset()
	calls = 0
	if code := app.Run(t.Context(), args); code != 0 || stderr.Len() != 0 {
		t.Fatalf("replay code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	replay := decodeEnvelope(t, stdout.Bytes())["data"].(map[string]any)
	if replay["investigation_key"] != investigationKey || replay["regression"].(map[string]any)["comparison_key"] != comparisonKey || calls != 1 {
		t.Fatalf("replay=%#v calls=%d", replay, calls)
	}

	stdout.Reset()
	stderr.Reset()
	if code := app.Run(t.Context(), args[:len(args)-1]); code != 0 || !strings.Contains(stdout.String(), "Investigation: AVAILABLE latency_p95") || !strings.Contains(stdout.String(), "Classification: CANDIDATE") || !strings.Contains(stderr.String(), "warning:") {
		t.Fatalf("human code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
