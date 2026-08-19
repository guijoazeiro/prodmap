// Package correlation evaluates and explains provenance relationships.
package correlation

import "time"

const AlgorithmVersion = "provenance/v0-experimental"

type Level string

const (
	LevelExact   Level = "EXACT"
	LevelHigh    Level = "HIGH"
	LevelMedium  Level = "MEDIUM"
	LevelLow     Level = "LOW"
	LevelUnknown Level = "UNKNOWN"
)

type RelationType string

const (
	RelationExact    RelationType = "exact"
	RelationInferred RelationType = "inferred"
)

type EvidenceKind string

const (
	EvidenceIdentity      EvidenceKind = "identity"
	EvidenceTemporal      EvidenceKind = "temporal"
	EvidenceDataQuality   EvidenceKind = "data_quality"
	EvidenceContradiction EvidenceKind = "contradiction"
)

type Polarity string

const (
	PolaritySupports    Polarity = "supports"
	PolarityContradicts Polarity = "contradicts"
	PolarityNeutral     Polarity = "neutral"
)

type Evidence struct {
	Kind       EvidenceKind
	Subject    string
	Claim      string
	Polarity   Polarity
	Strength   float64
	Source     string
	ObservedAt time.Time
	Details    map[string]string
}

type ProvenanceInput struct {
	ImmutableIdentity   string
	MutableAlias        string
	OCIRevision         string
	ResolvedCommitSHA   string
	RevisionInvalid     bool
	RevisionNotFound    bool
	RevisionMismatch    bool
	IdentityConflict    bool
	ImageCreatedAt      *time.Time
	CommitTime          *time.Time
	ImageCreatedInvalid bool
	ObservedAt          time.Time
}

type Result struct {
	RelationType RelationType
	Level        Level
	Score        float64
	Algorithm    string
	Conclusion   string
	Missing      []string
	Warnings     []string
	Evidence     []Evidence
}
