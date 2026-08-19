// Package inventory owns runtime, artifact, service, and snapshot use cases.
package inventory

import (
	"context"
	"time"

	"github.com/guijoazeiro/prodmap/internal/correlation"
)

type Repository struct {
	ExternalID   string
	Name         string
	CanonicalURL string
	RootPathHash string
}

type Commit struct {
	SHA        string
	AuthorTime *time.Time
	CommitTime time.Time
	Subject    string
	TreeSHA    string
}

type RuntimeObservation struct {
	ExternalID     string
	ContainerName  string
	ImageReference string
	ImageID        string
	RepoDigests    []string
	RepoTags       []string
	OCILabels      map[string]string
	State          string
	Health         string
	RestartCount   int64
	StartedAt      *time.Time
	ObservedAt     time.Time
}

type RuntimeBatch struct {
	ObservedAt   time.Time
	Observations []RuntimeObservation
	Rejected     int
	Warnings     []string
}

type RuntimeSource interface {
	InspectRuntime(ctx context.Context) (RuntimeBatch, error)
}

type CommitSource interface {
	Repository(ctx context.Context) (Repository, error)
	ResolveCommit(ctx context.Context, revision string) (Commit, error)
}

type Artifact struct {
	Name              string
	IdentityKind      string
	Identity          string
	DigestAlgorithm   string
	Digest            string
	ImageID           string
	ObservedReference string
	Aliases           []string
	OCIRevision       string
	OCILabels         map[string]string
	IdentityIssues    []string
	MetadataIssues    []string
	RevisionInvalid   bool
}

type SnapshotItem struct {
	ServiceLogicalKey  string
	ServiceDisplayName string
	Environment        string
	Runtime            RuntimeObservation
	Artifact           Artifact
	Commit             *Commit
	Correlation        correlation.Result
}

type Snapshot struct {
	OperationID string
	ObservedAt  time.Time
	Repository  *Repository
	Items       []SnapshotItem
	Rejected    int
	Warnings    []string
}

type RefreshResult struct {
	OperationID      string
	ObservedAt       time.Time
	Processed        int
	Rejected         int
	Services         int
	RuntimeInstances int
	Warnings         []string
}

type SnapshotStore interface {
	SaveRuntimeSnapshot(ctx context.Context, snapshot Snapshot) (RefreshResult, error)
}

type RuntimeQuery struct {
	Service     string
	Environment string
	At          time.Time
	Limit       int
	Cursor      string
}

type RuntimeRecord struct {
	ID               string
	ExternalID       string
	ContainerName    string
	ServiceID        string
	ServiceKey       string
	Environment      string
	RuntimeKind      string
	State            string
	Health           string
	RestartCount     int64
	StartedAt        *time.Time
	ObservedAt       time.Time
	ImageReference   string
	ImageID          string
	ImageDigest      string
	ArtifactID       string
	ArtifactIdentity string
	CommitID         string
	CommitSHA        string
	CorrelationID    string
	Confidence       correlation.Level
	Score            float64
	EvidenceIDs      []string
}

type ServiceRecord struct {
	ID               string
	LogicalKey       string
	Environment      string
	DisplayName      string
	RuntimeInstances int
	State            string
	Health           string
	ArtifactIdentity string
	CommitConfidence correlation.Level
	Freshness        time.Time
}

type Status struct {
	LastSnapshot     *time.Time
	Services         int
	RuntimeInstances int
}

type Explanation struct {
	TargetID           string
	TargetType         string
	Conclusion         string
	RelationType       correlation.RelationType
	Confidence         correlation.Level
	Score              float64
	Algorithm          string
	Entities           map[string]string
	Supporting         []PersistedEvidence
	Contradicting      []PersistedEvidence
	Neutral            []PersistedEvidence
	Missing            []string
	Warnings           []string
	Sources            []string
	ObservedAt         time.Time
	Limitations        []string
	ScoreComponents    []correlation.ScoreComponent
	HardCaps           []correlation.HardCap
	ResolutionAttempts []correlation.ResolutionAttempt
	SourceFreshness    []correlation.SourceFreshness
}

type PersistedEvidence struct {
	ID         string
	Kind       correlation.EvidenceKind
	Subject    string
	Claim      string
	Polarity   correlation.Polarity
	Strength   float64
	Source     string
	ObservedAt time.Time
	Details    map[string]string
}

type Reader interface {
	Status(ctx context.Context) (Status, error)
	Services(ctx context.Context) ([]ServiceRecord, error)
	Runtime(ctx context.Context, query RuntimeQuery) ([]RuntimeRecord, string, error)
	Explain(ctx context.Context, selector string) (Explanation, error)
}
