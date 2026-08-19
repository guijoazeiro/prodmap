package inventory_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/cli"
	"github.com/guijoazeiro/prodmap/internal/correlation"
	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/inventory"
)

const fixtureSchemaVersion = "1.0"

var fixtureUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type fixtureDocument struct {
	SchemaVersion string            `json:"schema_version"`
	Clock         time.Time         `json:"clock"`
	Repository    fixtureRepository `json:"repository"`
	Batches       []fixtureBatch    `json:"runtime_batches"`
	Commits       []fixtureCommit   `json:"commits"`
	NotFound      []string          `json:"revisions_not_found"`
}

type fixtureRepository struct {
	ExternalID   string `json:"external_id"`
	Name         string `json:"name"`
	CanonicalURL string `json:"canonical_url"`
	RootPathHash string `json:"root_path_hash"`
}

type fixtureConfig struct {
	SchemaVersion     string    `json:"schema_version"`
	Clock             time.Time `json:"clock"`
	Environment       string    `json:"environment"`
	OCILabelAllowlist []string  `json:"oci_label_allowlist"`
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
	SchemaVersion  string    `json:"schema_version"`
	Clock          time.Time `json:"clock"`
	ServiceKey     string    `json:"service_key"`
	RuntimeRecords []struct {
		ObservedAt time.Time `json:"observed_at"`
		Preserved  bool      `json:"preserved"`
		Latest     bool      `json:"latest"`
	} `json:"runtime_records"`
	Provenance struct {
		RelationType       correlation.RelationType   `json:"relation_type"`
		Level              correlation.Level          `json:"level"`
		Score              float64                    `json:"score"`
		Warnings           []string                   `json:"warnings"`
		Missing            []string                   `json:"missing"`
		EvidenceKinds      []correlation.EvidenceKind `json:"evidence_kinds"`
		Contradiction      bool                       `json:"contradiction"`
		CommitSHA          *string                    `json:"commit_sha"`
		History            []time.Time                `json:"history"`
		PersistedOCILabels map[string]string          `json:"persisted_oci_labels"`
	} `json:"provenance"`
}

