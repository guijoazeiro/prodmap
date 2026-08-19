package correlation

import (
	"fmt"
	"time"
)

// EvaluateProvenance applies the Phase 1 qualitative provenance invariants.
func EvaluateProvenance(input ProvenanceInput) Result {
	result := Result{
		RelationType:    RelationInferred,
		Level:           LevelUnknown,
		Algorithm:       AlgorithmVersion,
		Missing:         []string{},
		Warnings:        []string{},
		Evidence:        []Evidence{},
		ScoreComponents: []ScoreComponent{},
		HardCaps:        []HardCap{},
		SourceFreshness: []SourceFreshness{{Source: "docker", ObservedAt: input.ObservedAt.UTC()}},
	}

	revisionState := normalizedRevisionState(input)
	addResolutionContext(&result, revisionState, input)

	if input.ImmutableIdentity != "" {
		result.Evidence = append(result.Evidence, Evidence{
			Kind: EvidenceIdentity, Polarity: PolaritySupports, Strength: 1, Source: "docker", ObservedAt: input.ObservedAt,
			Subject: input.ImmutableIdentity,
			Claim:   fmt.Sprintf("The running container uses immutable image identity %s.", input.ImmutableIdentity),
		})
		result.ScoreComponents = append(result.ScoreComponents, ScoreComponent{
			Name: "immutable_artifact_identity", Value: 1, Description: "A validated immutable Docker artifact identity is available.",
		})
	} else if input.MutableAlias != "" {
		result.Evidence = append(result.Evidence, Evidence{
			Kind: EvidenceIdentity, Polarity: PolaritySupports, Strength: 0.35, Source: "docker", ObservedAt: input.ObservedAt,
			Subject: input.MutableAlias,
			Claim:   fmt.Sprintf("The running container uses mutable image alias %s.", input.MutableAlias),
		})
		result.ScoreComponents = append(result.ScoreComponents, ScoreComponent{
			Name: "mutable_artifact_alias", Value: 0.35, Description: "Only a mutable Docker artifact alias is available.",
		})
		addHardCap(&result, "mutable_artifact_identity", LevelLow, "A mutable alias cannot establish exact artifact identity.")
	} else {
		result.Missing = append(result.Missing, "usable artifact identity")
		addHardCap(&result, "missing_artifact_identity", LevelUnknown, "No usable artifact identity is available.")
	}

	for _, issue := range input.IdentityIssues {
		if issue == "" {
			continue
		}
		result.Evidence = append(result.Evidence, Evidence{
			Kind: EvidenceDataQuality, Polarity: PolarityNeutral, Strength: 0, Source: "docker", ObservedAt: input.ObservedAt,
			Subject: firstNonEmpty(input.ImmutableIdentity, input.MutableAlias),
			Claim:   issue + ".",
		})
		result.Warnings = appendUnique(result.Warnings, issue)
	}
	futureCreated := addFutureCreatedEvidence(&result, input)

	if input.IdentityConflict {
		result.Evidence = append(result.Evidence, Evidence{
			Kind: EvidenceContradiction, Polarity: PolarityContradicts, Strength: 1, Source: "docker", ObservedAt: input.ObservedAt,
			Subject: input.ImmutableIdentity,
			Claim:   "The image exposes incompatible immutable identities.",
		})
		result.Warnings = appendUnique(result.Warnings, "conflicting immutable image identities")
		addHardCap(&result, "identity_contradiction", LevelUnknown, "Conflicting immutable identities invalidate the provenance chain.")
		result.Conclusion = "Runtime provenance is unknown because immutable image identities conflict."
		return result
	}

	if revisionState == RevisionInvalid {
		result.Evidence = append(result.Evidence, Evidence{
			Kind: EvidenceDataQuality, Polarity: PolarityNeutral, Strength: 0, Source: "docker", ObservedAt: input.ObservedAt,
			Subject: "invalid OCI revision",
			Claim:   "The image declares an OCI revision value that is not a complete Git SHA; no Git lookup was attempted.",
		})
		result.Warnings = appendUnique(result.Warnings, "invalid OCI revision")
		result.Missing = append(result.Missing, "valid OCI revision metadata", "commit resolution")
		addHardCap(&result, "invalid_oci_revision", LevelUnknown, "Invalid revision metadata cannot be used for Git resolution.")
		result.Conclusion = "Commit provenance is unknown because the OCI revision is invalid."
		return result
	}

	if input.OCIRevision == "" {
		result.Missing = append(result.Missing, "OCI revision metadata")
		result.Evidence = append(result.Evidence, Evidence{
			Kind: EvidenceDataQuality, Polarity: PolarityNeutral, Strength: 0, Source: "docker", ObservedAt: input.ObservedAt,
			Subject: firstNonEmpty(input.ImmutableIdentity, input.MutableAlias),
			Claim:   "The image does not declare org.opencontainers.image.revision.",
		})
		if futureCreated {
			result.Conclusion = "Commit provenance is unknown because the declared image creation time contradicts the runtime observation."
		} else if input.ImmutableIdentity == "" && input.MutableAlias != "" {
			result.Level = LevelLow
			result.Score = 0.35
			result.Conclusion = "Only a mutable image alias is available; commit provenance is not established."
		} else {
			addHardCap(&result, "missing_oci_revision", LevelUnknown, "Without a declared revision, an artifact cannot be linked to a commit.")
			result.Conclusion = "Commit provenance is unknown because OCI revision metadata is absent."
		}
		return result
	}

	result.Evidence = append(result.Evidence, Evidence{
		Kind: EvidenceIdentity, Polarity: PolaritySupports, Strength: 0.7, Source: "docker", ObservedAt: input.ObservedAt,
		Subject: input.OCIRevision,
		Claim:   fmt.Sprintf("The image declares OCI revision %s.", input.OCIRevision),
	})
	result.ScoreComponents = append(result.ScoreComponents, ScoreComponent{
		Name: "declared_oci_revision", Value: 0.7, Description: "The image declares a complete normalized OCI revision.",
	})

	if input.ImageCreatedInvalid {
		result.Evidence = append(result.Evidence, Evidence{
			Kind: EvidenceDataQuality, Polarity: PolarityNeutral, Strength: 0, Source: "docker", ObservedAt: input.ObservedAt,
			Subject: "invalid OCI created timestamp",
			Claim:   "The image declares an OCI creation timestamp that is not valid RFC3339; the raw value was discarded.",
		})
		result.Warnings = appendUnique(result.Warnings, "invalid OCI creation timestamp")
	}

	contradiction := futureCreated
	resolved := false
	switch revisionState {
	case RevisionMismatched:
		contradiction = true
		result.Evidence = append(result.Evidence, Evidence{
			Kind: EvidenceContradiction, Polarity: PolarityContradicts, Strength: 1, Source: "git", ObservedAt: input.ObservedAt,
			Subject: input.OCIRevision,
			Claim:   "The locally resolved commit identity does not equal the complete OCI revision.",
		})
		result.Warnings = appendUnique(result.Warnings, "resolved commit differs from OCI revision")
		addHardCap(&result, "revision_mismatch", LevelUnknown, "A commit identity mismatch contradicts the declared provenance chain.")
	case RevisionResolved:
		if input.ResolvedCommitSHA == "" || input.ResolvedCommitSHA != input.OCIRevision {
			contradiction = true
			result.Evidence = append(result.Evidence, Evidence{
				Kind: EvidenceContradiction, Polarity: PolarityContradicts, Strength: 1, Source: "git", ObservedAt: input.ObservedAt,
				Subject: input.OCIRevision,
				Claim:   "Git resolution was marked successful but did not return the declared complete revision.",
			})
			addHardCap(&result, "invalid_resolution_result", LevelUnknown, "A successful resolution must exactly match the declared revision.")
		} else {
			resolved = true
			result.Evidence = append(result.Evidence, Evidence{
				Kind: EvidenceIdentity, Polarity: PolaritySupports, Strength: 1, Source: "git", ObservedAt: input.ObservedAt,
				Subject: input.ResolvedCommitSHA,
				Claim:   fmt.Sprintf("OCI revision %s resolves unambiguously to commit %s.", input.OCIRevision, input.ResolvedCommitSHA),
			})
			result.ScoreComponents = append(result.ScoreComponents, ScoreComponent{
				Name: "local_commit_resolution", Value: 1, Description: "The complete OCI revision resolves exactly in the local Git repository.",
			})
		}
	case RevisionNotFound:
		result.Missing = append(result.Missing, "locally resolvable commit")
		result.Evidence = append(result.Evidence, Evidence{
			Kind: EvidenceDataQuality, Polarity: PolarityNeutral, Strength: 0, Source: "git", ObservedAt: input.ObservedAt,
			Subject: input.OCIRevision,
			Claim:   "A Git lookup was executed, but the declared OCI revision was not found in the local repository.",
		})
		result.Warnings = appendUnique(result.Warnings, "OCI revision not found in local repository")
		addHardCap(&result, "commit_not_found", LevelUnknown, "The declared revision was not found by the local Git lookup.")
	case RevisionSourceUnavailable:
		result.Missing = append(result.Missing, "available Git source", "commit resolution")
		result.Evidence = append(result.Evidence, Evidence{
			Kind: EvidenceDataQuality, Polarity: PolarityNeutral, Strength: 0, Source: "git", ObservedAt: input.ObservedAt,
			Subject: input.OCIRevision,
			Claim:   "The Git source was unavailable before the declared revision could be looked up.",
		})
		result.Warnings = appendUnique(result.Warnings, "Git source unavailable; OCI revision was not queried")
		addHardCap(&result, "git_source_unavailable", LevelUnknown, "Transient Git source unavailability leaves commit resolution incomplete.")
	case RevisionQueryNotRun, RevisionNotApplicable:
		result.Missing = append(result.Missing, "commit resolution")
		result.Evidence = append(result.Evidence, Evidence{
			Kind: EvidenceDataQuality, Polarity: PolarityNeutral, Strength: 0, Source: "prodmap", ObservedAt: input.ObservedAt,
			Subject: input.OCIRevision,
			Claim:   "The declared OCI revision was not submitted to a Git resolution query.",
		})
		addHardCap(&result, "commit_query_not_executed", LevelUnknown, "The declared revision has not been resolved.")
	}

	if resolved && input.ImageCreatedAt != nil && input.CommitTime != nil {
		if input.CommitTime.After(*input.ImageCreatedAt) {
			contradiction = true
			result.Evidence = append(result.Evidence, Evidence{
				Kind: EvidenceTemporal, Polarity: PolarityContradicts, Strength: 1, Source: "docker+git", ObservedAt: input.ObservedAt,
				Subject: input.ResolvedCommitSHA,
				Claim:   "The declared image creation time predates the resolved commit time.",
				Details: map[string]string{
					"created_at": input.ImageCreatedAt.UTC().Format(time.RFC3339Nano),
					"commit_at":  input.CommitTime.UTC().Format(time.RFC3339Nano),
				},
			})
			result.Warnings = appendUnique(result.Warnings, "image creation time predates resolved commit")
			addHardCap(&result, "commit_after_image", LevelUnknown, "A commit cannot postdate an image that declares it as provenance.")
		} else {
			result.Evidence = append(result.Evidence, Evidence{
				Kind: EvidenceTemporal, Polarity: PolaritySupports, Strength: 0.5, Source: "docker+git", ObservedAt: input.ObservedAt,
				Subject: input.ResolvedCommitSHA,
				Claim:   "The resolved commit time does not follow the declared image creation time.",
			})
			result.ScoreComponents = append(result.ScoreComponents, ScoreComponent{
				Name: "temporal_plausibility", Value: 0.5, Description: "Commit and image creation timestamps are chronologically plausible.",
			})
		}
	}

	if contradiction {
		result.Conclusion = "Commit provenance is unknown because factual evidence contradicts the declared provenance chain."
		return result
	}
	if !resolved {
		switch revisionState {
		case RevisionNotFound:
			result.Conclusion = "Commit provenance is unknown because a Git lookup did not find the declared revision locally."
		case RevisionSourceUnavailable:
			result.Conclusion = "Commit provenance is unknown because the Git source was unavailable and no revision lookup was completed."
		default:
			result.Conclusion = "Commit provenance is unknown because the declared revision has not been resolved."
		}
		return result
	}
	if input.ImmutableIdentity == "" {
		result.Level = LevelLow
		result.Score = 0.35
		result.Conclusion = "The revision resolves, but only a mutable image alias identifies the artifact."
		return result
	}

	result.RelationType = RelationExact
	result.Level = LevelExact
	result.Score = 1
	result.Conclusion = fmt.Sprintf("The running immutable image maps exactly to commit %s.", input.ResolvedCommitSHA)
	return result
}

