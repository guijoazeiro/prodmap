package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestEndpointsCLIJSONHumanHelpAndNoActiveWindow(t *testing.T) {
	project := t.TempDir()
	fixture, err := filepath.Abs(filepath.Join("..", "..", "testdata", "otel", "linked-services.otlp.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	app, stdout, stderr := testApp(project)
	ingest := []string{"telemetry", "ingest", "--file", fixture, "--environment", "reference", "--window-start", "2026-08-19T12:00:00Z", "--window-end", "2026-08-19T12:01:00Z", "--project-dir", project, "--json"}
	if code := app.Run(context.Background(), ingest); code != 0 {
		t.Fatalf("ingest=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	args := []string{"endpoints", "--service", "checkout", "--environment", "reference", "--at", "2026-08-19T12:00:30Z", "--project-dir", project, "--json"}
	if code := app.Run(context.Background(), args); code != 0 || stderr.Len() != 0 {
		t.Fatalf("endpoints=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	envelope := decodeEnvelope(t, stdout.Bytes())
	data := envelope["data"].(map[string]any)
	if data["telemetry_status"] != "OBSERVED" || data["service"].(map[string]any)["logical_key"] != "checkout" {
		t.Fatalf("data=%#v", data)
	}
	items := data["items"].([]any)
	if len(items) != 1 || len(items[0].(map[string]any)["windows"].([]any)) != 1 || strings.Contains(stdout.String(), "trace") || strings.Contains(stdout.String(), "span") {
		t.Fatalf("items=%#v", items)
	}
	stdout.Reset()
	stderr.Reset()
	args[6] = "2026-08-19T12:01:00Z"
	if code := app.Run(context.Background(), args); code != 0 {
		t.Fatalf("boundary=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	boundary := decodeEnvelope(t, stdout.Bytes())
	if boundary["data"].(map[string]any)["telemetry_status"] != "NO_ACTIVE_WINDOW" || len(boundary["data"].(map[string]any)["items"].([]any)) != 0 {
		t.Fatalf("boundary=%#v", boundary)
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), []string{"endpoints", "--help"}); code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "[window_start, window_end)") {
		t.Fatalf("help code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), []string{"help", "endpoints"}); code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "usage: prodmap endpoints") {
		t.Fatalf("help topic code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
