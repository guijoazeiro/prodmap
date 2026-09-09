// Package deployment owns the offline deployment-ledger contract.
package deployment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/identity"
	"github.com/guijoazeiro/prodmap/internal/redaction"
)

const (
	Format                                  = "deployment-ledger-jsonl/v1"
	AlgorithmVersion                        = "deployment-ledger/v1"
	FingerprintVersion                      = "sha256-v1"
	MaxFileBytes                      int64 = 16 << 20
	MaxLineBytes                            = 1 << 20
	MaxRecords                              = 10000
	RuntimeAlgorithmVersion                 = "deployment-runtime/v1"
	MaxRuntimeCandidatesPerDeployment       = 100
	MaxRuntimeCandidatesPerPage             = 10000
	DiscoveryDefaultLimit                   = 20
	DiscoveryMaxLimit                       = 100
)

const MaxRuntimeConfirmationDelay = 30 * time.Minute

type Record struct {
	DeploymentID        string
	DeployedAt          time.Time
	BuildStartedAt      time.Time
	BuildDate           time.Time
	Environment         string
	Service             string
	Version             string
	ScenarioProfile     string
	GitHead             string
	GitDirty            bool
	VCSRevision         string
	VCSRevisionVerified bool
	ImageReference      string
	ImageID             string
	RepoDigest          *string
	ComposeProject      string
	Status              string
	Fingerprint         string
}

type Snapshot struct {
	SourceKey  string
	SourceHash string
	Format     string
	ObservedAt time.Time
	Records    []Record
}

// SourceQuery describes the logical GitHub Actions artifact to fetch. It has no
// transport credentials or HTTP details.
type SourceQuery struct {
	Owner, Repository, ArtifactName string
	ObservedAt                      time.Time
}

// SourceResult carries only sanitized artifact metadata and a validated ledger.
type SourceResult struct {
	Repository, ArtifactName, ArtifactDigest, WorkflowHeadSHA string
	ArtifactID, WorkflowRunID                                 int64
	CreatedAt, UpdatedAt, ExpiresAt                           time.Time
	Snapshot                                                  Snapshot
	Warnings                                                  []string
}

type SyncResult struct {
	Ingestion           IngestResult
	SourceObservationID string
	SourceExisting      bool
}

// DeploymentSource is consumer-owned: implementations provide a validated
// deployment-ledger snapshot and never expose transport responses.
type DeploymentSource interface {
	Fetch(context.Context, SourceQuery) (SourceResult, error)
}

type IngestResult struct {
	IngestionID         string
	SourceHash          string
	Format              string
	RecordsSeen         int
	DeploymentsInserted int
	DeploymentsExisting int
	IdempotentReplay    bool
	Warnings            []string
}

type Evidence struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Subject  string `json:"subject"`
	Claim    string `json:"claim"`
	Polarity string `json:"polarity"`
	Source   string `json:"source"`
}

type Provenance struct {
	Status      string
	Confidence  string
	Basis       string
	ArtifactID  *string
	RepoDigest  *string
	ImageID     string
	CommitID    *string
	CommitSHA   *string
	Verified    bool
	Evidence    []Evidence
	Limitations []string
}

type Deployment struct {
	ID                 string
	ExternalID         string
	Environment        string
	Service            string
	Status             string
	Strategy           string
	StartedAt          time.Time
	FinishedAt         *time.Time
	Provenance         Provenance
	RuntimeAssociation RuntimeAssociation
	NextDeploymentAt   *time.Time
	Concurrent         bool
}

// RuntimeCandidate is sanitized runtime evidence supplied by a query adapter.
type RuntimeCandidate struct {
	ID, SourceID, ExternalID, ImageID, ImageDigest, ArtifactIdentity, CommitSHA string
	ObservedAt, ValidFrom                                                       time.Time
	ValidTo                                                                     *time.Time
	State, Health                                                               string
	Preexisting                                                                 bool
}

type RuntimeEvidence struct {
	Fingerprint string    `json:"fingerprint"`
	Kind        string    `json:"kind"`
	Subject     string    `json:"subject"`
	Claim       string    `json:"claim"`
	Polarity    string    `json:"polarity"`
	Source      string    `json:"source"`
	ObservedAt  time.Time `json:"observed_at"`
}

