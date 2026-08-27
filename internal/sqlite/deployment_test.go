package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/correlation"
	"github.com/guijoazeiro/prodmap/internal/deployment"
	"github.com/guijoazeiro/prodmap/internal/errs"
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

func TestGitHubActionsDeploymentSourceIsAtomicAndIdempotent(t *testing.T) {
	ctx := t.Context()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	source := githubActionsDeploymentSource(t, ctx)
	first, err := store.SaveDeploymentSource(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if first.SourceExisting || first.Ingestion.IdempotentReplay || first.Ingestion.DeploymentsInserted != 2 || first.SourceObservationID == "" {
		t.Fatalf("first sync=%#v", first)
	}
	replay, err := store.SaveDeploymentSource(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.SourceExisting || !replay.Ingestion.IdempotentReplay || replay.Ingestion.IngestionID != first.Ingestion.IngestionID || replay.SourceObservationID != first.SourceObservationID {
		t.Fatalf("replay sync=%#v", replay)
	}
	var ingestions, observations, deployments int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM deployment_ingestions`).Scan(&ingestions); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM github_actions_deployment_fetches`).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM deployments`).Scan(&deployments); err != nil {
		t.Fatal(err)
	}
	if ingestions != 1 || observations != 1 || deployments != 2 {
		t.Fatalf("counts ingestions=%d observations=%d deployments=%d", ingestions, observations, deployments)
	}

	conflict := source
	conflict.ArtifactDigest = "sha256:" + strings.Repeat("b", 64)
	if _, err := store.SaveDeploymentSource(ctx, conflict); err == nil {
		t.Fatal("conflicting source metadata unexpectedly persisted")
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM github_actions_deployment_fetches`).Scan(&observations); err != nil || observations != 1 {
		t.Fatalf("conflict changed source observations=%d err=%v", observations, err)
	}
}

func TestGitHubActionsArtifactIdentityBindsOneLedger(t *testing.T) {
	ctx := t.Context()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ledgerA := githubActionsDeploymentSource(t, ctx)
	first, err := store.SaveDeploymentSource(ctx, ledgerA)
	if err != nil {
		t.Fatal(err)
	}
	assertGitHubDeploymentCounts(t, store, 1, 1, 2)
	if replay, err := store.SaveDeploymentSource(ctx, ledgerA); err != nil || !replay.Ingestion.IdempotentReplay || !replay.SourceExisting {
		t.Fatalf("same artifact/same ledger replay=%#v err=%v", replay, err)
	}
	assertGitHubDeploymentCounts(t, store, 1, 1, 2)

	newArtifactSameLedger := ledgerA
	newArtifactSameLedger.ArtifactID = 43
	newArtifactSameLedger.WorkflowRunID = 44
	if result, err := store.SaveDeploymentSource(ctx, newArtifactSameLedger); err != nil || !result.Ingestion.IdempotentReplay || result.SourceExisting || result.Ingestion.IngestionID != first.Ingestion.IngestionID {
		t.Fatalf("new artifact/same ledger result=%#v err=%v", result, err)
	}
	var fetchedIngestionID string
	if err := store.db.QueryRowContext(ctx, `SELECT ingestion_id FROM github_actions_deployment_fetches WHERE artifact_id=?`, newArtifactSameLedger.ArtifactID).Scan(&fetchedIngestionID); err != nil || fetchedIngestionID != first.Ingestion.IngestionID {
		t.Fatalf("new artifact fetch ingestion=%q err=%v", fetchedIngestionID, err)
	}
	assertGitHubDeploymentCounts(t, store, 1, 2, 2)

	ledgerB := githubActionsDeploymentSource(t, ctx)
	ledgerB.ArtifactID = 44
	ledgerB.WorkflowRunID = 45
	ledgerB.Snapshot = reorderedGitHubLedger(t, ctx, ledgerB.Snapshot.ObservedAt)
	if result, err := store.SaveDeploymentSource(ctx, ledgerB); err != nil || result.Ingestion.IdempotentReplay || result.SourceExisting {
		t.Fatalf("new artifact/new ledger result=%#v err=%v", result, err)
	}
	assertGitHubDeploymentCounts(t, store, 2, 3, 2)

	conflict := ledgerB
	conflict.Snapshot = ledgerA.Snapshot
	if _, err := store.SaveDeploymentSource(ctx, conflict); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("same artifact/different ledger error=%v", err)
	}
	assertGitHubDeploymentCounts(t, store, 2, 3, 2)
}

func reorderedGitHubLedger(t *testing.T, ctx context.Context, observedAt time.Time) deployment.Snapshot {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", "testdata", "deployment", "valid-two-services.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(contents), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("fixture lines=%d", len(lines))
	}
	return mustLoadGitHubLedger(t, ctx, []byte(lines[1]+"\n"+lines[0]+"\n"), observedAt)
}

