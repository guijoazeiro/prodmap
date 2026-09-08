package cli

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPackageCreateAndVerifyJSONAreReadOnlyAndRedacted(t *testing.T) {
	project := t.TempDir()
	deploymentID, deployedAt := seedRegressionCLIDataWithMetrics(t, project, regressionCLIMetrics{requests: 20, errors: 1, p50NS: 250_000_000, p95NS: 250_000_000, p99NS: 250_000_000}, regressionCLIMetrics{requests: 30, errors: 1, p50NS: 300_000_000, p95NS: 300_000_000, p99NS: 300_000_000})
	databasePath := filepath.Join(project, ".prodmap", "prodmap.db")
	before, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	beforeHash := sha256.Sum256(before)
	outputPath := filepath.Join(t.TempDir(), "investigation.zip")
	generatedAt := deployedAt.Add(time.Hour)
	app, stdout, stderr := testApp(project)
	calls := 0
	app.Now = func() time.Time { calls++; return generatedAt }
	createArgs := []string{"package", "create", "--deployment", deploymentID, "--metric", "latency_p95", "--output", outputPath, "--project-dir", project, "--json"}
	if code := app.Run(t.Context(), createArgs); code != 0 || stderr.Len() != 0 || calls != 1 {
		t.Fatalf("create code=%d stdout=%q stderr=%q calls=%d", code, stdout.String(), stderr.String(), calls)
	}
	after, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if afterHash := sha256.Sum256(after); afterHash != beforeHash {
		t.Fatal("package create changed SQLite")
	}
	created := decodeEnvelope(t, stdout.Bytes())
	data := created["data"].(map[string]any)
	if created["command"] != "package create" || created["generated_at"] != generatedAt.Format(time.RFC3339Nano) || data["file_name"] != "investigation.zip" || data["investigation_key"] == "" || strings.Contains(stdout.String(), outputPath) {
		t.Fatalf("create envelope=%#v", created)
	}
	assertRegressionOutputDoesNotLeak(t, stdout.String())
	assertPackageContentsAreRedacted(t, outputPath)
	firstKey := data["investigation_key"]

	stdout.Reset()
	stderr.Reset()
	calls = 0
	verifyArgs := []string{"package", "verify", "--file", outputPath, "--json"}
	if code := app.Run(t.Context(), verifyArgs); code != 0 || stderr.Len() != 0 || calls != 1 {
		t.Fatalf("verify code=%d stdout=%q stderr=%q calls=%d", code, stdout.String(), stderr.String(), calls)
	}
	verified := decodeEnvelope(t, stdout.Bytes())
	verifiedData := verified["data"].(map[string]any)
	if verified["command"] != "package verify" || verifiedData["investigation_key"] != firstKey || strings.Contains(stdout.String(), outputPath) {
		t.Fatalf("verify envelope=%#v", verified)
	}

	secondPath := filepath.Join(t.TempDir(), "replay.zip")
	// Reuse the semantic query with a distinct target to prove replay stability.
	stdout.Reset()
	stderr.Reset()
	calls = 0
	replayArgs := []string{"package", "create", "--deployment", deploymentID, "--metric", "latency_p95", "--output", secondPath, "--project-dir", project, "--json"}
	if code := app.Run(t.Context(), replayArgs); code != 0 || stderr.Len() != 0 || calls != 1 {
		t.Fatalf("replay code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if replay := decodeEnvelope(t, stdout.Bytes())["data"].(map[string]any); replay["investigation_key"] != firstKey {
		t.Fatalf("replay=%#v first=%q", replay, firstKey)
	}
}

func TestPackageHelpInvalidArgumentsAndVerifyDoNotOpenInventory(t *testing.T) {
	project := t.TempDir()
	app, stdout, stderr := testApp(project)
	for _, args := range [][]string{{"package", "--help"}, {"package", "create", "--help"}, {"package", "verify", "--help"}} {
		stdout.Reset()
		stderr.Reset()
		if code := app.Run(t.Context(), args); code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "usage: prodmap package") {
			t.Fatalf("help args=%v code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(t.Context(), []string{"package", "create", "--deployment", "bad", "--metric", "invalid", "--output", filepath.Join(project, "x.zip"), "--project-dir", project, "--json"}); code == 0 || stderr.Len() != 0 {
		t.Fatalf("invalid code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(project, ".prodmap")); !os.IsNotExist(err) {
		t.Fatalf("invalid package create opened inventory: %v", err)
	}

	packagePath := filepath.Join(t.TempDir(), "not-a-package.zip")
	if err := os.WriteFile(packagePath, []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(t.Context(), []string{"package", "verify", "--file", packagePath, "--json"}); code == 0 || stderr.Len() != 0 {
		t.Fatalf("verify code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(project, ".prodmap")); !os.IsNotExist(err) {
		t.Fatalf("package verify opened inventory: %v", err)
	}
}

func TestPackageCreateReadOnlyOpenDoesNotCreateAbsentInventory(t *testing.T) {
	project := t.TempDir()
	app, _, _ := testApp(project)
	output := filepath.Join(t.TempDir(), "investigation.zip")
	args := []string{"package", "create", "--deployment", "018f0000-0000-7000-8000-000000000000", "--metric", "latency_p95", "--output", output, "--project-dir", project}
	if code := app.Run(t.Context(), args); code == 0 {
		t.Fatal("package create opened an absent inventory")
	}
	if _, err := os.Stat(filepath.Join(project, ".prodmap")); !os.IsNotExist(err) {
		t.Fatalf("package create created absent inventory: %v", err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("package create wrote output after inventory open failed: %v", err)
	}
}

func assertPackageContentsAreRedacted(t *testing.T, path string) {
	t.Helper()
	archive, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	for _, file := range archive.File {
		if file.Name != "investigation.json" {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		var buffer bytes.Buffer
		if _, err := buffer.ReadFrom(reader); err != nil {
			t.Fatal(err)
		}
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"external_id", "git_head", "vcs_revision", "image_reference", "image_id", "artifact_identity", "Bearer", "Authorization", "regression-cli"} {
			if strings.Contains(buffer.String(), forbidden) {
				t.Fatalf("package leaked %q: %s", forbidden, buffer.String())
			}
		}
		return
	}
	t.Fatal("investigation.json missing")
}
