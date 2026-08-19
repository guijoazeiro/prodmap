package correlation

import (
	"strings"
	"testing"
	"time"
)

func TestEvaluateProvenanceCoreLevels(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	sha := strings.Repeat("a", 40)
	digest := "sha256:" + strings.Repeat("b", 64)
	tests := []struct {
		name  string
		input ProvenanceInput
		level Level
		kind  EvidenceKind
	}{
		{"exact", ProvenanceInput{ImmutableIdentity: digest, OCIRevision: sha, ResolvedCommitSHA: sha, RevisionState: RevisionResolved, ObservedAt: now}, LevelExact, EvidenceIdentity},
		{"mutable tag", ProvenanceInput{MutableAlias: "api:latest", ObservedAt: now}, LevelLow, EvidenceDataQuality},
		{"missing revision", ProvenanceInput{ImmutableIdentity: digest, ObservedAt: now}, LevelUnknown, EvidenceDataQuality},
		{"revision not found", ProvenanceInput{ImmutableIdentity: digest, OCIRevision: sha, RevisionState: RevisionNotFound, ObservedAt: now}, LevelUnknown, EvidenceDataQuality},
		{"conflict", ProvenanceInput{ImmutableIdentity: digest, IdentityConflict: true, RevisionState: RevisionQueryNotRun, ObservedAt: now}, LevelUnknown, EvidenceContradiction},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := EvaluateProvenance(test.input)
			if result.Level != test.level || result.Algorithm != AlgorithmVersion {
				t.Fatalf("result = %+v", result)
			}
			if !hasEvidenceKind(result, test.kind) {
				t.Fatalf("evidence kind %s absent: %+v", test.kind, result.Evidence)
			}
		})
	}
}

func TestInvalidRevisionDoesNotClaimGitQueryOrEchoValue(t *testing.T) {
	secret := "token=must-not-leak\x1b[31m"
	result := EvaluateProvenance(ProvenanceInput{
		ImmutableIdentity: "sha256:" + strings.Repeat("a", 64),
		OCIRevision:       secret,
		RevisionState:     RevisionInvalid,
		ObservedAt:        time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC),
	})
	encoded := result.Conclusion
	for _, evidence := range result.Evidence {
		encoded += evidence.Subject + evidence.Claim + evidence.Source
		if evidence.Source == "git" {
			t.Fatalf("invalid revision attributed evidence to Git: %+v", evidence)
		}
	}
	for _, attempt := range result.ResolutionAttempts {
		encoded += attempt.Revision
	}
	if strings.Contains(encoded, secret) || strings.Contains(encoded, "must-not-leak") || strings.ContainsRune(encoded, '\x1b') {
		t.Fatalf("invalid revision leaked into evidence: %q", encoded)
	}
	if result.Level != LevelUnknown || len(result.ResolutionAttempts) != 1 || result.ResolutionAttempts[0].State != RevisionInvalid || result.ResolutionAttempts[0].Revision != "<invalid>" {
		t.Fatalf("invalid revision result = %+v", result)
	}
}