func mustLoadGitHubLedger(t *testing.T, ctx context.Context, contents []byte, observedAt time.Time) deployment.Snapshot {
	t.Helper()
	snapshot, err := deployment.LoadGitHubActionsLedger(ctx, contents, "acme", "prodmap", "deployment-ledger", observedAt)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func assertGitHubDeploymentCounts(t *testing.T, store *Store, wantIngestions, wantFetches, wantDeployments int) {
	t.Helper()
	var ingestions, fetches, deployments int
	if err := store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM deployment_ingestions`).Scan(&ingestions); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM github_actions_deployment_fetches`).Scan(&fetches); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM deployments`).Scan(&deployments); err != nil {
		t.Fatal(err)
	}
	if ingestions != wantIngestions || fetches != wantFetches || deployments != wantDeployments {
		t.Fatalf("counts ingestions=%d fetches=%d deployments=%d want=%d,%d,%d", ingestions, fetches, deployments, wantIngestions, wantFetches, wantDeployments)
	}
}

func githubActionsDeploymentSource(t *testing.T, ctx context.Context) deployment.SourceResult {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", "testdata", "deployment", "valid-two-services.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 26, 12, 1, 0, 0, time.UTC)
	snapshot := mustLoadGitHubLedger(t, ctx, contents, at)
	return deployment.SourceResult{
		Repository: "acme/prodmap", ArtifactName: "deployment-ledger", ArtifactDigest: "sha256:" + strings.Repeat("a", 64), WorkflowHeadSHA: strings.Repeat("a", 40), ArtifactID: 42, WorkflowRunID: 43,
		CreatedAt: at.Add(-time.Hour), UpdatedAt: at.Add(-time.Minute), ExpiresAt: at.Add(24 * time.Hour), Snapshot: snapshot, Warnings: []string{},
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

func TestRuntimeCandidateValidityUsesHalfOpenInterval(t *testing.T) {
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	end := at.Add(time.Minute)
	candidate := deployment.RuntimeCandidate{ValidFrom: at, ValidTo: &end}
	if !runtimeCandidateValidAt(candidate, at) || runtimeCandidateValidAt(candidate, end) {
		t.Fatalf("half-open runtime validity was not honored: %#v", candidate)
	}
}

func TestDeploymentRuntimeAssociationEnrichesWithoutReingestAndHonorsBoundary(t *testing.T) {
	ctx := t.Context()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot, err := deployment.LoadFrozenFile(ctx, filepath.Join("..", "..", "testdata", "deployment", "valid-two-services.jsonl"), time.Date(2026, 8, 26, 12, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveDeployment(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	record := snapshot.Records[0]
	query := deployment.Query{Environment: record.Environment, Service: record.Service, Since: record.DeployedAt.Add(-time.Minute), Until: record.DeployedAt.Add(time.Hour), Limit: 10}
	before, err := store.Deployments(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Items) != 1 || before.Items[0].RuntimeAssociation.Status != "UNKNOWN" {
		t.Fatalf("association before runtime = %#v", before.Items)
	}
	stateBefore := deploymentRowState(t, store, before.Items[0].ID)
	observedAt := record.DeployedAt.Add(time.Second)
	seedArtifactAndCommit(t, store, record, observedAt)
	after, err := store.Deployments(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	association := after.Items[0].RuntimeAssociation
	if association.Status != "MATCHED" || association.Confidence != "HIGH" || len(association.RuntimeInstances) != 1 || association.RuntimeInstances == nil || association.Evidence == nil || association.Limitations == nil {
		t.Fatalf("association after runtime = %#v", association)
	}
	if stateAfter := deploymentRowState(t, store, after.Items[0].ID); stateAfter != stateBefore {
		t.Fatalf("runtime refresh modified deployment row: before=%q after=%q", stateBefore, stateAfter)
	}
	exact := query
	exact.Until = observedAt
	boundary, err := store.Deployments(ctx, exact)
	if err != nil {
		t.Fatal(err)
	}
	if boundary.Items[0].RuntimeAssociation.Status == "MATCHED" || boundary.Items[0].RuntimeAssociation.CandidateInstances != 0 {
		t.Fatalf("exact boundary included runtime observation: %#v", boundary.Items[0].RuntimeAssociation)
	}
	exact.Until = observedAt.Add(time.Nanosecond)
	plusOne, err := store.Deployments(ctx, exact)
	if err != nil {
		t.Fatal(err)
	}
	if plusOne.Items[0].RuntimeAssociation.Status != "MATCHED" || plusOne.Items[0].RuntimeAssociation.Confidence != "HIGH" {
		t.Fatalf("boundary plus one = %#v", plusOne.Items[0].RuntimeAssociation)
	}
	var persisted int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name LIKE '%runtime_association%'`).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != 0 {
		t.Fatalf("runtime association was persisted in %d tables", persisted)
	}
}

func deploymentRowState(t *testing.T, store *Store, id string) string {
	t.Helper()
	var firstIngestion, fingerprint, createdAt, updatedAt string
	if err := store.db.QueryRowContext(t.Context(), `SELECT first_ingestion_id,record_fingerprint,created_at,updated_at FROM deployments WHERE id=?`, id).Scan(&firstIngestion, &fingerprint, &createdAt, &updatedAt); err != nil {
		t.Fatal(err)
	}
	return strings.Join([]string{firstIngestion, fingerprint, createdAt, updatedAt}, "|")
}

