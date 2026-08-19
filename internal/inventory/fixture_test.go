package inventory_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/correlation"
	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/inventory"
	prodmapsqlite "github.com/guijoazeiro/prodmap/internal/sqlite"
)

type fixtureDocument struct {
	Clock      time.Time            `json:"clock"`
	Repository inventory.Repository `json:"repository"`
	Batches    []fixtureBatch       `json:"runtime_batches"`
	Commits    []fixtureCommit      `json:"commits"`
	NotFound   []string             `json:"revisions_not_found"`
}

type fixtureBatch struct {
	ArrivalSequence int                         `json:"arrival_sequence"`
	Observations    []fixtureRuntimeObservation `json:"observations"`
}

type fixtureRuntimeObservation struct {
	ExternalID     string            `json:"external_id"`
	ContainerName  string            `json:"container_name"`
	ImageReference string            `json:"image_reference"`
	ImageID        string            `json:"image_id"`
	RepoDigests    []string          `json:"repo_digests"`
	RepoTags       []string          `json:"repo_tags"`
	OCILabels      map[string]string `json:"oci_labels"`
	State          string            `json:"state"`
	Health         string            `json:"health"`
	RestartCount   int64             `json:"restart_count"`
	StartedAt      *time.Time        `json:"started_at"`
	ObservedAt     time.Time         `json:"observed_at"`
}

type fixtureCommit struct {
	Revision   string    `json:"revision"`
	SHA        string    `json:"sha"`
	CommitTime time.Time `json:"commit_time"`
	Subject    string    `json:"subject"`
	TreeSHA    string    `json:"tree_sha"`
}

type fixtureExpected struct {
	Clock          time.Time `json:"clock"`
	ServiceKey     string    `json:"service_key"`
	RuntimeRecords []struct {
		ObservedAt time.Time `json:"observed_at"`
		Preserved  bool      `json:"preserved"`
		Latest     bool      `json:"latest"`
	} `json:"runtime_records"`
	Provenance struct {
		RelationType  correlation.RelationType   `json:"relation_type"`
		Level         correlation.Level          `json:"level"`
		Score         float64                    `json:"score"`
		Warnings      []string                   `json:"warnings"`
		Missing       []string                   `json:"missing"`
		EvidenceKinds []correlation.EvidenceKind `json:"evidence_kinds"`
		Contradiction bool                       `json:"contradiction"`
		CommitSHA     *string                    `json:"commit_sha"`
	} `json:"provenance"`
}

type fixtureCLIExpected struct {
	ExitCode int `json:"exit_code"`
	Stdout   struct {
		TargetType    string                     `json:"target_type"`
		Confidence    correlation.Level          `json:"confidence"`
		Score         float64                    `json:"score"`
		RelationType  correlation.RelationType   `json:"relation_type"`
		Algorithm     string                     `json:"algorithm"`
		Warnings      []string                   `json:"warnings"`
		Missing       []string                   `json:"missing"`
		EvidenceKinds []correlation.EvidenceKind `json:"evidence_kinds"`
	} `json:"stdout"`
}

type fixtureRuntimeSource struct{ batch inventory.RuntimeBatch }

func (s fixtureRuntimeSource) InspectRuntime(context.Context) (inventory.RuntimeBatch, error) {
	return s.batch, nil
}

type fixtureCommitSource struct {
	repository inventory.Repository
	commits    map[string]inventory.Commit
	notFound   map[string]bool
}

func (s fixtureCommitSource) Repository(context.Context) (inventory.Repository, error) {
	return s.repository, nil
}

func (s fixtureCommitSource) ResolveCommit(_ context.Context, revision string) (inventory.Commit, error) {
	if value, ok := s.commits[revision]; ok {
		return value, nil
	}
	if s.notFound[revision] {
		return inventory.Commit{}, errors.Join(errs.ErrNotFound, errors.New("fixture revision unavailable"))
	}
	return inventory.Commit{}, errors.Join(errs.ErrNotFound, errors.New("fixture commit absent"))
}

