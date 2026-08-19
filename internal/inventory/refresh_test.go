package inventory

import (
	"context"
	"errors"
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
	repository Repository
	commit     Commit
	resolveErr error
}

func (f fakeCommitSource) Repository(context.Context) (Repository, error) { return f.repository, nil }
func (f fakeCommitSource) ResolveCommit(context.Context, string) (Commit, error) {
	return f.commit, f.resolveErr
}

type captureStore struct{ snapshot Snapshot }

func (s *captureStore) SaveRuntimeSnapshot(_ context.Context, snapshot Snapshot) (RefreshResult, error) {
	s.snapshot = snapshot
	return RefreshResult{Processed: len(snapshot.Items), Warnings: snapshot.Warnings}, nil
}

func TestRefreshBuildsExactSnapshot(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	sha := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := &captureStore{}
	refresher := Refresher{
		Runtime: fakeRuntimeSource{batch: RuntimeBatch{Observations: []RuntimeObservation{{ExternalID: "c1", ImageReference: "api:latest", RepoDigests: []string{"api@sha256:abc"}, OCILabels: map[string]string{"org.opencontainers.image.revision": sha}, ObservedAt: now}}}},
		Commits: fakeCommitSource{repository: Repository{ExternalID: "repo", Name: "repo"}, commit: Commit{SHA: sha, CommitTime: now}},
		Store:   store,
	}
	result, err := refresher.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Processed != 1 || store.snapshot.OperationID == "" || len(store.snapshot.Items) != 1 || store.snapshot.Items[0].Correlation.Level != correlation.LevelExact {
		t.Fatalf("result=%+v snapshot=%+v", result, store.snapshot)
	}
}

func TestRefreshValidatesRequiredDependencies(t *testing.T) {
	_, err := (Refresher{}).Refresh(context.Background())
	if !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("Refresh() error = %v, want ErrInvalid", err)
	}
}

func TestRefreshTreatsMissingCommitAsUnknown(t *testing.T) {
	now := time.Now().UTC()
	sha := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := &captureStore{}
	refresher := Refresher{
		Runtime: fakeRuntimeSource{batch: RuntimeBatch{Observations: []RuntimeObservation{{ExternalID: "c1", ImageReference: "api:latest", ImageID: "sha256:abc", OCILabels: map[string]string{"org.opencontainers.image.revision": sha}, ObservedAt: now}}}},
		Commits: fakeCommitSource{repository: Repository{ExternalID: "repo"}, resolveErr: errs.ErrNotFound}, Store: store,
	}
	_, err := refresher.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if store.snapshot.Items[0].Correlation.Level != correlation.LevelUnknown || !errors.Is(fakeCommitSource{resolveErr: errs.ErrNotFound}.resolveErr, errs.ErrNotFound) {
		t.Fatalf("correlation = %+v", store.snapshot.Items[0].Correlation)
	}
}