func TestDeploymentRuntimeCandidateTemporalFilterAndLimit(t *testing.T) {
	ctx := t.Context()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot, err := deployment.LoadFrozenFile(ctx, filepath.Join("..", "..", "testdata", "deployment", "valid-two-services.jsonl"), time.Date(2026, 8, 26, 12, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveDeployment(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	record := snapshot.Records[0]
	insertRuntimeCandidates(t, store, record, record.DeployedAt.Add(-3*time.Hour), record.DeployedAt.Add(-2*time.Hour), deployment.MaxRuntimeCandidatesPerPage+1, true)
	insertRuntimeCandidates(t, store, record, record.DeployedAt.Add(time.Second), time.Time{}, 1, false)
	query := deployment.Query{Environment: record.Environment, Service: record.Service, Since: record.DeployedAt.Add(-time.Minute), Until: record.DeployedAt.Add(time.Hour), Limit: 10}
	result, err := store.Deployments(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].RuntimeAssociation.Status != "MATCHED" || result.Items[0].RuntimeAssociation.Confidence != "HIGH" || result.Items[0].RuntimeAssociation.CandidateInstances != 1 {
		t.Fatalf("expired candidates affected association: %#v", result.Items)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM runtime_instances`); err != nil {
		t.Fatal(err)
	}
	insertRuntimeCandidates(t, store, record, record.DeployedAt.Add(time.Second), time.Time{}, deployment.MaxRuntimeCandidatesPerDeployment+1, false)
	result, err = store.Deployments(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	association := result.Items[0].RuntimeAssociation
	if association.Status != "UNKNOWN" || association.Confidence != "UNKNOWN" || association.CandidateInstances != 0 || !strings.Contains(strings.Join(association.Limitations, " "), "candidate limit exceeded") {
		t.Fatalf("relevant candidates above limit = %#v", association)
	}
}

func TestRuntimeConfirmationWindowTiePrecedence(t *testing.T) {
	start := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	cap := start.Add(deployment.MaxRuntimeConfirmationDelay)
	for _, test := range []struct {
		name        string
		next, until time.Time
		wantReason  string
	}{
		{name: "next equals query until", next: start.Add(10 * time.Minute), until: start.Add(10 * time.Minute), wantReason: "next_deployment"},
		{name: "query until equals cap", until: cap, wantReason: "query_until"},
		{name: "next equals cap", next: cap, until: cap.Add(time.Minute), wantReason: "next_deployment"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var next *time.Time
			if !test.next.IsZero() {
				next = new(test.next)
			}
			end, reason := runtimeConfirmationWindow(start, next, test.until)
			if !end.Equal(test.next) && !end.Equal(test.until) {
				t.Fatalf("window end=%s", end)
			}
			if reason != test.wantReason {
				t.Fatalf("reason=%q want %q", reason, test.wantReason)
			}
		})
	}
}

func insertRuntimeCandidates(t *testing.T, store *Store, record deployment.Record, start, end time.Time, count int, expired bool) {
	t.Helper()
	ctx := t.Context()
	now := formatTime(record.DeployedAt)
	if _, err := store.db.ExecContext(ctx, `INSERT OR IGNORE INTO sources(id,kind,name,instance_key,last_status,created_at,updated_at) VALUES('candidate-source','docker','candidate-source','candidate-source','success',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT OR IGNORE INTO services(id,logical_key,environment,display_name,first_seen_at,last_seen_at,created_at,updated_at) VALUES('candidate-service',?,?,?,?,?,?,?)`, record.Service, record.Environment, record.Service, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT OR IGNORE INTO artifacts(id,source_id,kind,name,identity_kind,identity,digest_algorithm,digest,image_id,observed_reference,oci_labels_json,observed_at,ingested_at,created_at,updated_at) VALUES('candidate-artifact','candidate-source','container_image','candidate','image_id',?,'sha256',?,?, 'candidate:immutable','{}',?,?,?,?)`, record.ImageID, strings.TrimPrefix(record.ImageID, "sha256:"), record.ImageID, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	statement, err := tx.PrepareContext(ctx, `INSERT INTO runtime_instances(id,source_id,external_id,container_name,service_id,artifact_id,runtime_kind,state,health,restart_count,started_at,valid_from,valid_to,observed_at,ingested_at,image_reference,image_id,image_digest,created_at,updated_at) VALUES(?,?,?,?,?,'candidate-artifact','docker_container','exited','none',0,?,?,?,?,?,'candidate:immutable',?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer statement.Close()
	prefix := strconv.FormatInt(start.UnixNano(), 10)
	for index := range count {
		at := start.Add(time.Duration(index) * time.Nanosecond)
		var validTo any
		if expired {
			validTo = formatTime(end)
		}
		if _, err := statement.ExecContext(ctx, "candidate-runtime-"+prefix+"-"+strconv.Itoa(index), "candidate-source", "candidate-external-"+prefix+"-"+strconv.Itoa(index), "candidate", "candidate-service", formatTime(at), formatTime(at), validTo, formatTime(at), now, record.ImageID, record.ImageID, now, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
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