func TestCanonicalProvenanceFixtures(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "testdata", "scenarios", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 7 {
		t.Fatalf("fixture directories = %d, want 7", len(paths))
	}
	for _, path := range paths {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			var input fixtureDocument
			readFixtureJSON(t, filepath.Join(path, "input.json"), &input)
			var expected fixtureExpected
			readFixtureJSON(t, filepath.Join(path, "expected.json"), &expected)
			var expectedCLI fixtureCLIExpected
			readFixtureJSON(t, filepath.Join(path, "expected-cli.json"), &expectedCLI)
			if !input.Clock.Equal(expected.Clock) {
				t.Fatalf("fixture clocks differ: input=%s expected=%s", input.Clock, expected.Clock)
			}

			databasePath := filepath.Join(t.TempDir(), "prodmap.db")
			store, err := prodmapsqlite.Open(context.Background(), databasePath)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			commits := make(map[string]inventory.Commit)
			for _, value := range input.Commits {
				commits[value.Revision] = inventory.Commit{SHA: value.SHA, CommitTime: value.CommitTime, Subject: value.Subject, TreeSHA: value.TreeSHA}
			}
			notFound := make(map[string]bool)
			for _, revision := range input.NotFound {
				notFound[revision] = true
			}
			commitSource := fixtureCommitSource{repository: input.Repository, commits: commits, notFound: notFound}
			var last inventory.RefreshResult
			sort.Slice(input.Batches, func(i, j int) bool { return input.Batches[i].ArrivalSequence < input.Batches[j].ArrivalSequence })
			for _, batch := range input.Batches {
				observations := make([]inventory.RuntimeObservation, 0, len(batch.Observations))
				var observedAt time.Time
				for _, value := range batch.Observations {
					observation := inventory.RuntimeObservation{
						ExternalID: value.ExternalID, ContainerName: value.ContainerName, ImageReference: value.ImageReference,
						ImageID: value.ImageID, RepoDigests: value.RepoDigests, RepoTags: value.RepoTags, OCILabels: value.OCILabels,
						State: value.State, Health: value.Health, RestartCount: value.RestartCount,
						StartedAt: value.StartedAt, ObservedAt: value.ObservedAt,
					}
					observations = append(observations, observation)
					if observation.ObservedAt.After(observedAt) {
						observedAt = observation.ObservedAt
					}
				}
				last, err = (inventory.Refresher{
					Runtime: fixtureRuntimeSource{batch: inventory.RuntimeBatch{ObservedAt: observedAt, Observations: observations}},
					Commits: commitSource, Store: store,
				}).Refresh(context.Background())
				if err != nil {
					t.Fatal(err)
				}
			}
			if !reflect.DeepEqual(last.Warnings, expected.Provenance.Warnings) {
				t.Fatalf("warnings = %#v, want %#v", last.Warnings, expected.Provenance.Warnings)
			}
			for _, record := range expected.RuntimeRecords {
				items, _, err := store.Runtime(context.Background(), inventory.RuntimeQuery{
					Service: expected.ServiceKey, Environment: "default", At: record.ObservedAt, Limit: 10,
				})
				if err != nil {
					t.Fatal(err)
				}
				if record.Preserved && len(items) != 1 {
					t.Fatalf("runtime at %s has %d records, want 1", record.ObservedAt, len(items))
				}
			}
			items, _, err := store.Runtime(context.Background(), inventory.RuntimeQuery{
				Service: expected.ServiceKey, Environment: "default", At: expected.Clock, Limit: 10,
			})
			if err != nil || len(items) != 1 {
				t.Fatalf("current runtime = %d items, err=%v", len(items), err)
			}
			item := items[0]
			if item.Confidence != expected.Provenance.Level || item.Score != expected.Provenance.Score {
				t.Fatalf("provenance = (%s, %.2f), want (%s, %.2f)", item.Confidence, item.Score, expected.Provenance.Level, expected.Provenance.Score)
			}
			if expected.Provenance.CommitSHA == nil && item.CommitSHA != "" {
				t.Fatalf("commit SHA = %q, want absent", item.CommitSHA)
			}
			if expected.Provenance.CommitSHA != nil && item.CommitSHA != *expected.Provenance.CommitSHA {
				t.Fatalf("commit SHA = %q, want %q", item.CommitSHA, *expected.Provenance.CommitSHA)
			}
			explanation, err := store.Explain(context.Background(), item.CorrelationID)
			if err != nil {
				t.Fatal(err)
			}
			if explanation.RelationType != expected.Provenance.RelationType || !reflect.DeepEqual(explanation.Missing, expected.Provenance.Missing) {
				t.Fatalf("explanation relation/missing = (%s, %#v), want (%s, %#v)", explanation.RelationType, explanation.Missing, expected.Provenance.RelationType, expected.Provenance.Missing)
			}
			if expectedCLI.ExitCode != 0 || explanation.TargetType != expectedCLI.Stdout.TargetType ||
				explanation.Confidence != expectedCLI.Stdout.Confidence || explanation.Score != expectedCLI.Stdout.Score ||
				explanation.RelationType != expectedCLI.Stdout.RelationType || explanation.Algorithm != expectedCLI.Stdout.Algorithm ||
				!reflect.DeepEqual(explanation.Warnings, expectedCLI.Stdout.Warnings) || !reflect.DeepEqual(explanation.Missing, expectedCLI.Stdout.Missing) {
				t.Fatalf("CLI expectation mismatch: explanation=%+v expected=%+v", explanation, expectedCLI.Stdout)
			}
			kinds := make([]correlation.EvidenceKind, 0, len(explanation.Supporting)+len(explanation.Contradicting)+len(explanation.Neutral))
			for _, group := range [][]inventory.PersistedEvidence{explanation.Supporting, explanation.Contradicting, explanation.Neutral} {
				for _, evidence := range group {
					kinds = append(kinds, evidence.Kind)
				}
			}
			if !sameEvidenceKinds(kinds, expected.Provenance.EvidenceKinds) {
				t.Fatalf("evidence kinds = %#v, want %#v", kinds, expected.Provenance.EvidenceKinds)
			}
			if !sameEvidenceKinds(kinds, expectedCLI.Stdout.EvidenceKinds) {
				t.Fatalf("CLI evidence kinds = %#v, want %#v", kinds, expectedCLI.Stdout.EvidenceKinds)
			}
			if got := len(explanation.Contradicting) > 0; got != expected.Provenance.Contradiction {
				t.Fatalf("contradiction = %t, want %t", got, expected.Provenance.Contradiction)
			}
			if filepath.Base(path) == "sensitive_labels" {
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(databasePath)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(raw), "https://example.invalid/prodmap") {
					t.Fatal("allowlisted OCI source label was not persisted")
				}
				for _, forbidden := range []string{"REDACTED_FIXTURE_VALUE", "com.example.secret", "com.example.internal.note", "org.opencontainers.image.title"} {
					if strings.Contains(string(raw), forbidden) {
						t.Fatalf("database contains forbidden label data %q", forbidden)
					}
				}
			}
		})
	}
}

func readFixtureJSON(t *testing.T, path string, target any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

func sameEvidenceKinds(left, right []correlation.EvidenceKind) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[correlation.EvidenceKind]int)
	for _, value := range left {
		counts[value]++
	}
	for _, value := range right {
		counts[value]--
	}
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}
	return true
}
