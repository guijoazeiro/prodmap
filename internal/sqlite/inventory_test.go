package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/correlation"
	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/inventory"
)

func TestRuntimeSnapshotIsIdempotentQueryableAndExplainable(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	snapshot := exactSnapshot(now)
	for i := 0; i < 2; i++ {
		if _, err := store.SaveRuntimeSnapshot(ctx, snapshot); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	status, err := store.Status(ctx)
	if err != nil || status.Services != 1 || status.RuntimeInstances != 1 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	items, cursor, err := store.Runtime(ctx, inventory.RuntimeQuery{At: now.Add(time.Minute), Limit: 100})
	if err != nil || cursor != "" || len(items) != 1 || items[0].Confidence != correlation.LevelExact || len(items[0].EvidenceIDs) != 3 {
		t.Fatalf("items=%+v cursor=%q err=%v", items, cursor, err)
	}
	explanation, err := store.Explain(ctx, items[0].CorrelationID)
	if err != nil || explanation.Confidence != correlation.LevelExact || len(explanation.Supporting) != 3 || explanation.Entities["commit_sha"] == "" {
		t.Fatalf("explanation=%+v err=%v", explanation, err)
	}
}

func TestRuntimeSnapshotRejectsConflictingReprocessing(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	if _, err := store.SaveRuntimeSnapshot(ctx, exactSnapshot(now)); err != nil {
		t.Fatal(err)
	}
	conflict := exactSnapshot(now)
	conflict.Items[0].Runtime.ImageID = "sha256:def"
	conflict.Items[0].Runtime.RepoDigests = []string{"api@sha256:def"}
	conflict.Items[0].Artifact.Identity = "sha256:def"
	conflict.Items[0].Artifact.Digest = "def"
	conflict.Items[0].Artifact.ImageID = "sha256:def"
	if _, err := store.SaveRuntimeSnapshot(ctx, conflict); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("conflicting reprocessing error = %v, want ErrConflict", err)
	}
	items, _, err := store.Runtime(ctx, inventory.RuntimeQuery{At: now.Add(time.Second), Limit: 10})
	if err != nil || len(items) != 1 || items[0].ArtifactIdentity != "sha256:abc" {
		t.Fatalf("runtime after conflict = %+v, err=%v", items, err)
	}
}

func TestRuntimeSnapshotRollsBackAtomically(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot := exactSnapshot(time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC))
	snapshot.Items[0].Correlation.Evidence[0].Details = map[string]string{"oversized": strings.Repeat("x", 5000)}
	if _, err := store.SaveRuntimeSnapshot(ctx, snapshot); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("SaveRuntimeSnapshot() error = %v, want ErrInvalid", err)
	}
	status, err := store.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.LastSnapshot != nil || status.Services != 0 || status.RuntimeInstances != 0 {
		t.Fatalf("failed snapshot left partial state: %+v", status)
	}
}

func TestOutOfOrderSnapshotPreservesIntervals(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	later := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	earlier := later.Add(-time.Hour)
	if _, err := store.SaveRuntimeSnapshot(ctx, exactSnapshot(later)); err != nil {
		t.Fatal(err)
	}
	earlierSnapshot := exactSnapshot(earlier)
	earlierSnapshot.Items[0].Runtime.State = "created"
	if _, err := store.SaveRuntimeSnapshot(ctx, earlierSnapshot); err != nil {
		t.Fatal(err)
	}
	oldItems, _, err := store.Runtime(ctx, inventory.RuntimeQuery{At: earlier.Add(time.Minute), Limit: 10})
	if err != nil || len(oldItems) != 1 || oldItems[0].State != "created" {
		t.Fatalf("old=%+v err=%v", oldItems, err)
	}
	newItems, _, err := store.Runtime(ctx, inventory.RuntimeQuery{At: later.Add(time.Minute), Limit: 10})
	if err != nil || len(newItems) != 1 || newItems[0].State != "running" {
		t.Fatalf("new=%+v err=%v", newItems, err)
	}
	var firstSeen, lastSeen string
	if err := store.db.QueryRow(`SELECT first_seen_at,last_seen_at FROM services WHERE logical_key='api'`).Scan(&firstSeen, &lastSeen); err != nil {
		t.Fatal(err)
	}
	if firstSeen != formatTime(earlier) || lastSeen != formatTime(later) {
		t.Fatalf("service interval = [%s, %s], want [%s, %s]", firstSeen, lastSeen, formatTime(earlier), formatTime(later))
	}
	if _, err := store.Explain(ctx, "container-1"); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("Explain(historical external ID) error = %v, want ErrConflict", err)
	}
	explanation, err := store.ExplainAt(ctx, "container-1", earlier.Add(time.Minute))
	if err != nil || explanation.TargetID != oldItems[0].CorrelationID {
		t.Fatalf("ExplainAt() = %+v, err=%v", explanation, err)
	}
}

