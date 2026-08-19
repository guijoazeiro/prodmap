package sqlite

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/correlation"
	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/inventory"
)

const (
	testImageDigestA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testImageDigestB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
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
	conflict.Items[0].Runtime.ImageID = testImageDigestB
	conflict.Items[0].Runtime.RepoDigests = []string{"api@" + testImageDigestB}
	conflict.Items[0].Artifact.Identity = testImageDigestB
	conflict.Items[0].Artifact.Digest = strings.TrimPrefix(testImageDigestB, "sha256:")
	conflict.Items[0].Artifact.ImageID = testImageDigestB
	if _, err := store.SaveRuntimeSnapshot(ctx, conflict); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("conflicting reprocessing error = %v, want ErrConflict", err)
	}
	items, _, err := store.Runtime(ctx, inventory.RuntimeQuery{At: now.Add(time.Second), Limit: 10})
	if err != nil || len(items) != 1 || items[0].ArtifactIdentity != testImageDigestA {
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

func TestArtifactDigestConstraintRejectsInvalidAlgorithmLength(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot := exactSnapshot(time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC))
	snapshot.Items[0].Artifact.Identity = "sha256:short"
	snapshot.Items[0].Artifact.Digest = "short"
	if _, err := store.SaveRuntimeSnapshot(ctx, snapshot); err == nil {
		t.Fatal("SaveRuntimeSnapshot() accepted an invalid sha256 artifact digest")
	}
	status, err := store.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.LastSnapshot != nil || status.Services != 0 || status.RuntimeInstances != 0 {
		t.Fatalf("invalid artifact digest left partial state: %+v", status)
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
	} else {
		var ambiguous *inventory.AmbiguousSelectorError
		if !errors.As(err, &ambiguous) || len(ambiguous.Candidates) != 2 {
			t.Fatalf("Explain(ambiguous) error = %#v, want two safe candidates", err)
		}
		for _, candidate := range ambiguous.Candidates {
			if candidate.Type != "correlation" || candidate.ID == "" || candidate.DisplayLabel != "correlation "+candidate.ID {
				t.Fatalf("unsafe or incomplete candidate = %+v", candidate)
			}
		}
	}
}