func TestResolutionStatesReportWhatActuallyHappened(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	sha := strings.Repeat("a", 40)
	digest := "sha256:" + strings.Repeat("b", 64)
	tests := []struct {
		name          string
		state         RevisionResolutionState
		wantClaim     string
		rejectClaim   string
		wantGitFresh  bool
		wantGitSource bool
	}{
		{name: "query not run", state: RevisionQueryNotRun, wantClaim: "not submitted", rejectClaim: "not found", wantGitFresh: false, wantGitSource: false},
		{name: "source unavailable", state: RevisionSourceUnavailable, wantClaim: "source was unavailable", rejectClaim: "not found", wantGitFresh: true, wantGitSource: true},
		{name: "not found", state: RevisionNotFound, wantClaim: "lookup was executed", rejectClaim: "source was unavailable", wantGitFresh: true, wantGitSource: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := EvaluateProvenance(ProvenanceInput{
				ImmutableIdentity: digest, OCIRevision: sha, RevisionState: test.state, ObservedAt: now,
			})
			claims := ""
			hasGitEvidence := false
			for _, evidence := range result.Evidence {
				claims += evidence.Claim
				hasGitEvidence = hasGitEvidence || evidence.Source == "git"
			}
			if !strings.Contains(claims, test.wantClaim) || strings.Contains(claims, test.rejectClaim) {
				t.Fatalf("claims = %q", claims)
			}
			if hasGitEvidence != test.wantGitSource {
				t.Fatalf("has Git evidence = %t, want %t: %+v", hasGitEvidence, test.wantGitSource, result.Evidence)
			}
			if hasSource(result.SourceFreshness, "git") != test.wantGitFresh {
				t.Fatalf("source freshness = %+v", result.SourceFreshness)
			}
			if len(result.ResolutionAttempts) != 1 || result.ResolutionAttempts[0].State != test.state || !result.ResolutionAttempts[0].ObservedAt.Equal(now) {
				t.Fatalf("resolution attempts = %+v", result.ResolutionAttempts)
			}
		})
	}
}

func TestFutureImageCreationClockSkewBoundary(t *testing.T) {
	observed := time.Date(2026, 8, 19, 12, 0, 0, 123, time.UTC)
	sha := strings.Repeat("a", 40)
	base := ProvenanceInput{
		ImmutableIdentity: "sha256:" + strings.Repeat("b", 64),
		OCIRevision:       sha,
		ResolvedCommitSHA: sha,
		RevisionState:     RevisionResolved,
		ObservedAt:        observed,
	}
	tests := []struct {
		name         string
		created      time.Time
		wantLevel    Level
		wantConflict bool
	}{
		{name: "earlier", created: observed.Add(-time.Hour), wantLevel: LevelExact},
		{name: "equal", created: observed, wantLevel: LevelExact},
		{name: "inside tolerance", created: observed.Add(MaxImageCreatedClockSkew - time.Nanosecond), wantLevel: LevelExact},
		{name: "exactly at limit", created: observed.Add(MaxImageCreatedClockSkew), wantLevel: LevelExact},
		{name: "one instant beyond", created: observed.Add(MaxImageCreatedClockSkew + time.Nanosecond), wantLevel: LevelUnknown, wantConflict: true},
		{name: "far future", created: observed.Add(24 * time.Hour), wantLevel: LevelUnknown, wantConflict: true},
		{name: "offset timezone", created: observed.Add(MaxImageCreatedClockSkew + time.Second).In(time.FixedZone("offset", -3*60*60)), wantLevel: LevelUnknown, wantConflict: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			created := test.created
			input.ImageCreatedAt = &created
			result := EvaluateProvenance(input)
			if result.Level != test.wantLevel {
				t.Fatalf("level = %s, want %s: %+v", result.Level, test.wantLevel, result)
			}
			future := futureContradiction(result)
			if (future != nil) != test.wantConflict {
				t.Fatalf("future contradiction = %+v, want %t", future, test.wantConflict)
			}
			if future != nil {
				if len(future.Details) != 3 || future.Details["created_at"] != created.UTC().Format(time.RFC3339Nano) || future.Details["observed_at"] != observed.Format(time.RFC3339Nano) || future.Details["max_clock_skew"] != MaxImageCreatedClockSkew.String() {
					t.Fatalf("future details = %#v", future.Details)
				}
			}
		})
	}
}

func TestInvalidCreatedTimestampProducesDataQualityEvidence(t *testing.T) {
	sha := strings.Repeat("a", 40)
	result := EvaluateProvenance(ProvenanceInput{
		ImmutableIdentity:   "sha256:" + strings.Repeat("b", 64),
		OCIRevision:         sha,
		ResolvedCommitSHA:   sha,
		RevisionState:       RevisionResolved,
		ImageCreatedInvalid: true,
		ObservedAt:          time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC),
	})
	if result.Level != LevelExact || !strings.Contains(joinClaims(result), "raw value was discarded") {
		t.Fatalf("invalid created result = %+v", result)
	}
}