type AssociatedRuntime struct {
	ID                         string
	ObservedAt                 time.Time
	State, Health              string
	ArtifactMatch, CommitMatch bool
}

type RuntimeAssociation struct {
	Status, Confidence, Basis, AlgorithmVersion                 string
	WindowStart, WindowEnd                                      time.Time
	WindowEndReason                                             string
	CandidateInstances, MatchedInstances, ContradictedInstances int
	RuntimeInstances                                            []AssociatedRuntime
	Evidence                                                    []RuntimeEvidence
	Limitations                                                 []string
	CausalityClaimed                                            bool
}

type RuntimeAssociationInput struct {
	DeploymentID                                            string
	ArtifactRepoDigest                                      *string
	ArtifactImageID                                         string
	CommitSHA                                               *string
	CommitVerified                                          bool
	Start, End                                              time.Time
	EndReason                                               string
	Concurrent, CandidateLimitExceeded, Future, Preexisting bool
	Candidates                                              []RuntimeCandidate
}

// EvaluateRuntimeAssociation is deterministic and deliberately non-causal.
func EvaluateRuntimeAssociation(input RuntimeAssociationInput) RuntimeAssociation {
	result := RuntimeAssociation{Status: "UNKNOWN", Confidence: "UNKNOWN", Basis: "no compatible runtime evidence", AlgorithmVersion: RuntimeAlgorithmVersion, WindowStart: input.Start.UTC(), WindowEnd: input.End.UTC(), WindowEndReason: input.EndReason, RuntimeInstances: []AssociatedRuntime{}, Evidence: []RuntimeEvidence{}, Limitations: []string{}, CausalityClaimed: false}
	if input.Concurrent {
		result.Limitations = append(result.Limitations, "concurrent deployments prevent exclusive runtime association")
		return result
	}
	if input.Future {
		result.Limitations = append(result.Limitations, "deployment is after query evidence horizon")
		return result
	}
	if !input.End.After(input.Start) {
		result.WindowEndReason = "empty_window"
		result.Limitations = append(result.Limitations, "runtime confirmation window is empty")
		return result
	}
	if input.CandidateLimitExceeded {
		result.Limitations = append(result.Limitations, "runtime candidate limit exceeded")
		return result
	}
	seen := map[string]struct{}{}
	candidates := slices.Clone(input.Candidates)
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].ObservedAt.Equal(candidates[j].ObservedAt) {
			return candidates[i].ObservedAt.Before(candidates[j].ObservedAt)
		}
		return candidates[i].ID < candidates[j].ID
	})
	deployIDs := immutableIDs(input.ArtifactRepoDigest, input.ArtifactImageID)
	for _, candidate := range candidates {
		key := candidate.SourceID + "\x00" + candidate.ExternalID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		runtimeIDs := immutableIDs(nil, candidate.ImageID, candidate.ImageDigest, candidate.ArtifactIdentity)
		artifactMatch, comparable := intersects(deployIDs, runtimeIDs), len(deployIDs) > 0 && len(runtimeIDs) > 0
		commitMatch, commitConflict := false, false
		if input.CommitVerified && input.CommitSHA != nil && candidate.CommitSHA != "" {
			commitMatch = *input.CommitSHA == candidate.CommitSHA
			commitConflict = !commitMatch
		}
		entry := AssociatedRuntime{ID: candidate.ID, ObservedAt: candidate.ObservedAt.UTC(), State: candidate.State, Health: candidate.Health, ArtifactMatch: artifactMatch, CommitMatch: commitMatch}
		result.RuntimeInstances = append(result.RuntimeInstances, entry)
		result.CandidateInstances++
		if artifactMatch && !commitConflict {
			result.MatchedInstances++
			result.Evidence = append(result.Evidence, runtimeEvidence(input.DeploymentID, candidate, "supports", "immutable artifact identity matched runtime observation"))
			if candidate.Preexisting {
				result.Limitations = append(result.Limitations, "artifact already observed before deployment")
			}
		} else if comparable || commitConflict {
			result.ContradictedInstances++
			result.Evidence = append(result.Evidence, runtimeEvidence(input.DeploymentID, candidate, "contradicts", "immutable artifact identity or verified commit differed from runtime observation"))
		}
	}
	if result.CandidateInstances == 0 {
		result.Limitations = append(result.Limitations, "no runtime observations in confirmation window")
		return result
	}
	if result.MatchedInstances > 0 && result.ContradictedInstances > 0 {
		result.Status, result.Confidence, result.Basis = "PARTIAL", "MEDIUM", "compatible and contradictory immutable runtime identities observed"
		return result
	}
	if result.ContradictedInstances > 0 {
		result.Status, result.Basis = "CONTRADICTED", "immutable runtime identity or verified commit contradicts deployment"
		return result
	}
	if result.MatchedInstances > 0 {
		if input.Preexisting || slices.ContainsFunc(candidates, func(candidate RuntimeCandidate) bool {
			return candidate.Preexisting
		}) {
			result.Status, result.Confidence, result.Basis = "PARTIAL", "MEDIUM", "immutable artifact was already observed before deployment"
			return result
		}
		result.Status, result.Confidence, result.Basis = "MATCHED", "HIGH", "immutable artifact identity observed in compatible runtime window"
		return result
	}
	result.Limitations = append(result.Limitations, "runtime identity is not comparable to deployment identity")
	return result
}

