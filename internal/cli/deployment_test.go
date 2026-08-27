package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/deployment"
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

func TestDeploymentsSyncGitHubActionsUsesFactoryAndJSON(t *testing.T) {
	project := t.TempDir()
	app, stdout, stderr := testApp(project)
	contents, err := os.ReadFile(filepath.Join("..", "..", "testdata", "deployment", "valid-two-services.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	observedAt := app.Now().UTC()
	snapshot, err := deployment.LoadGitHubActionsLedger(t.Context(), contents, "acme", "prodmap", "deployment-ledger", observedAt)
	if err != nil {
		t.Fatal(err)
	}
	app.Environment["PRODMAP_GITHUB_TOKEN"] = "test-token"
	called := false
	app.GitHubDeploymentSource = func(token string) (deployment.DeploymentSource, error) {
		if token != "test-token" {
			t.Fatalf("token=%q", token)
		}
		return deploymentSourceFunc(func(ctx context.Context, query deployment.SourceQuery) (deployment.SourceResult, error) {
			called = true
			if query.ObservedAt != observedAt || query.Owner != "acme" || query.Repository != "prodmap" || query.ArtifactName != "deployment-ledger" {
				t.Fatalf("query=%#v", query)
			}
			return deployment.SourceResult{Repository: "acme/prodmap", ArtifactName: "deployment-ledger", ArtifactDigest: "sha256:" + strings.Repeat("a", 64), WorkflowHeadSHA: strings.Repeat("a", 40), ArtifactID: 1, WorkflowRunID: 2, CreatedAt: observedAt.Add(-time.Hour), UpdatedAt: observedAt.Add(-time.Minute), ExpiresAt: observedAt.Add(time.Hour), Snapshot: snapshot, Warnings: []string{}}, nil
		}), nil
	}
	if code := app.Run(t.Context(), []string{"deployments", "sync", "github-actions", "--owner", "acme", "--repository", "prodmap", "--artifact", "deployment-ledger", "--project-dir", project, "--json"}); code != 0 {
		t.Fatalf("sync exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !called || stderr.Len() != 0 {
		t.Fatalf("called=%t stderr=%q", called, stderr.String())
	}
	envelope := decodeEnvelope(t, stdout.Bytes())
	if envelope["command"] != "deployments sync github-actions" {
		t.Fatalf("sync envelope=%#v", envelope)
	}
	data := envelope["data"].(map[string]any)
	source := data["source"].(map[string]any)
	ingestion := data["ingestion"].(map[string]any)
	observation := data["source_observation"].(map[string]any)
	for _, field := range []string{"kind", "repository", "artifact_name", "artifact_id", "artifact_digest", "workflow_run_id", "workflow_head_sha", "created_at", "updated_at", "expires_at"} {
		if _, found := source[field]; !found {
			t.Fatalf("missing source.%s in %#v", field, data)
		}
	}
	for _, field := range []string{"ingestion_id", "source_hash", "format", "records_seen", "deployments_inserted", "deployments_existing", "idempotent_replay"} {
		if _, found := ingestion[field]; !found {
			t.Fatalf("missing ingestion.%s in %#v", field, data)
		}
	}
	if source["kind"] != "github_actions_artifact" || source["repository"] != "acme/prodmap" || observation["id"] == "" || observation["existing"] != false {
		t.Fatalf("sync data=%#v", data)
	}
	for _, forbidden := range []string{"test-token", "\"url\"", "\"header", "\"zip\"", "\"path\"", "\"contents\""} {
		if strings.Contains(strings.ToLower(stdout.String()), forbidden) {
			t.Fatalf("sync output exposed %q: %q", forbidden, stdout.String())
		}
	}
}

func TestDeploymentsSyncGitHubActionsFailureHasNoSideEffects(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*App)
	}{
		{name: "missing token", configure: func(app *App) {}},
		{name: "fetch failure", configure: func(app *App) {
			app.Environment["PRODMAP_GITHUB_TOKEN"] = "test-token"
			app.GitHubDeploymentSource = func(string) (deployment.DeploymentSource, error) {
				return deploymentSourceFunc(func(context.Context, deployment.SourceQuery) (deployment.SourceResult, error) {
					return deployment.SourceResult{}, context.DeadlineExceeded
				}), nil
			}
		}},
		{name: "invalid result", configure: func(app *App) {
			app.Environment["PRODMAP_GITHUB_TOKEN"] = "test-token"
			app.GitHubDeploymentSource = func(string) (deployment.DeploymentSource, error) {
				return deploymentSourceFunc(func(context.Context, deployment.SourceQuery) (deployment.SourceResult, error) {
					return deployment.SourceResult{Warnings: []string{}}, nil
				}), nil
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			project := t.TempDir()
			app, _, _ := testApp(project)
			test.configure(app)
			if code := app.Run(t.Context(), []string{"deployments", "sync", "github-actions", "--owner", "acme", "--repository", "prodmap", "--artifact", "deployment-ledger", "--project-dir", project}); code == 0 {
				t.Fatal("sync unexpectedly succeeded")
			}
			if _, err := os.Stat(filepath.Join(project, ".prodmap")); !os.IsNotExist(err) {
				t.Fatalf("failure created project state: %v", err)
			}
		})
	}
}

func TestDeploymentsSyncGitHubActionsHelpHasNoSideEffects(t *testing.T) {
	project := t.TempDir()
	app, stdout, stderr := testApp(project)
	called := false
	app.GitHubDeploymentSource = func(string) (deployment.DeploymentSource, error) {
		called = true
		return nil, nil
	}
	if code := app.Run(t.Context(), []string{"deployments", "sync", "github-actions", "--help", "--project-dir", project}); code != 0 {
		t.Fatalf("help exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if called || stderr.Len() != 0 || !strings.Contains(stdout.String(), "deployments sync github-actions") {
		t.Fatalf("called=%t stdout=%q stderr=%q", called, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(project, ".prodmap")); !os.IsNotExist(err) {
		t.Fatalf("help created project state: %v", err)
	}
}

type deploymentSourceFunc func(context.Context, deployment.SourceQuery) (deployment.SourceResult, error)

func (function deploymentSourceFunc) Fetch(ctx context.Context, query deployment.SourceQuery) (deployment.SourceResult, error) {
	return function(ctx, query)
}