func TestSubsecondRuntimeIntervalsUseChronologicalOrdering(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	base := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	later := base.Add(900 * time.Millisecond)
	earlier := base.Add(100 * time.Millisecond)
	if _, err := store.SaveRuntimeSnapshot(ctx, exactSnapshot(later)); err != nil {
		t.Fatal(err)
	}
	earlierSnapshot := exactSnapshot(earlier)
	earlierSnapshot.Items[0].Runtime.State = "created"
	if _, err := store.SaveRuntimeSnapshot(ctx, earlierSnapshot); err != nil {
		t.Fatal(err)
	}
	oldItems, _, err := store.Runtime(ctx, inventory.RuntimeQuery{At: base.Add(500 * time.Millisecond), Limit: 10})
	if err != nil || len(oldItems) != 1 || oldItems[0].State != "created" {
		t.Fatalf("subsecond old interval = %+v, err=%v", oldItems, err)
	}
	newItems, _, err := store.Runtime(ctx, inventory.RuntimeQuery{At: base.Add(950 * time.Millisecond), Limit: 10})
	if err != nil || len(newItems) != 1 || newItems[0].State != "running" {
		t.Fatalf("subsecond current interval = %+v, err=%v", newItems, err)
	}
}

func TestExplainRejectsAmbiguousSelector(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	snapshot := exactSnapshot(now)
	second := snapshot.Items[0]
	second.Runtime.ExternalID = "container-2"
	snapshot.Items = append(snapshot.Items, second)
	if _, err := store.SaveRuntimeSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Explain(ctx, "api-1"); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("Explain(ambiguous) error = %v, want ErrConflict", err)
	}
}

func TestRuntimeCursorBelongsToQuery(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	snapshot := exactSnapshot(now)
	second := snapshot.Items[0]
	second.Runtime.ExternalID = "container-2"
	snapshot.Items = append(snapshot.Items, second)
	if _, err := store.SaveRuntimeSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	first, cursor, err := store.Runtime(ctx, inventory.RuntimeQuery{At: now.Add(time.Second), Limit: 1})
	if err != nil || cursor == "" || len(first) != 1 {
		t.Fatalf("cursor=%q err=%v", cursor, err)
	}
	secondPage, next, err := store.Runtime(ctx, inventory.RuntimeQuery{At: now.Add(time.Second), Limit: 1, Cursor: cursor})
	if err != nil || next != "" || len(secondPage) != 1 || secondPage[0].ID == first[0].ID {
		t.Fatalf("second page=%+v next=%q err=%v", secondPage, next, err)
	}
	_, _, err = store.Runtime(ctx, inventory.RuntimeQuery{At: now.Add(time.Second), Limit: 2, Cursor: cursor})
	if !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("cursor mismatch error=%v", err)
	}
}

func TestServicesDoesNotHideMixedArtifactsOrWeakConfidence(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	snapshot := exactSnapshot(now)
	second := snapshot.Items[0]
	second.Runtime.ExternalID = "container-2"
	second.Runtime.Health = "unhealthy"
	second.Runtime.ImageID = "sha256:def"
	second.Runtime.RepoDigests = []string{"api@sha256:def"}
	second.Artifact.Identity = "sha256:def"
	second.Artifact.Digest = "def"
	second.Artifact.ImageID = "sha256:def"
	second.Artifact.OCIRevision = ""
	second.Commit = nil
	second.Correlation = correlation.EvaluateProvenance(correlation.ProvenanceInput{ImmutableIdentity: "sha256:def", ObservedAt: now})
	snapshot.Items = append(snapshot.Items, second)
	if _, err := store.SaveRuntimeSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	services, err := store.Services(ctx)
	if err != nil || len(services) != 1 {
		t.Fatalf("services=%+v err=%v", services, err)
	}
	service := services[0]
	if service.RuntimeInstances != 2 || service.Health != "mixed" || service.ArtifactIdentity != "" || service.CommitConfidence != correlation.LevelUnknown {
		t.Fatalf("mixed service summary = %+v", service)
	}
}

func exactSnapshot(now time.Time) inventory.Snapshot {
	sha := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	observation := inventory.RuntimeObservation{ExternalID: "container-1", ContainerName: "api-1", ImageReference: "api:latest", ImageID: "sha256:abc", RepoDigests: []string{"api@sha256:abc"}, State: "running", Health: "healthy", ObservedAt: now, OCILabels: map[string]string{"org.opencontainers.image.revision": sha}}
	result := correlation.EvaluateProvenance(correlation.ProvenanceInput{ImmutableIdentity: "sha256:abc", OCIRevision: sha, ResolvedCommitSHA: sha, ObservedAt: now})
	commitTime := now.Add(-time.Hour)
	return inventory.Snapshot{ObservedAt: now, Repository: &inventory.Repository{ExternalID: "repo-hash", Name: "repo", RootPathHash: "hash"}, Items: []inventory.SnapshotItem{{ServiceLogicalKey: "api", ServiceDisplayName: "api", Environment: "default", Runtime: observation, Artifact: inventory.Artifact{Name: "api", IdentityKind: "repo_digest", Identity: "sha256:abc", DigestAlgorithm: "sha256", Digest: "abc", ImageID: "sha256:abc", ObservedReference: "api:latest", Aliases: []string{"api:latest"}, OCIRevision: sha}, Commit: &inventory.Commit{SHA: sha, CommitTime: commitTime, Subject: "test"}, Correlation: result}}}
}