func normalizedRevisionState(input ProvenanceInput) RevisionResolutionState {
	if input.RevisionState != "" {
		return input.RevisionState
	}
	if input.OCIRevision == "" {
		return RevisionNotApplicable
	}
	if input.ResolvedCommitSHA != "" {
		return RevisionResolved
	}
	return RevisionQueryNotRun
}

func addFutureCreatedEvidence(result *Result, input ProvenanceInput) bool {
	if input.ImageCreatedAt == nil || input.ObservedAt.IsZero() || !input.ImageCreatedAt.After(input.ObservedAt.Add(MaxImageCreatedClockSkew)) {
		return false
	}
	result.Evidence = append(result.Evidence, Evidence{
		Kind: EvidenceTemporal, Polarity: PolarityContradicts, Strength: 1, Source: "docker", ObservedAt: input.ObservedAt,
		Subject: firstNonEmpty(input.ImmutableIdentity, input.MutableAlias),
		Claim:   "The declared image creation time is later than runtime observation plus the allowed clock skew.",
		Details: map[string]string{
			"created_at":     input.ImageCreatedAt.UTC().Format(time.RFC3339Nano),
			"observed_at":    input.ObservedAt.UTC().Format(time.RFC3339Nano),
			"max_clock_skew": MaxImageCreatedClockSkew.String(),
		},
	})
	result.Warnings = appendUnique(result.Warnings, "image creation time is implausibly later than runtime observation")
	addHardCap(result, "future_image_creation", LevelUnknown, "A future image creation timestamp contradicts the observed runtime chronology.")
	return true
}

func addResolutionContext(result *Result, state RevisionResolutionState, input ProvenanceInput) {
	if state == RevisionNotApplicable {
		return
	}
	source := "git"
	if state == RevisionInvalid {
		source = "docker"
	}
	result.ResolutionAttempts = append(result.ResolutionAttempts, ResolutionAttempt{
		State: state, Source: source, Revision: safeRevision(state, input.OCIRevision), ObservedAt: input.ObservedAt.UTC(),
	})
	switch state {
	case RevisionSourceUnavailable, RevisionNotFound, RevisionResolved, RevisionMismatched:
		result.SourceFreshness = append(result.SourceFreshness, SourceFreshness{Source: "git", ObservedAt: input.ObservedAt.UTC()})
	}
}

func safeRevision(state RevisionResolutionState, revision string) string {
	if state == RevisionInvalid {
		return "<invalid>"
	}
	return revision
}

func addHardCap(result *Result, name string, level Level, reason string) {
	result.HardCaps = append(result.HardCaps, HardCap{Name: name, MaxLevel: level, Reason: reason})
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return "runtime-provenance"
}