func TestCorrelationReprocessingIsIdempotentAndOrderIndependent(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	exact := exactSnapshot(now)
	unavailable := exactSnapshot(now)
	unavailable.Items[0].Commit = nil
	unavailable.Items[0].Correlation = correlation.EvaluateProvenance(correlation.ProvenanceInput{
		ImmutableIdentity: testImageDigestA,
		OCIRevision:       "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RevisionState:     correlation.RevisionSourceUnavailable,
		ObservedAt:        now,
	})
	unknown := exactSnapshot(now)
	unknown.Items[0].Commit = nil
	unknown.Items[0].Artifact.OCIRevision = ""
	unknown.Items[0].Artifact.OCILabels = map[string]string{}
	unknown.Items[0].Runtime.OCILabels = map[string]string{}
	unknown.Items[0].Correlation = correlation.EvaluateProvenance(correlation.ProvenanceInput{ImmutableIdentity: testImageDigestA, ObservedAt: now})
	low := exactSnapshot(now)
	low.Items[0].Runtime.ImageID = ""
	low.Items[0].Runtime.RepoDigests = nil
	low.Items[0].Artifact, _ = inventory.NormalizeArtifact(low.Items[0].Runtime)
	low.Items[0].Correlation = correlation.EvaluateProvenance(correlation.ProvenanceInput{
		MutableAlias: low.Items[0].Artifact.ObservedReference,
		OCIRevision:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ResolvedCommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RevisionState: correlation.RevisionResolved, ObservedAt: now,
	})

	for _, weakCase := range []struct {
		name     string
		snapshot inventory.Snapshot
	}{{"unavailable", unavailable}, {"unknown", unknown}, {"low", low}} {
		for _, order := range []struct {
			name      string
			snapshots []inventory.Snapshot
		}{
			{name: weakCase.name + " then exact", snapshots: []inventory.Snapshot{weakCase.snapshot, exact}},
			{name: "exact then " + weakCase.name, snapshots: []inventory.Snapshot{exact, weakCase.snapshot}},
		} {
			t.Run(order.name, func(t *testing.T) {
				ctx := context.Background()
				store, err := Open(ctx, filepath.Join(t.TempDir(), "prodmap.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				store.now = func() time.Time { return now.Add(time.Hour) }
				for _, snapshot := range order.snapshots {
					if _, err := store.SaveRuntimeSnapshot(ctx, snapshot); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := store.SaveRuntimeSnapshot(ctx, order.snapshots[len(order.snapshots)-1]); err != nil {
					t.Fatal(err)
				}
				items, _, err := store.Runtime(ctx, inventory.RuntimeQuery{At: now.Add(time.Second), Limit: 10})
				if err != nil || len(items) != 1 || items[0].Confidence != correlation.LevelExact || items[0].ArtifactIdentity != testImageDigestA {
					t.Fatalf("current runtime = %+v, err=%v", items, err)
				}
				var derivations, current, duplicateEvidence int
				if err := store.db.QueryRow(`SELECT COUNT(*),SUM(is_current) FROM correlations`).Scan(&derivations, &current); err != nil {
					t.Fatal(err)
				}
				if derivations != 2 || current != 1 {
					t.Fatalf("derivations=%d current=%d, want 2 and 1", derivations, current)
				}
				if err := store.db.QueryRow(`SELECT COUNT(*) FROM (SELECT correlation_id,ordinal,COUNT(*) n FROM evidence GROUP BY correlation_id,ordinal HAVING n>1)`).Scan(&duplicateEvidence); err != nil {
					t.Fatal(err)
				}
				if duplicateEvidence != 0 {
					t.Fatalf("duplicate evidence groups = %d", duplicateEvidence)
				}
			})
		}
	}
}

type reprocessingRuntimeSource struct {
	observation inventory.RuntimeObservation
}

func (s reprocessingRuntimeSource) InspectRuntime(context.Context) (inventory.RuntimeBatch, error) {
	return inventory.RuntimeBatch{ObservedAt: s.observation.ObservedAt, Observations: []inventory.RuntimeObservation{s.observation}}, nil
}

type reprocessingCommitSource struct {
	observedAt time.Time
}

func (s reprocessingCommitSource) Repository(context.Context) (inventory.Repository, error) {
	return inventory.Repository{ExternalID: "repo", Name: "repo"}, nil
}

func (s reprocessingCommitSource) ResolveCommit(_ context.Context, revision string) (inventory.Commit, error) {
	return inventory.Commit{SHA: revision, CommitTime: s.observedAt.Add(-time.Hour), Subject: "fixture"}, nil
}

func TestRefresherReprocessingWithRealLowAndUnavailableInputsIsOrderIndependent(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	sha := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	exactObservation := inventory.RuntimeObservation{
		ExternalID: "container-1", ContainerName: "api-1", ImageReference: "api:latest",
		ImageID: testImageDigestA, RepoDigests: []string{"api@" + testImageDigestA}, RepoTags: []string{"api:latest"},
		OCILabels: map[string]string{"org.opencontainers.image.revision": sha}, State: "running", Health: "healthy", ObservedAt: now,
	}
	lowObservation := exactObservation
	lowObservation.ImageID = ""
	lowObservation.RepoDigests = nil
	commitSource := reprocessingCommitSource{observedAt: now}
	tests := []struct {
		name  string
		steps []struct {
			observation inventory.RuntimeObservation
			commits     inventory.CommitSource
		}
	}{
		{name: "low then exact", steps: []struct {
			observation inventory.RuntimeObservation
			commits     inventory.CommitSource
		}{{lowObservation, commitSource}, {exactObservation, commitSource}}},
		{name: "exact then low", steps: []struct {
			observation inventory.RuntimeObservation
			commits     inventory.CommitSource
		}{{exactObservation, commitSource}, {lowObservation, commitSource}}},
		{name: "unavailable then exact", steps: []struct {
			observation inventory.RuntimeObservation
			commits     inventory.CommitSource
		}{{exactObservation, nil}, {exactObservation, commitSource}}},
		{name: "exact then unavailable", steps: []struct {
			observation inventory.RuntimeObservation
			commits     inventory.CommitSource
		}{{exactObservation, commitSource}, {exactObservation, nil}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := Open(ctx, filepath.Join(t.TempDir(), "prodmap.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			for _, step := range test.steps {
				_, err := (inventory.Refresher{Runtime: reprocessingRuntimeSource{observation: step.observation}, Commits: step.commits, Store: store, Now: func() time.Time { return now }}).Refresh(ctx)
				if err != nil {
					t.Fatal(err)
				}
			}
			items, _, err := store.Runtime(ctx, inventory.RuntimeQuery{At: now.Add(time.Second), Limit: 10})
			if err != nil || len(items) != 1 || items[0].Confidence != correlation.LevelExact || items[0].ArtifactIdentity != testImageDigestA {
				t.Fatalf("reprocessed runtime=%+v err=%v", items, err)
			}
		})
	}
}

func TestFactualContradictionWinsRegardlessOfArrivalOrder(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	exact := exactSnapshot(now)
	contradiction := exactSnapshot(now)
	contradiction.Items[0].Commit = nil
	contradiction.Items[0].Correlation = correlation.EvaluateProvenance(correlation.ProvenanceInput{
		ImmutableIdentity: testImageDigestA,
		OCIRevision:       "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ResolvedCommitSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		RevisionState:     correlation.RevisionMismatched,
		ObservedAt:        now,
	})

	for _, snapshots := range [][]inventory.Snapshot{{exact, contradiction}, {contradiction, exact}} {
		ctx := context.Background()
		store, err := Open(ctx, filepath.Join(t.TempDir(), "prodmap.db"))
		if err != nil {
			t.Fatal(err)
		}
		for _, snapshot := range snapshots {
			if _, err := store.SaveRuntimeSnapshot(ctx, snapshot); err != nil {
				store.Close()
				t.Fatal(err)
			}
		}
		items, _, err := store.Runtime(ctx, inventory.RuntimeQuery{At: now.Add(time.Second), Limit: 10})
		store.Close()
		if err != nil || len(items) != 1 || items[0].Confidence != correlation.LevelUnknown {
			t.Fatalf("contradiction did not remain current: items=%+v err=%v", items, err)
		}
	}
}

func TestCorrelationFingerprintCanonicalizesEquivalentInstants(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	snapshot := exactSnapshot(now)
	if _, err := store.SaveRuntimeSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	equivalent := exactSnapshot(now)
	offset := time.FixedZone("fixture-offset", -3*60*60)
	for index := range equivalent.Items[0].Correlation.Evidence {
		equivalent.Items[0].Correlation.Evidence[index].ObservedAt = now.In(offset)
	}
	for index := range equivalent.Items[0].Correlation.ResolutionAttempts {
		equivalent.Items[0].Correlation.ResolutionAttempts[index].ObservedAt = now.In(offset)
	}
	for index := range equivalent.Items[0].Correlation.SourceFreshness {
		equivalent.Items[0].Correlation.SourceFreshness[index].ObservedAt = now.In(offset)
	}
	if _, err := store.SaveRuntimeSnapshot(ctx, equivalent); err != nil {
		t.Fatal(err)
	}
	var derivations int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM correlations`).Scan(&derivations); err != nil {
		t.Fatal(err)
	}
	if derivations != 1 {
		t.Fatalf("equivalent instants produced %d derivations, want 1", derivations)
	}
}

func TestExplanationRoundTripsFullCorrelationFieldsAndEvidenceDetails(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	snapshot := exactSnapshot(now)
	snapshot.Items[0].Correlation.Evidence[0].Details = map[string]string{"identity_kind": "repo_digest"}
	if _, err := store.SaveRuntimeSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	items, _, err := store.Runtime(ctx, inventory.RuntimeQuery{At: now.Add(time.Second), Limit: 10})
	if err != nil || len(items) != 1 {
		t.Fatalf("Runtime()=%+v, err=%v", items, err)
	}
	explanation, err := store.Explain(ctx, items[0].CorrelationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(explanation.ScoreComponents) == 0 || len(explanation.ResolutionAttempts) == 0 || len(explanation.SourceFreshness) == 0 {
		t.Fatalf("full correlation fields were not persisted: %+v", explanation)
	}
	var foundDetails bool
	for _, evidence := range explanation.Supporting {
		if evidence.Details["identity_kind"] == "repo_digest" {
			foundDetails = true
		}
	}
	if !foundDetails {
		t.Fatalf("evidence details missing from explanation: %+v", explanation.Supporting)
	}
}

func TestRejectedSnapshotMarksDockerSourcePartial(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot := exactSnapshot(time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC))
	snapshot.Rejected = 1
	if _, err := store.SaveRuntimeSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := store.db.QueryRow(`SELECT last_status FROM sources WHERE kind='docker'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "partial" {
		t.Fatalf("docker source status=%q, want partial", status)
	}
}

func TestSaveUsesInjectedStoreClock(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	observed := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	inserted := observed.Add(37 * time.Minute)
	store.now = func() time.Time { return inserted }
	if _, err := store.SaveRuntimeSnapshot(ctx, exactSnapshot(observed)); err != nil {
		t.Fatal(err)
	}
	var created string
	if err := store.db.QueryRow(`SELECT created_at FROM correlations`).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if created != formatTime(inserted) {
		t.Fatalf("correlation created_at=%q, want %q", created, formatTime(inserted))
	}
}

func TestConcurrentStoresCanIdempotentlySaveSameSnapshot(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "prodmap.db")
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	first.now = func() time.Time { return now.Add(time.Hour) }
	second.now = first.now
	snapshot := exactSnapshot(now)
	start := make(chan struct{})
	errorsByStore := make(chan error, 2)
	var wait sync.WaitGroup
	for _, store := range []*Store{first, second} {
		wait.Add(1)
		go func(store *Store) {
			defer wait.Done()
			<-start
			_, err := store.SaveRuntimeSnapshot(ctx, snapshot)
			errorsByStore <- err
		}(store)
	}
	close(start)
	wait.Wait()
	close(errorsByStore)
	for err := range errorsByStore {
		if err != nil {
			t.Fatalf("concurrent save error: %v", err)
		}
	}
	var runtimes, derivations, evidenceCount int
	if err := first.db.QueryRow(`SELECT COUNT(*) FROM runtime_instances`).Scan(&runtimes); err != nil {
		t.Fatal(err)
	}
	if err := first.db.QueryRow(`SELECT COUNT(*) FROM correlations`).Scan(&derivations); err != nil {
		t.Fatal(err)
	}
	if err := first.db.QueryRow(`SELECT COUNT(*) FROM evidence`).Scan(&evidenceCount); err != nil {
		t.Fatal(err)
	}
	if runtimes != 1 || derivations != 1 || evidenceCount != len(snapshot.Items[0].Correlation.Evidence) {
		t.Fatalf("counts after concurrent save: runtimes=%d derivations=%d evidence=%d", runtimes, derivations, evidenceCount)
	}
}

func TestArtifactAliasIntervalsAreDeterministicOutOfOrder(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	t1 := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	t3 := t2.Add(time.Hour)
	first := exactSnapshot(t1)
	setSnapshotArtifactIdentity(&first, testImageDigestA)
	second := exactSnapshot(t2)
	setSnapshotArtifactIdentity(&second, testImageDigestB)
	third := exactSnapshot(t3)
	setSnapshotArtifactIdentity(&third, testImageDigestA)
	for _, snapshot := range []inventory.Snapshot{third, first, second, first, second, third} {
		if _, err := store.SaveRuntimeSnapshot(ctx, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := store.db.Query(`SELECT a.identity,aa.valid_from,COALESCE(aa.valid_to,'') FROM artifact_aliases aa JOIN artifacts a ON a.id=aa.artifact_id WHERE aa.alias='api:latest' ORDER BY aa.valid_from`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got [][3]string
	for rows.Next() {
		var row [3]string
		if err := rows.Scan(&row[0], &row[1], &row[2]); err != nil {
			t.Fatal(err)
		}
		got = append(got, row)
	}
	want := [][3]string{
		{testImageDigestA, formatTime(t1), formatTime(t2)},
		{testImageDigestB, formatTime(t2), formatTime(t3)},
		{testImageDigestA, formatTime(t3), ""},
	}
	if len(got) != len(want) {
		t.Fatalf("alias intervals=%v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("alias interval %d=%v, want %v", index, got[index], want[index])
		}
	}
	var observations int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM artifact_alias_observations WHERE alias='api:latest'`).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if observations != 3 {
		t.Fatalf("alias observations=%d, want 3", observations)
	}
	for _, query := range []struct {
		name string
		at   time.Time
		want string
	}{
		{name: "before move", at: t1.Add(30 * time.Minute), want: testImageDigestA},
		{name: "during moved interval", at: t2.Add(30 * time.Minute), want: testImageDigestB},
		{name: "after move back", at: t3.Add(30 * time.Minute), want: testImageDigestA},
	} {
		t.Run(query.name, func(t *testing.T) {
			var identity string
			err := store.db.QueryRow(`SELECT a.identity FROM artifact_aliases aa JOIN artifacts a ON a.id=aa.artifact_id WHERE aa.alias='api:latest' AND aa.valid_from<=? AND (aa.valid_to IS NULL OR aa.valid_to>?)`, formatTime(query.at), formatTime(query.at)).Scan(&identity)
			if err != nil || identity != query.want {
				t.Fatalf("alias at %s = %q, want %q, err=%v", query.at, identity, query.want, err)
			}
		})
	}
}

func TestArtifactAliasConflictAtSameTimestampRollsBackSnapshot(t *testing.T) {
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
	setSnapshotItemArtifactIdentity(&second, testImageDigestB)
	snapshot.Items = append(snapshot.Items, second)
	if _, err := store.SaveRuntimeSnapshot(ctx, snapshot); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("SaveRuntimeSnapshot() error=%v, want ErrConflict", err)
	}
	var artifacts, observations, runtimes int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM artifacts`).Scan(&artifacts); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM artifact_alias_observations`).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM runtime_instances`).Scan(&runtimes); err != nil {
		t.Fatal(err)
	}
	if artifacts != 0 || observations != 0 || runtimes != 0 {
		t.Fatalf("alias conflict left partial state: artifacts=%d observations=%d runtimes=%d", artifacts, observations, runtimes)
	}
}

func TestRuntimeLoadsEvidenceOnceAfterPageTruncation(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	snapshot := exactSnapshot(now)
	prototype := snapshot.Items[0]
	for index := 2; index <= 25; index++ {
		item := prototype
		item.Runtime.ExternalID = fmt.Sprintf("container-%02d", index)
		item.Runtime.ContainerName = fmt.Sprintf("api-%02d", index)
		snapshot.Items = append(snapshot.Items, item)
	}
	if _, err := store.SaveRuntimeSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	var calls, received int
	store.evidenceBatchObserver = func(records int) {
		calls++
		received = records
	}
	items, cursor, err := store.Runtime(ctx, inventory.RuntimeQuery{At: now.Add(time.Second), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || received != 10 || len(items) != 10 || cursor == "" {
		t.Fatalf("evidence loader calls=%d records=%d items=%d cursor=%q", calls, received, len(items), cursor)
	}
	for _, item := range items {
		if len(item.EvidenceIDs) != len(snapshot.Items[0].Correlation.Evidence) {
			t.Fatalf("evidence IDs for %s=%d", item.ID, len(item.EvidenceIDs))
		}
	}
}

func TestRuntimeEnforcesZeroAndMaximumLimits(t *testing.T) {
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
	for _, limit := range []int{0, 1001} {
		if _, _, err := store.Runtime(ctx, inventory.RuntimeQuery{At: now.Add(time.Second), Limit: limit}); !errors.Is(err, errs.ErrInvalid) {
			t.Fatalf("Runtime(limit=%d) error=%v, want ErrInvalid", limit, err)
		}
	}
	items, cursor, err := store.Runtime(ctx, inventory.RuntimeQuery{At: now.Add(time.Second), Limit: 1000})
	if err != nil || len(items) != 1 || cursor != "" {
		t.Fatalf("Runtime(max limit) items=%d cursor=%q err=%v", len(items), cursor, err)
	}
}

func setSnapshotArtifactIdentity(snapshot *inventory.Snapshot, identity string) {
	setSnapshotItemArtifactIdentity(&snapshot.Items[0], identity)
}

func setSnapshotItemArtifactIdentity(item *inventory.SnapshotItem, identity string) {
	item.Runtime.ImageID = identity
	item.Runtime.RepoDigests = []string{"api@" + identity}
	item.Artifact.Identity = identity
	item.Artifact.Digest = strings.TrimPrefix(identity, "sha256:")
	item.Artifact.ImageID = identity
	item.Correlation = correlation.EvaluateProvenance(correlation.ProvenanceInput{
		ImmutableIdentity: identity,
		OCIRevision:       "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ResolvedCommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RevisionState:     correlation.RevisionResolved,
		ObservedAt:        item.Runtime.ObservedAt,
	})
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
	second.Runtime.ImageID = testImageDigestB
	second.Runtime.RepoDigests = []string{"api@" + testImageDigestB}
	second.Artifact.Identity = testImageDigestB
	second.Artifact.Digest = strings.TrimPrefix(testImageDigestB, "sha256:")
	second.Artifact.ImageID = testImageDigestB
	second.Artifact.Aliases = []string{"api:canary"}
	second.Artifact.OCIRevision = ""
	second.Commit = nil
	second.Correlation = correlation.EvaluateProvenance(correlation.ProvenanceInput{ImmutableIdentity: testImageDigestB, ObservedAt: now})
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
	observation := inventory.RuntimeObservation{ExternalID: "container-1", ContainerName: "api-1", ImageReference: "api:latest", ImageID: testImageDigestA, RepoDigests: []string{"api@" + testImageDigestA}, State: "running", Health: "healthy", ObservedAt: now, OCILabels: map[string]string{"org.opencontainers.image.revision": sha}}
	result := correlation.EvaluateProvenance(correlation.ProvenanceInput{ImmutableIdentity: testImageDigestA, OCIRevision: sha, ResolvedCommitSHA: sha, ObservedAt: now})
	commitTime := now.Add(-time.Hour)
	return inventory.Snapshot{ObservedAt: now, Repository: &inventory.Repository{ExternalID: "repo-hash", Name: "repo", RootPathHash: "hash"}, Items: []inventory.SnapshotItem{{ServiceLogicalKey: "api", ServiceDisplayName: "api", Environment: "default", Runtime: observation, Artifact: inventory.Artifact{Name: "api", IdentityKind: "repo_digest", Identity: testImageDigestA, DigestAlgorithm: "sha256", Digest: strings.TrimPrefix(testImageDigestA, "sha256:"), ImageID: testImageDigestA, ObservedReference: "api:latest", Aliases: []string{"api:latest"}, OCIRevision: sha}, Commit: &inventory.Commit{SHA: sha, CommitTime: commitTime, Subject: "test"}, Correlation: result}}}
}
