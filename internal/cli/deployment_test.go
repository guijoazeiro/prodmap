package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDeploymentsHumanOutputUsesAssociationConfidenceLevel(t *testing.T) {
	project := t.TempDir()
	app, stdout, stderr := testApp(project)
	ledger, err := filepath.Abs(filepath.Join("..", "..", "testdata", "deployment", "valid-two-services.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if code := app.Run(t.Context(), []string{"init", "--project-dir", project}); code != 0 {
		t.Fatalf("init exit=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(t.Context(), []string{"deployments", "ingest", "--file", ledger, "--project-dir", project}); code != 0 {
		t.Fatalf("ingest exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(t.Context(), []string{"deploys", "--environment", "reference", "--since", "2026-08-26T11:00:00Z", "--until", "2026-08-26T13:00:00Z", "--project-dir", project}); code != 0 {
		t.Fatalf("deploys exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "%!s") || !strings.Contains(stdout.String(), "UNKNOWN  UNKNOWN") || stderr.Len() != 0 {
		t.Fatalf("human deploys stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
