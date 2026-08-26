package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/correlation"
	"github.com/guijoazeiro/prodmap/internal/deployment"
	"github.com/guijoazeiro/prodmap/internal/inventory"
)

func TestDeploymentLedgerReplayAndQuery(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot, err := deployment.LoadFrozenFile(context.Background(), filepath.Join("..", "..", "testdata", "deployment", "valid-two-services.jsonl"), time.Date(2026, 8, 26, 12, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.SaveDeployment(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if first.DeploymentsInserted != 2 || first.DeploymentsExisting != 0 || first.IdempotentReplay {
		t.Fatalf("first result = %#v", first)
	}
	replay, err := store.SaveDeployment(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.IdempotentReplay || replay.IngestionID != first.IngestionID || replay.DeploymentsInserted != 2 {
		t.Fatalf("replay result = %#v", replay)
	}
	result, err := store.Deployments(context.Background(), deployment.Query{Environment: "reference", Since: time.Date(2026, 8, 26, 11, 0, 0, 0, time.UTC), Until: time.Date(2026, 8, 26, 13, 0, 0, 0, time.UTC), Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.NextCursor == "" || result.Items[0].Provenance.Status != "UNKNOWN" || result.Items[0].Provenance.Confidence != "UNKNOWN" {
		t.Fatalf("first page = %#v", result)
	}
	result, err = store.Deployments(context.Background(), deployment.Query{Environment: "reference", Since: time.Date(2026, 8, 26, 11, 0, 0, 0, time.UTC), Until: time.Date(2026, 8, 26, 13, 0, 0, 0, time.UTC), Limit: 1, Cursor: result.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.NextCursor != "" {
		t.Fatalf("second page = %#v", result)
	}
}

func TestFutureDeploymentStaysUnknownWithResolvedClaims(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	snapshot, err := deployment.LoadFrozenFile(ctx, filepath.Join("..", "..", "testdata", "deployment", "valid-two-services.jsonl"), time.Date(2026, 8, 26, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	record := snapshot.Records[0]
	seedArtifactAndCommit(t, store, record, time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC))
	store.now = func() time.Time { return snapshot.ObservedAt }

	if _, err := store.SaveDeployment(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	result, err := store.Deployments(ctx, deployment.Query{Environment: record.Environment, Service: record.Service, Since: snapshot.ObservedAt.Add(-time.Hour), Until: record.DeployedAt.Add(time.Hour), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Items[0].Provenance; got.Status != "UNKNOWN" || got.Confidence != "UNKNOWN" || got.ArtifactID == nil || got.CommitID == nil {
		t.Fatalf("future deployment provenance = %#v", got)
	}
}

func seedArtifactAndCommit(t *testing.T, store *Store, record deployment.Record, at time.Time) {
	t.Helper()
	sha := record.VCSRevision
	item := inventory.SnapshotItem{
		ServiceLogicalKey:  record.Service,
		ServiceDisplayName: record.Service,
		Environment:        record.Environment,
		Runtime: inventory.RuntimeObservation{
			ExternalID: "deployment-ledger-seed", ContainerName: "deployment-ledger-seed",
			ImageReference: "seed:immutable", ImageID: record.ImageID,
			RepoDigests: []string{"seed@" + record.ImageID}, State: "running", Health: "none", ObservedAt: at,
		},
		Artifact: inventory.Artifact{Name: "seed", IdentityKind: "repo_digest", Identity: record.ImageID, DigestAlgorithm: "sha256", Digest: record.ImageID[7:], ImageID: record.ImageID, ObservedReference: "seed:immutable", OCIRevision: sha},
		Commit:   &inventory.Commit{SHA: sha, CommitTime: at.Add(-time.Minute), Subject: "seed"},
		Correlation: correlation.EvaluateProvenance(correlation.ProvenanceInput{
			ImmutableIdentity: record.ImageID, OCIRevision: sha, ResolvedCommitSHA: sha, ObservedAt: at,
		}),
	}
	if _, err := store.SaveRuntimeSnapshot(context.Background(), inventory.Snapshot{ObservedAt: at, Repository: &inventory.Repository{ExternalID: "deployment-ledger-seed", Name: "seed", RootPathHash: "seed"}, Items: []inventory.SnapshotItem{item}}); err != nil {
		t.Fatalf("seed artifact and commit: %v", err)
	}
}
