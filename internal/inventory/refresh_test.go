package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/correlation"
	"github.com/guijoazeiro/prodmap/internal/errs"
)

type fakeRuntimeSource struct {
	batch RuntimeBatch
	err   error
}

func (f fakeRuntimeSource) InspectRuntime(context.Context) (RuntimeBatch, error) {
	return f.batch, f.err
}

type fakeCommitSource struct {
	repository    Repository
	repositoryErr error
	commit        Commit
	resolveErr    error
	resolveCalls  int
	lastRevision  string
}

func (f *fakeCommitSource) Repository(context.Context) (Repository, error) {
	return f.repository, f.repositoryErr
}

func (f *fakeCommitSource) ResolveCommit(_ context.Context, revision string) (Commit, error) {
	f.resolveCalls++
	f.lastRevision = revision
	return f.commit, f.resolveErr
}

type captureStore struct{ snapshot Snapshot }

func (s *captureStore) SaveRuntimeSnapshot(_ context.Context, snapshot Snapshot) (RefreshResult, error) {
	s.snapshot = snapshot
	return RefreshResult{Processed: len(snapshot.Items), Warnings: snapshot.Warnings}, nil
}

func TestRefreshBuildsExactSnapshotWithFullDerivationDetail(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	sha := strings.Repeat("a", 40)
	digest := strings.Repeat("b", 64)
	store := &captureStore{}
	commits := &fakeCommitSource{
		repository: Repository{ExternalID: "repo", Name: "repo"},
		commit:     Commit{SHA: sha, CommitTime: now.Add(-time.Hour)},
	}
	refresher := Refresher{
		Runtime: fakeRuntimeSource{batch: RuntimeBatch{ObservedAt: now, Observations: []RuntimeObservation{{
			ExternalID: "c1", ImageReference: "api:latest", RepoDigests: []string{"api@sha256:" + digest},
			OCILabels: map[string]string{ociRevisionLabel: sha, ociCreatedLabel: now.Format(time.RFC3339)}, ObservedAt: now,
		}}}},
		Commits: commits,
		Store:   store,
		Now:     func() time.Time { return now },
	}
	result, err := refresher.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	correlationResult := store.snapshot.Items[0].Correlation
	if result.Processed != 1 || store.snapshot.OperationID == "" || len(store.snapshot.Items) != 1 || correlationResult.Level != correlation.LevelExact {
		t.Fatalf("result=%+v snapshot=%+v", result, store.snapshot)
	}
	if commits.resolveCalls != 1 || commits.lastRevision != sha {
		t.Fatalf("ResolveCommit calls=%d revision=%q", commits.resolveCalls, commits.lastRevision)
	}
	if len(correlationResult.ScoreComponents) < 3 || len(correlationResult.ResolutionAttempts) != 1 || correlationResult.ResolutionAttempts[0].State != correlation.RevisionResolved || len(correlationResult.SourceFreshness) != 2 {
		t.Fatalf("derivation detail = %+v", correlationResult)
	}
}

func TestRefreshValidatesRequiredDependencies(t *testing.T) {
	_, err := (Refresher{}).Refresh(context.Background())
	if !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("Refresh() error = %v, want ErrInvalid", err)
	}
}