func runtimeEvidence(deploymentID string, candidate RuntimeCandidate, polarity, claim string) RuntimeEvidence {
	identity := candidate.ImageID
	if identity == "" {
		identity = candidate.ImageDigest
	}
	payload := deploymentID + "\x00" + candidate.ID + "\x00" + identity + "\x00" + polarity + "\x00" + RuntimeAlgorithmVersion
	digest := sha256.Sum256([]byte(payload))
	return RuntimeEvidence{Fingerprint: FingerprintVersion + ":" + hex.EncodeToString(digest[:]), Kind: "identity", Subject: "runtime artifact", Claim: claim, Polarity: polarity, Source: "docker_runtime", ObservedAt: candidate.ObservedAt.UTC()}
}

func immutableIDs(repo *string, values ...string) map[string]struct{} {
	result := map[string]struct{}{}
	if repo != nil {
		_, value, ok := strings.Cut(*repo, "@")
		if ok && validImageIdentity(value) {
			result[value] = struct{}{}
		}
	}
	for _, value := range values {
		if validImageIdentity(value) {
			result[value] = struct{}{}
		}
	}
	return result
}
func intersects(left, right map[string]struct{}) bool {
	for value := range left {
		if _, ok := right[value]; ok {
			return true
		}
	}
	return false
}

type Query struct {
	Environment string
	Service     string
	Status      string
	Since       time.Time
	Until       time.Time
	Limit       int
	Cursor      string
}

type QueryResult struct {
	Since       time.Time
	Until       time.Time
	Environment string
	Items       []Deployment
	NextCursor  string
}

// DiscoveryQuery describes the bounded, lightweight read model used by MCP.
// Cursor fields are internal keyset values decoded by the MCP boundary.
type DiscoveryQuery struct {
	Environment, Service, Status string
	Since, Until                 time.Time
	Limit                        int
	CursorStartedAt              time.Time
	CursorID                     string
}

// DiscoveryItem deliberately excludes ledger, artifact, and runtime details.
type DiscoveryItem struct {
	ID, Environment, Service, Status, Strategy string
	StartedAt                                  time.Time
	Provenance                                 DiscoveryProvenance
}

type DiscoveryProvenance struct {
	Status, Confidence, Basis string
	Limitations               []string
}

type DiscoveryResult struct {
	Items      []DiscoveryItem
	HasMore    bool
	LastCursor DiscoveryItem
}

// ValidStatus reports whether value belongs to the closed deployment-status contract.
func ValidStatus(value string) bool {
	return slices.Contains([]string{"pending", "running", "succeeded", "failed", "cancelled", "rolled_back", "unknown"}, value)
}

type Store interface {
	SaveDeployment(context.Context, Snapshot) (IngestResult, error)
	Deployments(context.Context, Query) (QueryResult, error)
}

