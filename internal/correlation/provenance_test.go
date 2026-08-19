package correlation

import (
	"strings"
	"testing"
	"time"
)

func TestEvaluateProvenance(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	sha := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tests := []struct {
		name  string
		input ProvenanceInput
		level Level
		kind  EvidenceKind
	}{
		{"exact", ProvenanceInput{ImmutableIdentity: "sha256:abc", OCIRevision: sha, ResolvedCommitSHA: sha, ObservedAt: now}, LevelExact, EvidenceIdentity},
		{"mutable tag", ProvenanceInput{MutableAlias: "api:latest", ObservedAt: now}, LevelLow, EvidenceDataQuality},
		{"missing revision", ProvenanceInput{ImmutableIdentity: "sha256:abc", ObservedAt: now}, LevelUnknown, EvidenceDataQuality},
		{"revision not found", ProvenanceInput{ImmutableIdentity: "sha256:abc", OCIRevision: sha, RevisionNotFound: true, ObservedAt: now}, LevelUnknown, EvidenceDataQuality},
		{"conflict", ProvenanceInput{ImmutableIdentity: "sha256:abc", IdentityConflict: true, ObservedAt: now}, LevelUnknown, EvidenceContradiction},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := EvaluateProvenance(test.input)
			if result.Level != test.level || result.Algorithm != AlgorithmVersion {
				t.Fatalf("result = %+v", result)
			}
			found := false
			for _, evidence := range result.Evidence {
				found = found || evidence.Kind == test.kind
			}
			if !found {
				t.Fatalf("evidence kind %s absent: %+v", test.kind, result.Evidence)
			}
		})
	}
}

func TestInvalidRevisionEvidenceDoesNotEchoUntrustedValue(t *testing.T) {
	secret := "token=must-not-leak\x1b[31m"
	result := EvaluateProvenance(ProvenanceInput{
		ImmutableIdentity: "sha256:abc", OCIRevision: secret, RevisionInvalid: true, ObservedAt: time.Now(),
	})
	encoded := result.Conclusion
	for _, evidence := range result.Evidence {
		encoded += evidence.Subject + evidence.Claim
	}
	if strings.Contains(encoded, secret) || strings.Contains(encoded, "must-not-leak") || strings.ContainsRune(encoded, '\x1b') {
		t.Fatalf("invalid revision leaked into evidence: %q", encoded)
	}
	if result.Level != LevelUnknown {
		t.Fatalf("invalid revision level = %s, want UNKNOWN", result.Level)
	}
}

func TestImpossibleTemporalMetadataPreventsExact(t *testing.T) {
	imageCreated := time.Date(2026, 8, 19, 11, 0, 0, 0, time.UTC)
	commitTime := imageCreated.Add(time.Hour)
	sha := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	result := EvaluateProvenance(ProvenanceInput{
		ImmutableIdentity: "sha256:abc", OCIRevision: sha, ResolvedCommitSHA: sha,
		ImageCreatedAt: &imageCreated, CommitTime: &commitTime, ObservedAt: commitTime,
	})
	if result.Level != LevelUnknown {
		t.Fatalf("temporal contradiction level = %s, want UNKNOWN", result.Level)
	}
	found := false
	for _, evidence := range result.Evidence {
		found = found || (evidence.Kind == EvidenceTemporal && evidence.Polarity == PolarityContradicts)
	}
	if !found {
		t.Fatalf("temporal contradiction absent: %+v", result.Evidence)
	}
}

func TestResolvedCommitMismatchIsContradictory(t *testing.T) {
	sha := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	result := EvaluateProvenance(ProvenanceInput{
		ImmutableIdentity: "sha256:abc", OCIRevision: sha, RevisionMismatch: true, ObservedAt: time.Now(),
	})
	if result.Level != LevelUnknown || len(result.Warnings) != 1 {
		t.Fatalf("revision mismatch result = %+v", result)
	}
	found := false
	for _, evidence := range result.Evidence {
		found = found || (evidence.Kind == EvidenceContradiction && evidence.Polarity == PolarityContradicts)
	}
	if !found {
		t.Fatalf("revision mismatch contradiction absent: %+v", result.Evidence)
	}
}

func TestStatisticalOrTemporalInputCannotProduceExact(t *testing.T) {
	result := EvaluateProvenance(ProvenanceInput{MutableAlias: "api:2026-08-19", ObservedAt: time.Now()})
	if result.Level == LevelExact || result.Score > 0.35 {
		t.Fatalf("mutable alias produced strong confidence: %+v", result)
	}
}