func TestCommitAfterImageCreationPreventsExact(t *testing.T) {
	imageCreated := time.Date(2026, 8, 19, 11, 0, 0, 0, time.UTC)
	commitTime := imageCreated.Add(time.Hour)
	sha := strings.Repeat("a", 40)
	result := EvaluateProvenance(ProvenanceInput{
		ImmutableIdentity: "sha256:" + strings.Repeat("b", 64), OCIRevision: sha, ResolvedCommitSHA: sha, RevisionState: RevisionResolved,
		ImageCreatedAt: &imageCreated, CommitTime: &commitTime, ObservedAt: commitTime,
	})
	if result.Level != LevelUnknown || !hasContradiction(result) {
		t.Fatalf("temporal contradiction result = %+v", result)
	}
}

func TestResolvedCommitMismatchIsContradictory(t *testing.T) {
	sha := strings.Repeat("a", 40)
	result := EvaluateProvenance(ProvenanceInput{
		ImmutableIdentity: "sha256:" + strings.Repeat("b", 64), OCIRevision: sha, RevisionState: RevisionMismatched, ObservedAt: time.Now(),
	})
	if result.Level != LevelUnknown || !hasContradiction(result) {
		t.Fatalf("revision mismatch result = %+v", result)
	}
}

func TestValidFallbackRetainsDataQualityEvidenceAndFullDetail(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	sha := strings.Repeat("a", 40)
	issue := "valid image ID was used because repository digests were unusable"
	result := EvaluateProvenance(ProvenanceInput{
		ImmutableIdentity: "sha256:" + strings.Repeat("b", 64), IdentityIssues: []string{issue},
		OCIRevision: sha, ResolvedCommitSHA: sha, RevisionState: RevisionResolved, ObservedAt: now,
	})
	if result.Level != LevelExact || !strings.Contains(joinClaims(result), issue) {
		t.Fatalf("fallback result = %+v", result)
	}
	if len(result.ScoreComponents) < 3 || len(result.ResolutionAttempts) != 1 || len(result.SourceFreshness) != 2 {
		t.Fatalf("full detail is incomplete: %+v", result)
	}
}

func TestStatisticalOrTemporalInputCannotProduceExact(t *testing.T) {
	result := EvaluateProvenance(ProvenanceInput{MutableAlias: "api:2026-08-19", ObservedAt: time.Now()})
	if result.Level == LevelExact || result.Score > 0.35 || len(result.HardCaps) == 0 {
		t.Fatalf("mutable alias produced strong confidence: %+v", result)
	}
}

func hasEvidenceKind(result Result, kind EvidenceKind) bool {
	for _, evidence := range result.Evidence {
		if evidence.Kind == kind {
			return true
		}
	}
	return false
}

func hasContradiction(result Result) bool {
	for _, evidence := range result.Evidence {
		if evidence.Polarity == PolarityContradicts {
			return true
		}
	}
	return false
}

func futureContradiction(result Result) *Evidence {
	for index := range result.Evidence {
		evidence := &result.Evidence[index]
		if evidence.Kind == EvidenceTemporal && evidence.Polarity == PolarityContradicts && evidence.Details["max_clock_skew"] != "" {
			return evidence
		}
	}
	return nil
}

func joinClaims(result Result) string {
	var claims strings.Builder
	for _, evidence := range result.Evidence {
		claims.WriteString(evidence.Claim)
	}
	return claims.String()
}

func hasSource(freshness []SourceFreshness, source string) bool {
	for _, item := range freshness {
		if item.Source == source {
			return true
		}
	}
	return false
}