// ValidateSnapshot validates a sanitized snapshot again at the persistence boundary.
func ValidateSnapshot(snapshot Snapshot) error {
	if snapshot.Format != Format || !validSHA256Value(snapshot.SourceKey) || !validSHA256Value(snapshot.SourceHash) || snapshot.ObservedAt.IsZero() || snapshot.ObservedAt.Location() != time.UTC || len(snapshot.Records) == 0 || len(snapshot.Records) > MaxRecords {
		return fmt.Errorf("%w: incomplete deployment snapshot", errs.ErrInvalid)
	}
	seen := map[string]struct{}{}
	for _, record := range snapshot.Records {
		if err := validateRecord(record); err != nil {
			return err
		}
		if record.Fingerprint != fingerprint(record) {
			return fmt.Errorf("%w: deployment record fingerprint is invalid", errs.ErrInvalid)
		}
		key := record.DeploymentID + "\x00" + record.Environment + "\x00" + record.Service
		if _, found := seen[key]; found {
			return fmt.Errorf("%w: duplicate deployment identity", errs.ErrInvalid)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// ValidateSourceResult rechecks metadata supplied by a DeploymentSource before
// persistence. The logical SourceKey is derived, never caller-selected.
func ValidateSourceResult(result SourceResult) error {
	if err := ValidateSnapshot(result.Snapshot); err != nil {
		return err
	}
	owner, repository, ok := strings.Cut(result.Repository, "/")
	if !ok || strings.Contains(repository, "/") || !validGitHubSourcePart(owner, 100) || !validGitHubSourcePart(repository, 100) || owner != strings.ToLower(owner) || repository != strings.ToLower(repository) || !validGitHubArtifactName(result.ArtifactName) || result.ArtifactID <= 0 || result.WorkflowRunID <= 0 || !validSHA256Value(result.ArtifactDigest) || !validGitHubWorkflowSHA(result.WorkflowHeadSHA) {
		return fmt.Errorf("%w: invalid GitHub Actions deployment source result", errs.ErrInvalid)
	}
	for _, timestamp := range []time.Time{result.CreatedAt, result.UpdatedAt, result.ExpiresAt, result.Snapshot.ObservedAt} {
		if timestamp.IsZero() || timestamp.Location() != time.UTC {
			return fmt.Errorf("%w: invalid GitHub Actions source timestamp", errs.ErrInvalid)
		}
	}
	if result.UpdatedAt.Before(result.CreatedAt) || !result.ExpiresAt.After(result.CreatedAt) {
		return fmt.Errorf("%w: inconsistent GitHub Actions source timestamps", errs.ErrInvalid)
	}
	for _, record := range result.Snapshot.Records {
		if record.VCSRevisionVerified && (record.VCSRevision != result.WorkflowHeadSHA || record.GitHead != result.WorkflowHeadSHA) {
			return fmt.Errorf("%w: verified deployment revision does not match GitHub Actions workflow head", errs.ErrInvalid)
		}
	}
	payload := "github-actions-ledger/v1\x00" + owner + "\x00" + repository + "\x00" + result.ArtifactName
	digest := sha256.Sum256([]byte(payload))
	if result.Snapshot.SourceKey != "sha256:"+hex.EncodeToString(digest[:]) || result.Warnings == nil {
		return fmt.Errorf("%w: inconsistent GitHub Actions deployment source result", errs.ErrInvalid)
	}
	for _, warning := range result.Warnings {
		if err := safeString("warning", warning, 512); err != nil {
			return err
		}
	}
	return nil
}

func validSHA256Value(value string) bool {
	return strings.HasPrefix(value, "sha256:") && len(value) == 71 && validHex(value[7:])
}

type rawRecord struct {
	SchemaVersion       string  `json:"schema_version"`
	DeploymentID        string  `json:"deployment_id"`
	DeployedAt          string  `json:"deployed_at"`
	BuildStartedAt      string  `json:"build_started_at"`
	BuildDate           string  `json:"build_date"`
	Environment         string  `json:"environment"`
	Service             string  `json:"service"`
	Version             string  `json:"version"`
	ScenarioProfile     string  `json:"scenario_profile"`
	GitHead             string  `json:"git_head"`
	GitDirty            bool    `json:"git_dirty"`
	VCSRevision         string  `json:"vcs_revision"`
	VCSRevisionVerified bool    `json:"vcs_revision_verified"`
	ImageReference      string  `json:"image_reference"`
	ImageID             string  `json:"image_id"`
	RepoDigest          *string `json:"repo_digest"`
	ComposeProject      string  `json:"compose_project"`
	Status              string  `json:"status"`
}

var requiredFields = []string{"schema_version", "deployment_id", "deployed_at", "build_started_at", "build_date", "environment", "service", "version", "scenario_profile", "git_head", "git_dirty", "vcs_revision", "vcs_revision_verified", "image_reference", "image_id", "repo_digest", "compose_project", "status"}

// LoadFrozenFile validates every record before a caller opens SQLite.
func LoadFrozenFile(ctx context.Context, name string, observedAt time.Time) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	abs, err := filepath.Abs(name)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%w: resolve ledger file", errs.ErrInvalid)
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return Snapshot{}, classifyFileError(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Snapshot{}, fmt.Errorf("%w: ledger file must be a regular non-symlink file", errs.ErrInvalid)
	}
	if info.Size() > MaxFileBytes {
		return Snapshot{}, fmt.Errorf("%w: ledger file exceeds limit", errs.ErrInvalid)
	}
	file, err := os.Open(abs)
	if err != nil {
		return Snapshot{}, classifyFileError(err)
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, MaxFileBytes+1))
	if err != nil {
		return Snapshot{}, fmt.Errorf("%w: read ledger file", errs.ErrUnavailable)
	}
	if int64(len(contents)) > MaxFileBytes {
		return Snapshot{}, fmt.Errorf("%w: ledger file exceeds limit", errs.ErrInvalid)
	}
	after, err := os.Stat(abs)
	if err != nil {
		return Snapshot{}, classifyFileError(err)
	}
	if after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return Snapshot{}, fmt.Errorf("%w: ledger file changed during read", errs.ErrConflict)
	}
	if !utf8.Valid(contents) {
		return Snapshot{}, fmt.Errorf("%w: ledger file has invalid UTF-8", errs.ErrInvalid)
	}
	pathHash := sha256.Sum256([]byte(filepath.Clean(abs)))
	return loadLedgerContents(ctx, contents, "sha256:"+hex.EncodeToString(pathHash[:]), observedAt)
}

