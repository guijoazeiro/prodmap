package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

type cliRuntimeBatchSource struct{ batch inventory.RuntimeBatch }

func (s cliRuntimeBatchSource) InspectRuntime(context.Context) (inventory.RuntimeBatch, error) {
	return s.batch, nil
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
	explainSummary := normalizeUUIDs(runJSON("explain_summary_actual", "explain", correlationID, "--detail", "summary", "--json", "--project-dir", projectDir))
	explainFull := normalizeUUIDs(runJSON("explain_full_actual", "explain", correlationID, "--detail", "full", "--json", "--project-dir", projectDir))
	delete(outputs, "explain_summary_actual")
	delete(outputs, "explain_full_actual")

	assertJSONGolden(t, filepath.Join("testdata", "phase1.golden.json"), outputs)
	assertJSONGolden(t, filepath.Join("testdata", "explain-summary.golden.json"), explainSummary)
	assertJSONGolden(t, filepath.Join("testdata", "explain-full.golden.json"), explainFull)
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
	for _, test := range []struct {
		invocation     []string
		stdoutContains string
		stderr         string
	}{
		{[]string{"runtime", "--refresh", "--project-dir", projectDir}, "Runtime refresh ", ""},
		{[]string{"status", "--project-dir", projectDir}, "Prodmap status\n", "warning: git is not installed\nwarning: docker is not installed\n"},
		{[]string{"services", "--project-dir", projectDir}, "api (", ""},
		{[]string{"runtime", "--project-dir", projectDir}, "service=api", ""},
	} {
		stdout.Reset()
		stderr.Reset()
		if code := app.Run(context.Background(), test.invocation); code != 0 || !strings.Contains(stdout.String(), test.stdoutContains) || stderr.String() != test.stderr {
			t.Fatalf("%v exit=%d stdout=%q stderr=%q", test.invocation, code, stdout.String(), stderr.String())
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
	if code := app.Run(context.Background(), []string{"explain", correlationID, "--detail", "full", "--project-dir", projectDir}); code != 0 || !strings.Contains(stdout.String(), "Supporting evidence:\n") || stderr.Len() != 0 {
		t.Fatalf("explain human exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), []string{"explain", correlationID, "--detail", "summary", "--project-dir", projectDir}); code != 0 || !strings.Contains(stdout.String(), "Evidence: supporting=") || strings.Contains(stdout.String(), "Supporting evidence:") || stderr.Len() != 0 {
		t.Fatalf("explain summary exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
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
	if stderr.Len() != 0 || strings.Count(stdout.String(), "No Docker runtime snapshot") != 1 {
		t.Fatalf("JSON warning streams stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if data := envelope["data"].(map[string]any); data["warnings"] != nil {
		t.Fatalf("runtime data duplicated envelope warnings: %#v", data)
	}

	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), []string{"runtime", "--project-dir", projectDir}); code != 0 {
		t.Fatalf("runtime human exit=%d", code)
	}
	if strings.Contains(stdout.String(), "warning:") || stderr.String() != "warning: No Docker runtime snapshot has been collected; run `prodmap runtime --refresh`.\n" {
		t.Fatalf("human warning streams stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestExplainAmbiguousSelectorReportsSafeCandidates(t *testing.T) {
	projectDir := t.TempDir()
	app, stdout, stderr := testApp(projectDir)
	fixed := app.Now()
	sha := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	observations := []inventory.RuntimeObservation{
		{
			ExternalID: "container-a", ContainerName: "shared-name", ImageReference: "registry.example.invalid/api:v1",
			ImageID:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			RepoDigests: []string{"registry.example.invalid/api@sha256:1111111111111111111111111111111111111111111111111111111111111111"},
			OCILabels:   map[string]string{"org.opencontainers.image.revision": sha}, State: "running", Health: "healthy", ObservedAt: fixed,
		},
		{
			ExternalID: "container-b", ContainerName: "shared-name", ImageReference: "registry.example.invalid/api:v2",
			ImageID:     "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			RepoDigests: []string{"registry.example.invalid/api@sha256:2222222222222222222222222222222222222222222222222222222222222222"},
			OCILabels:   map[string]string{"org.opencontainers.image.revision": sha}, State: "running", Health: "healthy", ObservedAt: fixed,
		},
	}
	app.RuntimeSource = func() inventory.RuntimeSource {
		return cliRuntimeBatchSource{batch: inventory.RuntimeBatch{ObservedAt: fixed, Observations: observations}}
	}
	app.CommitSource = func(string) inventory.CommitSource { return cliCommitSource{observedAt: fixed} }
	if code := app.Run(context.Background(), []string{"init", "--json", "--project-dir", projectDir}); code != 0 {
		t.Fatalf("init exit=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), []string{"runtime", "--refresh", "--json", "--project-dir", projectDir}); code != 0 {
		t.Fatalf("refresh exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), []string{"explain", "shared-name", "--json", "--project-dir", projectDir}); code != 6 {
		t.Fatalf("ambiguous JSON exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("ambiguous JSON wrote stderr: %q", stderr.String())
	}
	var envelope struct {
		Error ErrorPayload `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != ErrorCodeConflict {
		t.Fatalf("error code=%q, want %q", envelope.Error.Code, ErrorCodeConflict)
	}
	rawCandidates, ok := envelope.Error.Details["candidates"].([]any)
	if !ok || len(rawCandidates) != 2 {
		t.Fatalf("error candidates=%#v, want two", envelope.Error.Details["candidates"])
	}
	first := rawCandidates[0].(map[string]any)
	second := rawCandidates[1].(map[string]any)
	if first["type"] != "correlation" || second["type"] != "correlation" || first["id"].(string) >= second["id"].(string) {
		t.Fatalf("candidates are unsafe or nondeterministic: %#v", rawCandidates)
	}

	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), []string{"explain", "shared-name", "--project-dir", projectDir}); code != 6 {
		t.Fatalf("ambiguous human exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 || strings.Count(stderr.String(), "correlation ") < 2 || !strings.Contains(stderr.String(), "choose one candidate") {
		t.Fatalf("ambiguous human streams stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func assertJSONGolden(t *testing.T, path string, got any) {
	t.Helper()
	wantRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var want any
	if err := json.Unmarshal(wantRaw, &want); err != nil {
		t.Fatal(err)
	}
	gotRaw, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	wantCanonical, _ := json.MarshalIndent(want, "", "  ")
	if string(gotRaw) != string(wantCanonical) {
		t.Fatalf("JSON golden %s mismatch\n--- got ---\n%s\n--- want ---\n%s", path, gotRaw, wantCanonical)
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
