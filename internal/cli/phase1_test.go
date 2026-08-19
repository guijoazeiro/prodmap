package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/inventory"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var operationIDPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

type cliRuntimeSource struct{ observedAt time.Time }

func (s cliRuntimeSource) InspectRuntime(context.Context) (inventory.RuntimeBatch, error) {
	sha := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	observation := inventory.RuntimeObservation{
		ExternalID: "container-exact", ContainerName: "api-1", ImageReference: "registry.example.invalid/api:v1",
		ImageID:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RepoDigests: []string{"registry.example.invalid/api@sha256:1111111111111111111111111111111111111111111111111111111111111111"},
		RepoTags:    []string{"registry.example.invalid/api:v1"}, OCILabels: map[string]string{"org.opencontainers.image.revision": sha},
		State: "running", Health: "healthy", ObservedAt: s.observedAt,
	}
	return inventory.RuntimeBatch{ObservedAt: s.observedAt, Observations: []inventory.RuntimeObservation{observation}}, nil
}

type cliCommitSource struct{ observedAt time.Time }

func (s cliCommitSource) Repository(context.Context) (inventory.Repository, error) {
	return inventory.Repository{ExternalID: "repository-fixture", Name: "fixture", RootPathHash: "sha256:fixture"}, nil
}

func (s cliCommitSource) ResolveCommit(_ context.Context, revision string) (inventory.Commit, error) {
	return inventory.Commit{SHA: revision, CommitTime: s.observedAt.Add(-time.Hour), Subject: "fixture commit", TreeSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}, nil
}

func TestPhase1CLIJSONGolden(t *testing.T) {
	projectDir := t.TempDir()
	app, stdout, stderr := testApp(projectDir)
	fixed := app.Now()
	app.RuntimeSource = func() inventory.RuntimeSource { return cliRuntimeSource{observedAt: fixed} }
	app.CommitSource = func(string) inventory.CommitSource { return cliCommitSource{observedAt: fixed} }
	if code := app.Run(context.Background(), []string{"init", "--json", "--project-dir", projectDir}); code != 0 {
		t.Fatalf("init exit=%d stderr=%q", code, stderr.String())
	}

	outputs := map[string]any{}
	runJSON := func(name string, args ...string) map[string]any {
		t.Helper()
		stdout.Reset()
		stderr.Reset()
		if code := app.Run(context.Background(), args); code != 0 {
			t.Fatalf("%s exit=%d stderr=%q stdout=%q", name, code, stderr.String(), stdout.String())
		}
		value := decodeEnvelope(t, stdout.Bytes())
		outputs[name] = normalizeUUIDs(value)
		return value
	}
	runtimeEnvelope := runJSON("runtime_refresh", "runtime", "--refresh", "--json", "--project-dir", projectDir)
	runtimeData := runtimeEnvelope["data"].(map[string]any)
	runtimeItems := runtimeData["items"].([]any)
	correlationID := runtimeItems[0].(map[string]any)["correlation_id"].(string)
	runJSON("status", "status", "--json", "--project-dir", projectDir)
	runJSON("services", "services", "--json", "--project-dir", projectDir)
	runJSON("runtime", "runtime", "--json", "--project-dir", projectDir)
	runJSON("explain", "explain", correlationID, "--detail", "full", "--json", "--project-dir", projectDir)

	wantRaw, err := os.ReadFile(filepath.Join("testdata", "phase1.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var want any
	if err := json.Unmarshal(wantRaw, &want); err != nil {
		t.Fatal(err)
	}
	gotRaw, err := json.MarshalIndent(outputs, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	wantCanonical, _ := json.MarshalIndent(want, "", "  ")
	if string(gotRaw) != string(wantCanonical) {
		t.Fatalf("Phase 1 JSON golden mismatch\n--- got ---\n%s\n--- want ---\n%s", gotRaw, wantCanonical)
	}
}

func TestPhase1CLIHumanOutputAndValidation(t *testing.T) {
	projectDir := t.TempDir()
	app, stdout, stderr := testApp(projectDir)
	fixed := app.Now()
	app.RuntimeSource = func() inventory.RuntimeSource { return cliRuntimeSource{observedAt: fixed} }
	app.CommitSource = func(string) inventory.CommitSource { return cliCommitSource{observedAt: fixed} }
	if code := app.Run(context.Background(), []string{"init", "--project-dir", projectDir}); code != 0 {
		t.Fatalf("init exit=%d stderr=%q", code, stderr.String())
	}
	for _, invocation := range [][]string{
		{"runtime", "--refresh", "--project-dir", projectDir},
		{"status", "--project-dir", projectDir},
		{"services", "--project-dir", projectDir},
		{"runtime", "--project-dir", projectDir},
	} {
		stdout.Reset()
		stderr.Reset()
		if code := app.Run(context.Background(), invocation); code != 0 || stdout.Len() == 0 {
			t.Fatalf("%v exit=%d stdout=%q stderr=%q", invocation, code, stdout.String(), stderr.String())
		}
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), []string{"runtime", "--json", "--project-dir", projectDir}); code != 0 {
		t.Fatalf("runtime JSON exit=%d stderr=%q", code, stderr.String())
	}
	runtimeEnvelope := decodeEnvelope(t, stdout.Bytes())
	correlationID := runtimeEnvelope["data"].(map[string]any)["items"].([]any)[0].(map[string]any)["correlation_id"].(string)
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), []string{"explain", correlationID, "--detail", "full", "--project-dir", projectDir}); code != 0 || stdout.Len() == 0 {
		t.Fatalf("explain human exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), []string{"runtime", "--limit", "0", "--json", "--project-dir", projectDir}); code != 2 {
		t.Fatalf("invalid limit exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	if code := app.Run(context.Background(), []string{"runtime", "--at", "not-a-time", "--json", "--project-dir", projectDir}); code != 2 {
		t.Fatalf("invalid timestamp exit=%d", code)
	}
	stdout.Reset()
	if code := app.Run(context.Background(), []string{"runtime", "--cursor", "not-a-cursor", "--json", "--project-dir", projectDir}); code != 2 {
		t.Fatalf("invalid cursor exit=%d", code)
	}
}

func TestRuntimeWithoutSnapshotReportsUnknownFreshness(t *testing.T) {
	projectDir := t.TempDir()
	app, stdout, stderr := testApp(projectDir)
	if code := app.Run(context.Background(), []string{"init", "--json", "--project-dir", projectDir}); code != 0 {
		t.Fatalf("init exit=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), []string{"runtime", "--json", "--project-dir", projectDir}); code != 0 {
		t.Fatalf("runtime exit=%d stderr=%q", code, stderr.String())
	}
	envelope := decodeEnvelope(t, stdout.Bytes())
	warnings := envelope["warnings"].([]any)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %#v, want missing-snapshot warning", warnings)
	}
}

func normalizeUUIDs(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = normalizeUUIDs(item)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = normalizeUUIDs(item)
		}
		return result
	case string:
		if uuidPattern.MatchString(typed) {
			return "<uuid>"
		}
		if operationIDPattern.MatchString(typed) {
			return "<operation-id>"
		}
		return typed
	default:
		return value
	}
}
