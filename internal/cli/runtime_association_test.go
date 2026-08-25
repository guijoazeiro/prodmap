package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/inventory"
)

func TestRuntimeObservedServiceAssociationCLI(t *testing.T) {
	fixture, err := filepath.Abs(filepath.Join("..", "..", "testdata", "otel", "linked-services.otlp.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	app, stdout, stderr := testApp(project)
	observedAt := app.Now()
	app.RuntimeSource = func() inventory.RuntimeSource {
		return cliRuntimeBatchSource{batch: associationRuntimeBatch(observedAt, true)}
	}
	app.CommitSource = func(string) inventory.CommitSource { return cliCommitSource{observedAt: observedAt} }
	run := func(args ...string) map[string]any {
		t.Helper()
		stdout.Reset()
		stderr.Reset()
		if code := app.Run(context.Background(), args); code != 0 {
			t.Fatalf("%v exit=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
		return decodeEnvelope(t, stdout.Bytes())
	}
	run("init", "--project-dir", project, "--json")
	run("telemetry", "ingest", "--file", fixture, "--window-start", "2026-08-19T12:00:00Z", "--window-end", "2026-08-19T12:01:00Z", "--environment", "reference", "--project-dir", project, "--json")
	run("runtime", "--refresh", "--environment", "reference", "--project-dir", project, "--json")
	services := run("services", "--project-dir", project, "--json")
	for _, key := range []string{"checkout", "payment"} {
		service := serviceOutputByKey(t, services, key)
		if service["environment"] != "reference" || service["telemetry_observed"] != true {
			t.Fatalf("service %s=%#v", key, service)
		}
		association := service["runtime_association"].(map[string]any)
		if association["status"] != "MATCHED" || association["confidence"] != "HIGH" || association["current_instances"] != float64(1) || association["matched_instances"] != float64(1) || association["unverified_instances"] != float64(0) || len(association["limitations"].([]any)) != 0 {
			t.Fatalf("association %s=%#v", key, association)
		}
	}
	graph := run("graph", "--service", "checkout", "--environment", "reference", "--at", "2026-08-19T12:00:02.5Z", "--project-dir", project, "--json")
	if edges := graphEdges(t, graph); len(edges) != 1 || confidenceLevel(edges[0]) != "HIGH" {
		t.Fatalf("graph edges=%#v", edges)
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), []string{"services", "--project-dir", project}); code != 0 || !strings.Contains(stdout.String(), "runtime_association=MATCHED/HIGH matched=1 unverified=0") || stderr.Len() != 0 {
		t.Fatalf("human services exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), []string{"runtime", "--refresh", "--environment=", "--project-dir", project, "--json"}); code != 2 || stderr.Len() != 0 {
		t.Fatalf("empty environment exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	negativeProject := t.TempDir()
	negative, negativeOut, negativeErr := testApp(negativeProject)
	negative.RuntimeSource = func() inventory.RuntimeSource {
		return cliRuntimeBatchSource{batch: associationRuntimeBatch(observedAt, false)}
	}
	negative.CommitSource = func(string) inventory.CommitSource { return cliCommitSource{observedAt: observedAt} }
	for _, args := range [][]string{
		{"init", "--project-dir", negativeProject, "--json"},
		{"telemetry", "ingest", "--file", fixture, "--window-start", "2026-08-19T12:00:00Z", "--window-end", "2026-08-19T12:01:00Z", "--environment", "reference", "--project-dir", negativeProject, "--json"},
		{"runtime", "--refresh", "--environment", "reference", "--project-dir", negativeProject, "--json"},
		{"services", "--project-dir", negativeProject, "--json"},
	} {
		negativeOut.Reset()
		negativeErr.Reset()
		if code := negative.Run(context.Background(), args); code != 0 {
			t.Fatalf("negative %v exit=%d stdout=%q stderr=%q", args, code, negativeOut.String(), negativeErr.String())
		}
	}
	checkout := serviceOutputByKey(t, decodeEnvelope(t, negativeOut.Bytes()), "checkout")
	association := checkout["runtime_association"].(map[string]any)
	if association["status"] != "UNKNOWN" || association["confidence"] != "UNKNOWN" || association["basis"] != "runtime identity lacks allowlisted OCI title evidence" {
		t.Fatalf("fallback-only association=%#v", association)
	}
}

func associationRuntimeBatch(at time.Time, title bool) inventory.RuntimeBatch {
	items := make([]inventory.RuntimeObservation, 0, 2)
	for index, key := range []string{"checkout", "payment"} {
		labels := map[string]string{}
		if title {
			labels["org.opencontainers.image.title"] = key
		}
		digest := strings.Repeat(string(rune('a'+index)), 64)
		items = append(items, inventory.RuntimeObservation{ExternalID: key + "-runtime", ContainerName: "not-evidence", ImageReference: "registry.example/" + key + ":v1", ImageID: "sha256:" + digest, RepoDigests: []string{key + "@sha256:" + digest}, OCILabels: labels, State: "running", Health: "healthy", ObservedAt: at})
	}
	return inventory.RuntimeBatch{ObservedAt: at, Observations: items}
}

func serviceOutputByKey(t *testing.T, envelope map[string]any, key string) map[string]any {
	t.Helper()
	for _, item := range envelope["data"].(map[string]any)["items"].([]any) {
		service := item.(map[string]any)
		if service["logical_key"] == key {
			return service
		}
	}
	t.Fatalf("service %q not found in %#v", key, envelope)
	return nil
}