type fixtureCLIExpected struct {
	SchemaVersion string          `json:"schema_version"`
	Command       []string        `json:"command"`
	ExitCode      int             `json:"exit_code"`
	Stderr        string          `json:"stderr"`
	Stdout        json.RawMessage `json:"stdout"`
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
	for _, fixturePath := range paths {
		fixturePath := fixturePath
		t.Run(filepath.Base(fixturePath), func(t *testing.T) {
			var input fixtureDocument
			readStrictFixtureJSON(t, filepath.Join(fixturePath, "input.json"), &input)
			var config fixtureConfig
			readStrictFixtureJSON(t, filepath.Join(fixturePath, "config.json"), &config)
			var expected fixtureExpected
			readStrictFixtureJSON(t, filepath.Join(fixturePath, "expected.json"), &expected)
			var expectedCLI fixtureCLIExpected
			readStrictFixtureJSON(t, filepath.Join(fixturePath, "expected-cli.json"), &expectedCLI)
			validateFixtureContracts(t, input, config, expected, expectedCLI)

			projectDir := t.TempDir()
			app, stdout, stderr := newFixtureApp(projectDir, config.Clock)
			if code := app.Run(context.Background(), []string{"init", "--json", "--project-dir", projectDir}); code != 0 {
				t.Fatalf("init exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			assertOneJSONValue(t, stdout.Bytes())
			if stderr.Len() != 0 {
				t.Fatalf("init JSON wrote stderr: %q", stderr.String())
			}

			commits := make(map[string]inventory.Commit, len(input.Commits))
			for _, value := range input.Commits {
				commits[value.Revision] = inventory.Commit{SHA: value.SHA, CommitTime: value.CommitTime, Subject: value.Subject, TreeSHA: value.TreeSHA}
			}
			notFound := make(map[string]bool, len(input.NotFound))
			for _, revision := range input.NotFound {
				notFound[revision] = true
			}
			commitSource := fixtureCommitSource{repository: inventory.Repository{
				ExternalID: input.Repository.ExternalID, Name: input.Repository.Name,
				CanonicalURL: input.Repository.CanonicalURL, RootPathHash: input.Repository.RootPathHash,
			}, commits: commits, notFound: notFound}
			app.CommitSource = func(string) inventory.CommitSource { return commitSource }

			sort.Slice(input.Batches, func(i, j int) bool { return input.Batches[i].ArrivalSequence < input.Batches[j].ArrivalSequence })
			batches := make([]inventory.RuntimeBatch, 0, len(input.Batches))
			for _, batch := range input.Batches {
				batches = append(batches, makeRuntimeBatch(batch, config.OCILabelAllowlist))
			}
			nextBatch := 0
			app.RuntimeSource = func() inventory.RuntimeSource {
				if nextBatch >= len(batches) {
					t.Fatalf("runtime source requested more than %d configured batches", len(batches))
				}
				batch := batches[nextBatch]
				nextBatch++
				return fixtureRuntimeSource{batch: batch}
			}
			for range batches {
				runFixtureJSON(t, app, stdout, stderr, "runtime", "--refresh", "--json", "--project-dir", projectDir)
			}
			if nextBatch != len(batches) {
				t.Fatalf("consumed runtime batches = %d, want %d", nextBatch, len(batches))
			}

			current := runFixtureJSON(t, app, stdout, stderr, "runtime", "--service", expected.ServiceKey, "--environment", config.Environment, "--at", config.Clock.Format(time.RFC3339Nano), "--json", "--project-dir", projectDir)
			currentItems := envelopeItems(t, current)
			if len(currentItems) != 1 {
				t.Fatalf("current runtime items = %d, want 1", len(currentItems))
			}
			currentObservedAt := currentItems[0]["observed_at"]
			correlationID, ok := currentItems[0]["correlation_id"].(string)
			if !ok || correlationID == "" {
				t.Fatalf("current runtime correlation_id = %#v", currentItems[0]["correlation_id"])
			}

			observedHistory := make([]time.Time, 0, len(expected.RuntimeRecords))
			for _, record := range expected.RuntimeRecords {
				envelope := runFixtureJSON(t, app, stdout, stderr, "runtime", "--service", expected.ServiceKey, "--environment", config.Environment, "--at", record.ObservedAt.Format(time.RFC3339Nano), "--json", "--project-dir", projectDir)
				items := envelopeItems(t, envelope)
				preserved := len(items) == 1
				if preserved != record.Preserved {
					t.Fatalf("runtime at %s preserved=%t, want %t", record.ObservedAt, preserved, record.Preserved)
				}
				if preserved {
					observed, parseErr := time.Parse(time.RFC3339Nano, items[0]["observed_at"].(string))
					if parseErr != nil {
						t.Fatal(parseErr)
					}
					observedHistory = append(observedHistory, observed.UTC())
					latest := items[0]["observed_at"] == currentObservedAt
					if latest != record.Latest {
						t.Fatalf("runtime at %s latest=%t, want %t", record.ObservedAt, latest, record.Latest)
					}
				}
			}
			if expected.Provenance.History != nil && !reflect.DeepEqual(observedHistory, expected.Provenance.History) {
				t.Fatalf("runtime history = %#v, want %#v", observedHistory, expected.Provenance.History)
			}

			actualCLI := runExpectedCLI(t, app, stdout, stderr, projectDir, correlationID, expectedCLI)
			if actualCLI["generated_at"] != config.Clock.UTC().Format(time.RFC3339Nano) {
				t.Fatalf("CLI generated_at=%v, want fixed fixture clock %s", actualCLI["generated_at"], config.Clock.UTC().Format(time.RFC3339Nano))
			}
			assertExpectedProvenance(t, actualCLI, expected)
			jsonOutput := stdout.String()
			stdout.Reset()
			stderr.Reset()
			if code := app.Run(context.Background(), []string{"explain", correlationID, "--detail", "full", "--project-dir", projectDir}); code != 0 {
				t.Fatalf("human explain exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			assertPersistedLabelsAndRedaction(t, projectDir, input, expected, jsonOutput+"\n"+stdout.String(), stderr.String())
		})
	}
}

func newFixtureApp(projectDir string, clock time.Time) (*cli.App, *bytes.Buffer, *bytes.Buffer) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	app := cli.NewApp(stdout, stderr, cli.DefaultBuildInfo())
	app.Now = func() time.Time { return clock }
	app.WorkingDir = func() (string, error) { return projectDir, nil }
	app.Environment = map[string]string{}
	app.UserConfigPath = filepath.Join(projectDir, "absent-user-config.yaml")
	app.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
	app.RunExternal = func(context.Context, string, ...string) error { return nil }
	return app, stdout, stderr
}

func makeRuntimeBatch(batch fixtureBatch, allowlist []string) inventory.RuntimeBatch {
	allowed := make(map[string]struct{}, len(allowlist))
	for _, key := range allowlist {
		allowed[key] = struct{}{}
	}
	result := inventory.RuntimeBatch{Observations: make([]inventory.RuntimeObservation, 0, len(batch.Observations))}
	for _, value := range batch.Observations {
		labels := make(map[string]string)
		for key, labelValue := range value.OCILabels {
			if _, ok := allowed[key]; ok {
				labels[key] = labelValue
			}
		}
		observation := inventory.RuntimeObservation{
			ExternalID: value.ExternalID, ContainerName: value.ContainerName, ImageReference: value.ImageReference,
			ImageID: value.ImageID, RepoDigests: value.RepoDigests, RepoTags: value.RepoTags, OCILabels: labels,
			State: value.State, Health: value.Health, RestartCount: value.RestartCount,
			StartedAt: value.StartedAt, ObservedAt: value.ObservedAt,
		}
		result.Observations = append(result.Observations, observation)
		if observation.ObservedAt.After(result.ObservedAt) {
			result.ObservedAt = observation.ObservedAt
		}
	}
	return result
}

func runExpectedCLI(t *testing.T, app *cli.App, stdout, stderr *bytes.Buffer, projectDir, correlationID string, expected fixtureCLIExpected) map[string]any {
	t.Helper()
	if len(expected.Command) < 2 || expected.Command[0] != "prodmap" {
		t.Fatalf("expected CLI command must begin with prodmap: %#v", expected.Command)
	}
	args := make([]string, 0, len(expected.Command)+2)
	for _, argument := range expected.Command[1:] {
		if argument == "<correlation_id>" {
			argument = correlationID
		}
		args = append(args, argument)
	}
	args = append(args, "--project-dir", projectDir)
	stdout.Reset()
	stderr.Reset()
	code := app.Run(context.Background(), args)
	if code != expected.ExitCode {
		t.Fatalf("%v exit=%d, want %d; stdout=%q stderr=%q", args, code, expected.ExitCode, stdout.String(), stderr.String())
	}
	if stderr.String() != expected.Stderr {
		t.Fatalf("%v stderr=%q, want %q", args, stderr.String(), expected.Stderr)
	}
	actual := assertOneJSONValue(t, stdout.Bytes())
	var wanted any
	decodeStrictJSONBytes(t, "expected-cli.stdout", expected.Stdout, &wanted)
	actualNormalized := normalizeFixtureEnvelope(actual)
	wantedNormalized := normalizeFixtureEnvelope(wanted)
	if !reflect.DeepEqual(actualNormalized, wantedNormalized) {
		actualJSON, _ := json.MarshalIndent(actualNormalized, "", "  ")
		wantedJSON, _ := json.MarshalIndent(wantedNormalized, "", "  ")
		t.Fatalf("normalized CLI golden mismatch\n--- got ---\n%s\n--- want ---\n%s", actualJSON, wantedJSON)
	}
	envelope, ok := actual.(map[string]any)
	if !ok {
		t.Fatalf("CLI stdout is not an envelope object: %#v", actual)
	}
	return envelope
}

func runFixtureJSON(t *testing.T, app *cli.App, stdout, stderr *bytes.Buffer, args ...string) map[string]any {
	t.Helper()
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), args); code != 0 {
		t.Fatalf("%v exit=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("%v JSON wrote stderr: %q", args, stderr.String())
	}
	value := assertOneJSONValue(t, stdout.Bytes())
	envelope, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%v stdout is not an object: %#v", args, value)
	}
	if envelope["schema_version"] != fixtureSchemaVersion {
		t.Fatalf("%v schema_version=%v", args, envelope["schema_version"])
	}
	return envelope
}

func envelopeItems(t *testing.T, envelope map[string]any) []map[string]any {
	t.Helper()
	data, ok := envelope["data"].(map[string]any)
	if !ok {
		t.Fatalf("envelope data = %#v", envelope["data"])
	}
	rawItems, ok := data["items"].([]any)
	if !ok {
		t.Fatalf("envelope items = %#v", data["items"])
	}
	items := make([]map[string]any, 0, len(rawItems))
	for _, raw := range rawItems {
		item, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("runtime item = %#v", raw)
		}
		items = append(items, item)
	}
	return items
}