// LoadGitHubActionsLedger validates bytes already authenticated as the exact
// deployments.jsonl member of a GitHub Actions artifact. Callers cannot choose
// a SourceKey; it is derived from the logical remote source.
func LoadGitHubActionsLedger(ctx context.Context, contents []byte, owner, repository, artifactName string, observedAt time.Time) (Snapshot, error) {
	if !validGitHubSourcePart(owner, 100) || !validGitHubSourcePart(repository, 100) || !validGitHubArtifactName(artifactName) {
		return Snapshot{}, fmt.Errorf("%w: invalid GitHub Actions source", errs.ErrInvalid)
	}
	payload := "github-actions-ledger/v1\x00" + strings.ToLower(owner) + "\x00" + strings.ToLower(repository) + "\x00" + artifactName
	digest := sha256.Sum256([]byte(payload))
	return loadLedgerContents(ctx, contents, "sha256:"+hex.EncodeToString(digest[:]), observedAt)
}

func loadLedgerContents(ctx context.Context, contents []byte, sourceKey string, observedAt time.Time) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if int64(len(contents)) > MaxFileBytes {
		return Snapshot{}, fmt.Errorf("%w: ledger file exceeds limit", errs.ErrInvalid)
	}
	if !utf8.Valid(contents) {
		return Snapshot{}, fmt.Errorf("%w: ledger file has invalid UTF-8", errs.ErrInvalid)
	}
	hash := sha256.Sum256(contents)
	snapshot := Snapshot{SourceKey: sourceKey, SourceHash: "sha256:" + hex.EncodeToString(hash[:]), Format: Format, ObservedAt: observedAt.UTC(), Records: []Record{}}
	lines := strings.Split(string(contents), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 || len(lines) > MaxRecords {
		return Snapshot{}, fmt.Errorf("%w: ledger record count is invalid", errs.ErrInvalid)
	}
	identities := make(map[string]struct{}, len(lines))
	for index, line := range lines {
		if err := ctx.Err(); err != nil {
			return Snapshot{}, err
		}
		if line == "" || len(line) > MaxLineBytes {
			return Snapshot{}, lineError(index+1, "line", "invalid")
		}
		record, err := parseRecord(line)
		if err != nil {
			return Snapshot{}, fmt.Errorf("line %d: %w", index+1, err)
		}
		identityKey := record.DeploymentID + "\x00" + record.Environment + "\x00" + record.Service
		if _, found := identities[identityKey]; found {
			return Snapshot{}, lineError(index+1, "deployment_id", "duplicate identity")
		}
		identities[identityKey] = struct{}{}
		snapshot.Records = append(snapshot.Records, record)
	}
	sort.Slice(snapshot.Records, func(i, j int) bool {
		left, right := snapshot.Records[i], snapshot.Records[j]
		if !left.DeployedAt.Equal(right.DeployedAt) {
			return left.DeployedAt.Before(right.DeployedAt)
		}
		if left.Environment != right.Environment {
			return left.Environment < right.Environment
		}
		if left.Service != right.Service {
			return left.Service < right.Service
		}
		if left.DeploymentID != right.DeploymentID {
			return left.DeploymentID < right.DeploymentID
		}
		return left.Fingerprint < right.Fingerprint
	})
	return snapshot, nil
}