func TestRefreshDistinguishesNotFoundFromUnavailableGit(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	sha := strings.Repeat("a", 40)
	digest := "sha256:" + strings.Repeat("b", 64)
	tests := []struct {
		name          string
		repositoryErr error
		resolveErr    error
		wantState     correlation.RevisionResolutionState
		wantCalls     int
		wantClaim     string
		rejectClaim   string
	}{
		{name: "revision absent", resolveErr: errs.ErrNotFound, wantState: correlation.RevisionNotFound, wantCalls: 1, wantClaim: "lookup was executed", rejectClaim: "source was unavailable"},
		{name: "repository unavailable", repositoryErr: errs.ErrUnavailable, wantState: correlation.RevisionSourceUnavailable, wantCalls: 0, wantClaim: "source was unavailable", rejectClaim: "not found"},
		{name: "repository not present", repositoryErr: errs.ErrNotFound, wantState: correlation.RevisionSourceUnavailable, wantCalls: 0, wantClaim: "source was unavailable", rejectClaim: "not found"},
		{name: "source fails during resolution", resolveErr: errs.ErrUnavailable, wantState: correlation.RevisionSourceUnavailable, wantCalls: 1, wantClaim: "source was unavailable", rejectClaim: "not found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &captureStore{}
			commits := &fakeCommitSource{
				repository: Repository{ExternalID: "repo"}, repositoryErr: test.repositoryErr, resolveErr: test.resolveErr,
			}
			refresher := Refresher{
				Runtime: fakeRuntimeSource{batch: RuntimeBatch{ObservedAt: now, Observations: []RuntimeObservation{{
					ExternalID: "c1", ImageReference: "api:latest", ImageID: digest,
					OCILabels: map[string]string{ociRevisionLabel: sha}, ObservedAt: now,
				}}}},
				Commits: commits,
				Store:   store,
			}
			if _, err := refresher.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			got := store.snapshot.Items[0].Correlation
			if got.Level != correlation.LevelUnknown || len(got.ResolutionAttempts) != 1 || got.ResolutionAttempts[0].State != test.wantState {
				t.Fatalf("correlation = %+v", got)
			}
			if commits.resolveCalls != test.wantCalls {
				t.Fatalf("ResolveCommit calls = %d, want %d", commits.resolveCalls, test.wantCalls)
			}
			claims := correlationClaims(got)
			if !strings.Contains(claims, test.wantClaim) || strings.Contains(claims, test.rejectClaim) {
				t.Fatalf("claims = %q", claims)
			}
		})
	}
}

func TestRefreshInvalidRevisionDoesNotCallOrAttributeGit(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	secret := "token=must-not-leak"
	store := &captureStore{}
	commits := &fakeCommitSource{repository: Repository{ExternalID: "repo"}}
	refresher := Refresher{
		Runtime: fakeRuntimeSource{batch: RuntimeBatch{ObservedAt: now, Observations: []RuntimeObservation{{
			ExternalID: "c1", ImageReference: "api:latest", ImageID: "sha256:" + strings.Repeat("b", 64),
			OCILabels: map[string]string{ociRevisionLabel: secret}, ObservedAt: now,
		}}}},
		Commits: commits,
		Store:   store,
	}
	if _, err := refresher.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if commits.resolveCalls != 0 {
		t.Fatalf("ResolveCommit calls = %d, want zero", commits.resolveCalls)
	}
	got := store.snapshot.Items[0].Correlation
	for _, evidence := range got.Evidence {
		if evidence.Source == "git" {
			t.Fatalf("invalid revision attributed evidence to Git: %+v", evidence)
		}
	}
	encoded, err := json.Marshal(store.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("invalid revision leaked into snapshot: %s", encoded)
	}
}

func TestRefreshInvalidDigestCannotProduceExact(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	sha := strings.Repeat("a", 40)
	store := &captureStore{}
	commits := &fakeCommitSource{repository: Repository{ExternalID: "repo"}, commit: Commit{SHA: sha, CommitTime: now}}
	refresher := Refresher{
		Runtime: fakeRuntimeSource{batch: RuntimeBatch{ObservedAt: now, Observations: []RuntimeObservation{{
			ExternalID: "c1", ImageReference: "api:latest", RepoDigests: []string{"api@md5:x"},
			OCILabels: map[string]string{ociRevisionLabel: sha}, ObservedAt: now,
		}}}},
		Commits: commits,
		Store:   store,
	}
	if _, err := refresher.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := store.snapshot.Items[0].Correlation
	if got.Level != correlation.LevelLow || !strings.Contains(correlationClaims(got), issueInvalidRepoDigest) {
		t.Fatalf("invalid digest correlation = %+v", got)
	}
}

