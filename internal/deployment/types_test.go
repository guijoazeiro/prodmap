package deployment

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/errs"
)

func TestDeploymentLedgerLoadIsDeterministicAndDoesNotExposePath(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "deployment", "valid-two-services.jsonl")
	now := time.Date(2026, 8, 26, 12, 1, 0, 0, time.UTC)
	first, err := LoadFrozenFile(context.Background(), path, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadFrozenFile(context.Background(), path, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.SourceHash != second.SourceHash || first.SourceKey != second.SourceKey || len(first.Records) != 2 {
		t.Fatalf("unstable snapshot: %#v", first)
	}
	if first.Records[0].Service != "checkout-api" || first.Records[1].Service != "payment-api" || first.Records[0].Fingerprint == "" {
		t.Fatalf("records are not semantically sorted: %#v", first.Records)
	}
	if first.SourceKey == path {
		t.Fatal("source key exposed path")
	}
}

func TestDeploymentLedgerLoadRejectsDuplicateAndSecret(t *testing.T) {
	directory := t.TempDir()
	duplicate := filepath.Join(directory, "duplicate.jsonl")
	contents, err := os.ReadFile(filepath.Join("..", "..", "testdata", "deployment", "valid-two-services.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(duplicate, append(contents, contents[:len(contents)-1]...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFrozenFile(context.Background(), duplicate, time.Now()); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("duplicate error = %v", err)
	}
	secret := filepath.Join(directory, "secret.jsonl")
	if err := os.WriteFile(secret, []byte(`{"schema_version":"1.0","deployment_id":"secret-token","deployed_at":"2026-08-26T12:00:00Z","build_started_at":"2026-08-26T11:58:00Z","build_date":"2026-08-26T11:59:00Z","environment":"reference","service":"checkout-api","version":"abc","scenario_profile":"healthy","git_head":"unknown","git_dirty":true,"vcs_revision":"unknown","vcs_revision_verified":false,"image_reference":"checkout-api:stable","image_id":"sha256:1111111111111111111111111111111111111111111111111111111111111111","repo_digest":null,"compose_project":"reference-project","status":"running"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFrozenFile(context.Background(), secret, time.Now()); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("secret error = %v", err)
	}
}

func TestValidateSnapshotRejectsForgedBoundaryData(t *testing.T) {
	snapshot, err := LoadFrozenFile(context.Background(), filepath.Join("..", "..", "testdata", "deployment", "valid-two-services.jsonl"), time.Date(2026, 8, 26, 12, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Records[0].ImageID = "sha256:invalid"
	if err := ValidateSnapshot(snapshot); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("forged image identity error = %v", err)
	}

	snapshot, err = LoadFrozenFile(context.Background(), filepath.Join("..", "..", "testdata", "deployment", "valid-two-services.jsonl"), time.Date(2026, 8, 26, 12, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Records[0].Fingerprint = "sha256-v1:forged"
	if err := ValidateSnapshot(snapshot); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("forged fingerprint error = %v", err)
	}
}