func parseRecord(line string) (Record, error) {
	values, err := strictObject(line)
	if err != nil {
		return Record{}, err
	}
	if len(values) != len(requiredFields) {
		return Record{}, fmt.Errorf("%w: unexpected field", errs.ErrInvalid)
	}
	for _, name := range requiredFields {
		if _, ok := values[name]; !ok {
			return Record{}, fmt.Errorf("%w: missing field %s", errs.ErrInvalid, name)
		}
	}
	for _, name := range []string{"schema_version", "deployment_id", "deployed_at", "build_started_at", "build_date", "environment", "service", "version", "scenario_profile", "git_head", "vcs_revision", "image_reference", "image_id", "compose_project", "status"} {
		if !jsonString(values[name]) {
			return Record{}, fmt.Errorf("%w: wrong field type %s", errs.ErrInvalid, name)
		}
	}
	for _, name := range []string{"git_dirty", "vcs_revision_verified"} {
		if string(values[name]) != "true" && string(values[name]) != "false" {
			return Record{}, fmt.Errorf("%w: wrong field type %s", errs.ErrInvalid, name)
		}
	}
	if string(values["repo_digest"]) != "null" && !jsonString(values["repo_digest"]) {
		return Record{}, fmt.Errorf("%w: wrong field type repo_digest", errs.ErrInvalid)
	}
	var raw rawRecord
	canonical, err := json.Marshal(values)
	if err != nil {
		return Record{}, fmt.Errorf("%w: invalid JSON", errs.ErrInvalid)
	}
	if err := json.Unmarshal(canonical, &raw); err != nil {
		return Record{}, fmt.Errorf("%w: wrong field type", errs.ErrInvalid)
	}
	if raw.SchemaVersion != "1.0" {
		return Record{}, fmt.Errorf("%w: schema_version incompatible", errs.ErrIncompatible)
	}
	if err := validateRaw(raw); err != nil {
		return Record{}, err
	}
	deployedAt, _ := parseUTC(raw.DeployedAt)
	buildStartedAt, _ := parseUTC(raw.BuildStartedAt)
	buildDate, _ := parseUTC(raw.BuildDate)
	record := Record{DeploymentID: raw.DeploymentID, DeployedAt: deployedAt, BuildStartedAt: buildStartedAt, BuildDate: buildDate, Environment: raw.Environment, Service: raw.Service, Version: raw.Version, ScenarioProfile: raw.ScenarioProfile, GitHead: raw.GitHead, GitDirty: raw.GitDirty, VCSRevision: raw.VCSRevision, VCSRevisionVerified: raw.VCSRevisionVerified, ImageReference: raw.ImageReference, ImageID: raw.ImageID, RepoDigest: raw.RepoDigest, ComposeProject: raw.ComposeProject, Status: raw.Status}
	record.Fingerprint = fingerprint(record)
	return record, nil
}

func jsonString(value json.RawMessage) bool {
	return len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"'
}

func strictObject(line string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(strings.NewReader(line))
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("%w: malformed JSON", errs.ErrInvalid)
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, fmt.Errorf("%w: top-level must be object", errs.ErrInvalid)
	}
	values := map[string]json.RawMessage{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("%w: malformed JSON", errs.ErrInvalid)
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, fmt.Errorf("%w: invalid field", errs.ErrInvalid)
		}
		if _, exists := values[key]; exists {
			return nil, fmt.Errorf("%w: duplicate field %s", errs.ErrInvalid, safeField(key))
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("%w: malformed field %s", errs.ErrInvalid, safeField(key))
		}
		values[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, fmt.Errorf("%w: malformed JSON", errs.ErrInvalid)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: trailing JSON", errs.ErrInvalid)
	}
	return values, nil
}