func assertExpectedProvenance(t *testing.T, envelope map[string]any, expected fixtureExpected) {
	t.Helper()
	if envelope["schema_version"] != expected.SchemaVersion || envelope["command"] != "explain" {
		t.Fatalf("explain envelope identity = (%v, %v)", envelope["schema_version"], envelope["command"])
	}
	warnings := stringSlice(t, envelope["warnings"])
	if !reflect.DeepEqual(warnings, nonNilExpectedStrings(expected.Provenance.Warnings)) {
		t.Fatalf("warnings = %#v, want %#v", warnings, expected.Provenance.Warnings)
	}
	data := envelope["data"].(map[string]any)
	if data["target_type"] != "correlation" || data["relation_type"] != string(expected.Provenance.RelationType) || data["confidence"] != string(expected.Provenance.Level) || data["score"] != expected.Provenance.Score {
		t.Fatalf("provenance summary = %#v, want relation=%s level=%s score=%v", data, expected.Provenance.RelationType, expected.Provenance.Level, expected.Provenance.Score)
	}
	missing := stringSlice(t, data["missing"])
	if !reflect.DeepEqual(missing, nonNilExpectedStrings(expected.Provenance.Missing)) {
		t.Fatalf("missing = %#v, want %#v", missing, expected.Provenance.Missing)
	}
	kinds := make([]correlation.EvidenceKind, 0)
	contradicting := 0
	for _, key := range []string{"supporting_evidence", "contradicting_evidence", "neutral_evidence"} {
		values, ok := data[key].([]any)
		if !ok {
			t.Fatalf("full explanation field %s = %#v", key, data[key])
		}
		if key == "contradicting_evidence" {
			contradicting = len(values)
		}
		for _, raw := range values {
			value := raw.(map[string]any)
			kinds = append(kinds, correlation.EvidenceKind(value["kind"].(string)))
		}
	}
	if !sameEvidenceKinds(kinds, expected.Provenance.EvidenceKinds) {
		t.Fatalf("evidence kinds = %#v, want %#v", kinds, expected.Provenance.EvidenceKinds)
	}
	if got := contradicting > 0; got != expected.Provenance.Contradiction {
		t.Fatalf("contradiction=%t, want %t", got, expected.Provenance.Contradiction)
	}
	entities := data["entities"].(map[string]any)
	commitSHA, _ := entities["commit_sha"].(string)
	if expected.Provenance.CommitSHA == nil && commitSHA != "" {
		t.Fatalf("commit SHA=%q, want absent", commitSHA)
	}
	if expected.Provenance.CommitSHA != nil && commitSHA != *expected.Provenance.CommitSHA {
		t.Fatalf("commit SHA=%q, want %q", commitSHA, *expected.Provenance.CommitSHA)
	}
}