func TestRefreshInvalidRepoDigestCanFallbackToValidImageID(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	sha := strings.Repeat("a", 40)
	store := &captureStore{}
	refresher := Refresher{
		Runtime: fakeRuntimeSource{batch: RuntimeBatch{ObservedAt: now, Observations: []RuntimeObservation{{
			ExternalID: "c1", ImageReference: "api:latest", ImageID: "sha256:" + strings.Repeat("b", 64), RepoDigests: []string{"api@sha256:short"},
			OCILabels: map[string]string{ociRevisionLabel: sha}, ObservedAt: now,
		}}}},
		Commits: &fakeCommitSource{repository: Repository{ExternalID: "repo"}, commit: Commit{SHA: sha, CommitTime: now}},
		Store:   store,
	}
	if _, err := refresher.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := store.snapshot.Items[0].Correlation
	if got.Level != correlation.LevelExact || !strings.Contains(correlationClaims(got), issueImageIDFallback) {
		t.Fatalf("fallback correlation = %+v", got)
	}
}

func TestRefreshFutureImageCreationPreventsExact(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	sha := strings.Repeat("a", 40)
	store := &captureStore{}
	refresher := Refresher{
		Runtime: fakeRuntimeSource{batch: RuntimeBatch{ObservedAt: now, Observations: []RuntimeObservation{{
			ExternalID: "c1", ImageReference: "api:latest", ImageID: "sha256:" + strings.Repeat("b", 64),
			OCILabels: map[string]string{ociRevisionLabel: sha, ociCreatedLabel: now.Add(correlation.MaxImageCreatedClockSkew + time.Nanosecond).Format(time.RFC3339Nano)}, ObservedAt: now,
		}}}},
		Commits: &fakeCommitSource{repository: Repository{ExternalID: "repo"}, commit: Commit{SHA: sha, CommitTime: now}},
		Store:   store,
	}
	if _, err := refresher.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := store.snapshot.Items[0].Correlation
	if got.Level != correlation.LevelUnknown || !correlationHasTemporalContradiction(got) {
		t.Fatalf("future-created correlation = %+v", got)
	}
}

func TestRefreshPropagatesBatchObservedAtToPersistedRuntime(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	store := &captureStore{}
	refresher := Refresher{
		Runtime: fakeRuntimeSource{batch: RuntimeBatch{ObservedAt: now, Observations: []RuntimeObservation{{
			ExternalID: "c1", ImageReference: "api:latest", ImageID: "sha256:" + strings.Repeat("b", 64),
		}}}},
		Store: store,
	}
	if _, err := refresher.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := store.snapshot.Items[0].Runtime.ObservedAt; !got.Equal(now) {
		t.Fatalf("persisted runtime observed_at = %s, want %s", got, now)
	}
	if got := store.snapshot.Items[0].Correlation.Evidence[0].ObservedAt; !got.Equal(now) {
		t.Fatalf("correlation evidence observed_at = %s, want %s", got, now)
	}
}

func TestRefreshIdentityConflictSkipsCommitQuery(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	sha := strings.Repeat("a", 40)
	commits := &fakeCommitSource{repository: Repository{ExternalID: "repo"}, commit: Commit{SHA: sha}}
	store := &captureStore{}
	refresher := Refresher{
		Runtime: fakeRuntimeSource{batch: RuntimeBatch{ObservedAt: now, Observations: []RuntimeObservation{{
			ExternalID: "c1", ImageReference: "api:latest",
			RepoDigests: []string{"api@sha256:" + strings.Repeat("b", 64), "api@sha256:" + strings.Repeat("c", 64)},
			OCILabels:   map[string]string{ociRevisionLabel: sha}, ObservedAt: now,
		}}}},
		Commits: commits,
		Store:   store,
	}
	if _, err := refresher.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if commits.resolveCalls != 0 || store.snapshot.Items[0].Correlation.ResolutionAttempts[0].State != correlation.RevisionQueryNotRun {
		t.Fatalf("calls=%d correlation=%+v", commits.resolveCalls, store.snapshot.Items[0].Correlation)
	}
}

func correlationClaims(result correlation.Result) string {
	var claims strings.Builder
	for _, evidence := range result.Evidence {
		claims.WriteString(evidence.Claim)
	}
	return claims.String()
}

func correlationHasTemporalContradiction(result correlation.Result) bool {
	for _, evidence := range result.Evidence {
		if evidence.Kind == correlation.EvidenceTemporal && evidence.Polarity == correlation.PolarityContradicts {
			return true
		}
	}
	return false
}