func validateRaw(raw rawRecord) error {
	stringsToCheck := []struct {
		name, value string
		max         int
	}{
		{"deployment_id", raw.DeploymentID, 2048}, {"service", raw.Service, 255}, {"version", raw.Version, 512}, {"scenario_profile", raw.ScenarioProfile, 256}, {"git_head", raw.GitHead, 64}, {"vcs_revision", raw.VCSRevision, 64}, {"image_reference", raw.ImageReference, 1024}, {"image_id", raw.ImageID, 136}, {"compose_project", raw.ComposeProject, 256}, {"status", raw.Status, 32},
	}
	for _, field := range stringsToCheck {
		if err := safeString(field.name, field.value, field.max); err != nil {
			return err
		}
	}
	environment, err := identity.ValidEnvironment(raw.Environment)
	if err != nil || environment != raw.Environment {
		return fmt.Errorf("%w: invalid environment", errs.ErrInvalid)
	}
	if strings.TrimSpace(raw.DeploymentID) != raw.DeploymentID || strings.TrimSpace(raw.Service) != raw.Service {
		return fmt.Errorf("%w: invalid deployment_id", errs.ErrInvalid)
	}
	if !validSHA(raw.GitHead) || !validSHA(raw.VCSRevision) {
		return fmt.Errorf("%w: invalid git revision", errs.ErrInvalid)
	}
	if raw.VCSRevisionVerified && (raw.GitDirty || raw.GitHead == "unknown" || raw.GitHead != raw.VCSRevision) {
		return fmt.Errorf("%w: inconsistent verified revision", errs.ErrInvalid)
	}
	if !raw.VCSRevisionVerified && raw.VCSRevision != "unknown" {
		return fmt.Errorf("%w: inconsistent unverified revision", errs.ErrInvalid)
	}
	if !validImageIdentity(raw.ImageID) {
		return fmt.Errorf("%w: invalid image_id", errs.ErrInvalid)
	}
	if raw.RepoDigest != nil && !validRepoDigest(*raw.RepoDigest) {
		return fmt.Errorf("%w: invalid repo_digest", errs.ErrInvalid)
	}
	if raw.RepoDigest != nil {
		if err := safeString("repo_digest", *raw.RepoDigest, 1200); err != nil {
			return err
		}
	}
	if strings.Contains(raw.ImageReference, "@") || strings.Contains(raw.ImageReference, "?") || strings.Contains(raw.ImageReference, "#") || strings.Contains(raw.ImageReference, "://") {
		return fmt.Errorf("%w: invalid image_reference", errs.ErrInvalid)
	}
	if !ValidStatus(raw.Status) {
		return fmt.Errorf("%w: invalid status", errs.ErrInvalid)
	}
	started, err := parseUTC(raw.BuildStartedAt)
	if err != nil {
		return err
	}
	built, err := parseUTC(raw.BuildDate)
	if err != nil {
		return err
	}
	deployed, err := parseUTC(raw.DeployedAt)
	if err != nil {
		return err
	}
	// The reference ledger records image build_date (image creation) before the
	// deployment script's build_started_at. Preserve that source contract while
	// still rejecting impossible deployment chronology.
	if started.Before(built) || deployed.Before(started) {
		return fmt.Errorf("%w: deployment timestamps out of order", errs.ErrInvalid)
	}
	return nil
}