func assertPersistedLabelsAndRedaction(t *testing.T, projectDir string, input fixtureDocument, expected fixtureExpected, stdout, stderr string) {
	t.Helper()
	databasePath := filepath.Join(projectDir, ".prodmap", "prodmap.db")
	raw, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	database := string(raw)
	for key, value := range expected.Provenance.PersistedOCILabels {
		if !strings.Contains(database, key) || !strings.Contains(database, value) {
			t.Fatalf("database does not contain expected OCI metadata %q=%q", key, value)
		}
	}
	allowedExpected := expected.Provenance.PersistedOCILabels
	forbiddenMarkers := []string{
		"FIXTURE_SOURCE_SECRET", "FIXTURE_QUERY_SECRET", "FIXTURE_FRAGMENT_SECRET", "FIXTURE_VERSION_TOKEN",
		"REDACTED_FIXTURE_VALUE", "com.example.secret", "com.example.internal.note",
	}
	for surface, text := range map[string]string{"database": database, "stdout": stdout, "stderr": stderr} {
		for _, forbidden := range forbiddenMarkers {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s contains forbidden fixture marker %q", surface, forbidden)
			}
		}
	}
	for _, batch := range input.Batches {
		for _, observation := range batch.Observations {
			for key, value := range observation.OCILabels {
				if expectedValue, ok := allowedExpected[key]; ok && expectedValue == value {
					continue
				}
				upperValue := strings.ToUpper(value)
				for surface, text := range map[string]string{"database": database, "stdout": stdout, "stderr": stderr} {
					if strings.Contains(strings.ToLower(key), "secret") && strings.Contains(text, key) {
						t.Fatalf("%s contains forbidden OCI label key %q", surface, key)
					}
					if (strings.Contains(upperValue, "SECRET") || strings.Contains(upperValue, "TOKEN") || strings.Contains(value, "REDACTED_FIXTURE_VALUE")) && strings.Contains(text, value) {
						t.Fatalf("%s contains forbidden OCI label value for %q", surface, key)
					}
				}
			}
		}
	}
}

