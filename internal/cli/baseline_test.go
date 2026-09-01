package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	prodmapsqlite "github.com/guijoazeiro/prodmap/internal/sqlite"
	"github.com/guijoazeiro/prodmap/internal/telemetry"
)

func TestBaselineCLIJSONHumanHelpAndInputValidation(t *testing.T) {
	project := t.TempDir()
	if err := seedBaselineCLIWindow(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	app, stdout, stderr := testApp(project)
	app.Now = func() time.Time { return time.Date(2026, 8, 19, 12, 2, 0, 0, time.UTC) }
	args := []string{"baseline", "--service", "checkout", "--environment", "reference", "--metric", "request_count", "--at", "2026-08-19T12:01:00Z", "--window", "5m", "--project-dir", project, "--json"}
	if code := app.Run(context.Background(), args); code != 0 || stderr.Len() != 0 {
		t.Fatalf("json=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	envelope := decodeEnvelope(t, stdout.Bytes())
	data := envelope["data"].(map[string]any)
	if envelope["command"] != "baseline" || data["status"] != "AVAILABLE" || data["confidence"].(map[string]any)["level"] != "LOW" || data["causality_claimed"] != false || data["accepted_windows"] == nil || data["rejected_windows"] == nil {
		t.Fatalf("data=%#v", data)
	}
	if rendered := stdout.String(); strings.Contains(rendered, "NaN") || strings.Contains(rendered, "Inf") || strings.Contains(rendered, "HIGH") || strings.Contains(rendered, "EXACT") {
		t.Fatalf("unsafe baseline JSON=%q", rendered)
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), args[:len(args)-1]); code != 0 || !strings.Contains(stdout.String(), "Baseline: AVAILABLE request_count") || !strings.Contains(stdout.String(), "Confidence: LOW") || !strings.Contains(stderr.String(), "known deployment contamination checks") {
		t.Fatalf("human=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), []string{"baseline", "--help"}); code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "usage: prodmap baseline") {
		t.Fatalf("help=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), []string{"baseline", "--service", "checkout", "--metric", "unknown", "--at", "2026-08-19T12:01:00Z", "--project-dir", project, "--json"}); code == 0 || stderr.Len() != 0 {
		t.Fatalf("invalid=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(project, ".prodmap", "prodmap.db")); err != nil {
		t.Fatal(err)
	}
}

func TestBaselineMissingAtDoesNotOpenSQLite(t *testing.T) {
	project := t.TempDir()
	app, stdout, stderr := testApp(project)
	code := app.Run(t.Context(), []string{"baseline", "--service", "checkout", "--metric", "request_count", "--json", "--project-dir", project})
	if code == 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(project, ".prodmap")); !os.IsNotExist(err) {
		t.Fatalf("missing --at created state: %v", err)
	}
}

func seedBaselineCLIWindow(ctx context.Context, project string) error {
	store, err := prodmapsqlite.Open(ctx, filepath.Join(project, ".prodmap", "prodmap.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	start := time.Date(2026, 8, 19, 11, 56, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)
	coverage := .9
	_, err = store.SaveTelemetry(ctx, telemetry.Snapshot{
		SourceKey:   "file:baseline-cli",
		SourceHash:  "sha256:" + strings.Repeat("a", 64),
		Environment: "reference",
		WindowStart: start,
		WindowEnd:   end,
		ObservedAt:  end,
		Stats:       telemetry.Stats{Services: 1, TelemetryWindows: 1},
		Services:    []telemetry.Service{{LogicalKey: "checkout", DisplayName: "checkout"}},
		Windows:     []telemetry.Window{{Key: "checkout\x00", ServiceKey: "checkout", WindowStart: start, WindowEnd: end, RequestCount: 12, P50NS: 10, P95NS: 20, P99NS: 30, CoverageRatio: &coverage, IsComplete: true}},
	})
	return err
}