func validateRecord(record Record) error {
	if record.DeployedAt.Location() != time.UTC || record.BuildStartedAt.Location() != time.UTC || record.BuildDate.Location() != time.UTC {
		return fmt.Errorf("%w: deployment timestamps must be UTC", errs.ErrInvalid)
	}
	return validateRaw(rawRecord{
		SchemaVersion: "1.0", DeploymentID: record.DeploymentID,
		DeployedAt:     record.DeployedAt.Format(time.RFC3339Nano),
		BuildStartedAt: record.BuildStartedAt.Format(time.RFC3339Nano),
		BuildDate:      record.BuildDate.Format(time.RFC3339Nano),
		Environment:    record.Environment, Service: record.Service, Version: record.Version,
		ScenarioProfile: record.ScenarioProfile, GitHead: record.GitHead, GitDirty: record.GitDirty,
		VCSRevision: record.VCSRevision, VCSRevisionVerified: record.VCSRevisionVerified,
		ImageReference: record.ImageReference, ImageID: record.ImageID, RepoDigest: record.RepoDigest,
		ComposeProject: record.ComposeProject, Status: record.Status,
	})
}

func parseUTC(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Location() != time.UTC {
		return time.Time{}, fmt.Errorf("%w: invalid timestamp", errs.ErrInvalid)
	}
	return parsed.UTC(), nil
}
func validSHA(value string) bool {
	if value == "unknown" {
		return true
	}
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func validGitHubWorkflowSHA(value string) bool {
	return value != "unknown" && validSHA(value)
}

func validGitHubSourcePart(value string, maximum int) bool {
	if value == "" || len(value) > maximum || strings.Contains(value, "..") {
		return false
	}
	for index, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.') {
			return false
		}
		if index == 0 && (character == '-' || character == '_' || character == '.') {
			return false
		}
	}
	return true
}

func validGitHubArtifactName(value string) bool {
	if value == "" || len(value) > 255 || strings.Contains(value, "..") || strings.ContainsAny(value, "/\\") || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}
func validImageIdentity(value string) bool {
	return strings.HasPrefix(value, "sha256:") && len(value) == 71 && validHex(value[7:]) || strings.HasPrefix(value, "sha512:") && len(value) == 135 && validHex(value[7:])
}
func validRepoDigest(value string) bool {
	if strings.Contains(value, "@") {
		repository, digest, ok := strings.Cut(value, "@")
		return ok && repository != "" && validImageIdentity(digest) && !strings.ContainsAny(value, "?#") && !strings.Contains(value, "://")
	}
	return false
}
func validHex(value string) bool {
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func safeString(name, value string, max int) error {
	if value == "" || len(value) > max || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("%w: invalid %s", errs.ErrInvalid, name)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: invalid %s", errs.ErrInvalid, name)
		}
	}
	if err := redaction.ValidatePublicValue(redaction.Identifier, value); err != nil {
		return fmt.Errorf("%w: prohibited %s", errs.ErrInvalid, name)
	}
	// SQL keywords are not credential detection. This ledger-specific guard is
	// retained because these fields are identity metadata, not free text.
	if containsLedgerStatement(value) {
		return fmt.Errorf("%w: prohibited %s", errs.ErrInvalid, name)
	}
	return nil
}

func containsLedgerStatement(value string) bool {
	lowered := strings.ToLower(value)
	for _, word := range []string{"select ", "insert ", "update ", "delete "} {
		if strings.Contains(lowered, word) {
			return true
		}
	}
	return false
}
func safeField(value string) string {
	for _, allowed := range requiredFields {
		if value == allowed {
			return value
		}
	}
	return "unknown"
}
func lineError(line int, field, category string) error {
	return fmt.Errorf("%w: line %d field %s %s", errs.ErrInvalid, line, safeField(field), category)
}
func classifyFileError(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: ledger file", errs.ErrNotFound)
	}
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("%w: ledger file", errs.ErrUnauthorized)
	}
	return fmt.Errorf("%w: ledger file", errs.ErrUnavailable)
}
func fingerprint(record Record) string {
	value := struct {
		DeploymentID, Environment, Service, Status, DeployedAt, BuildStartedAt, BuildDate, Commit, ImageID, RepoDigest string
		Verified                                                                                                       bool
	}{record.DeploymentID, record.Environment, record.Service, record.Status, record.DeployedAt.UTC().Format(time.RFC3339Nano), record.BuildStartedAt.UTC().Format(time.RFC3339Nano), record.BuildDate.UTC().Format(time.RFC3339Nano), record.VCSRevision, record.ImageID, valueOrEmpty(record.RepoDigest), record.VCSRevisionVerified}
	bytes, _ := json.Marshal(value)
	digest := sha256.Sum256(bytes)
	return FingerprintVersion + ":" + hex.EncodeToString(digest[:])
}
func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