func validateFixtureContracts(t *testing.T, input fixtureDocument, config fixtureConfig, expected fixtureExpected, expectedCLI fixtureCLIExpected) {
	t.Helper()
	for name, version := range map[string]string{"input": input.SchemaVersion, "config": config.SchemaVersion, "expected": expected.SchemaVersion, "expected-cli": expectedCLI.SchemaVersion} {
		if version != fixtureSchemaVersion {
			t.Fatalf("%s schema_version=%q, want %q", name, version, fixtureSchemaVersion)
		}
	}
	if !input.Clock.Equal(config.Clock) || !input.Clock.Equal(expected.Clock) {
		t.Fatalf("fixture clocks differ: input=%s config=%s expected=%s", input.Clock, config.Clock, expected.Clock)
	}
	if config.Environment != "default" {
		t.Fatalf("fixture environment=%q, Phase 1 supports only default", config.Environment)
	}
	supported := map[string]bool{
		"org.opencontainers.image.revision": true,
		"org.opencontainers.image.source":   true,
		"org.opencontainers.image.created":  true,
	}
	seen := make(map[string]bool)
	for _, key := range config.OCILabelAllowlist {
		if !supported[key] {
			t.Fatalf("fixture OCI label %q is not supported by the effective Phase 1 allowlist", key)
		}
		if seen[key] {
			t.Fatalf("duplicate fixture OCI label %q", key)
		}
		seen[key] = true
	}
	if len(seen) != len(supported) {
		t.Fatalf("fixture OCI allowlist=%v, want all effective keys", config.OCILabelAllowlist)
	}
	if len(input.Batches) == 0 || expected.ServiceKey == "" || len(expected.RuntimeRecords) == 0 {
		t.Fatal("fixture must declare batches, service key, and expected runtime records")
	}
	sequences := make(map[int]bool)
	for _, batch := range input.Batches {
		if batch.ArrivalSequence < 1 || sequences[batch.ArrivalSequence] || len(batch.Observations) == 0 {
			t.Fatalf("invalid or duplicate arrival_sequence %d", batch.ArrivalSequence)
		}
		sequences[batch.ArrivalSequence] = true
	}
	if len(expectedCLI.Command) < 2 || expectedCLI.Command[0] != "prodmap" || expectedCLI.ExitCode != 0 {
		t.Fatalf("expected CLI contract is not an executable successful prodmap command: %+v", expectedCLI)
	}
	if expectedCLI.Stderr != "" {
		t.Fatalf("successful fixture JSON command must expect empty stderr, got %q", expectedCLI.Stderr)
	}
}

func readStrictFixtureJSON(t *testing.T, path string, target any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decodeStrictJSONBytes(t, path, raw, target)
}

func decodeStrictJSONBytes(t *testing.T, name string, raw []byte, target any) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		t.Fatalf("decode %s trailing data: %v", name, err)
	}
}

func assertOneJSONValue(t *testing.T, raw []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("invalid JSON stdout %q: %v", raw, err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		t.Fatalf("stdout contains more than one JSON value %q: %v", raw, err)
	}
	return value
}

func normalizeFixtureEnvelope(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			if key == "generated_at" {
				result[key] = "<generated-at>"
			} else {
				result[key] = normalizeFixtureEnvelope(item)
			}
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = normalizeFixtureEnvelope(item)
		}
		return result
	case string:
		if fixtureUUIDPattern.MatchString(typed) {
			return "<uuid>"
		}
		return typed
	default:
		return value
	}
}

func stringSlice(t *testing.T, raw any) []string {
	t.Helper()
	values, ok := raw.([]any)
	if !ok {
		t.Fatalf("value is not a JSON array: %#v", raw)
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			t.Fatalf("array value is not a string: %#v", value)
		}
		result = append(result, text)
	}
	return result
}

func nonNilExpectedStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
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

func TestStrictFixtureDecoderRejectsUnknownFields(t *testing.T) {
	var config fixtureConfig
	decoder := json.NewDecoder(strings.NewReader(`{"schema_version":"1.0","clock":"2026-08-19T12:00:00Z","environment":"default","oci_label_allowlist":[],"ignored":true}`))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("strict fixture decoder error=%v, want unknown field", err)
	}
}

func Example_fixtureCLIContract() {
	fmt.Println("fixture commands run through cli.App.Run with a fixed clock")
	// Output: fixture commands run through cli.App.Run with a fixed clock
}
