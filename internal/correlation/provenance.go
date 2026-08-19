package correlation

import "fmt"

// EvaluateProvenance applies the Phase 1 qualitative provenance invariants.
func EvaluateProvenance(input ProvenanceInput) Result {
	result := Result{
		RelationType: RelationInferred,
		Level:        LevelUnknown,
		Algorithm:    AlgorithmVersion,
		Missing:      []string{},
		Warnings:     []string{},
		Evidence:     []Evidence{},
	}

	if input.ImmutableIdentity != "" {
		result.Evidence = append(result.Evidence, Evidence{
			Kind: EvidenceIdentity, Polarity: PolaritySupports, Strength: 1, Source: "docker", ObservedAt: input.ObservedAt,
			Subject: input.ImmutableIdentity,
			Claim:   fmt.Sprintf("The running container uses immutable image identity %s.", input.ImmutableIdentity),
		})
	} else if input.MutableAlias != "" {
		result.Evidence = append(result.Evidence, Evidence{
			Kind: EvidenceIdentity, Polarity: PolaritySupports, Strength: 0.35, Source: "docker", ObservedAt: input.ObservedAt,
			Subject: input.MutableAlias,
			Claim:   fmt.Sprintf("The running container uses mutable image alias %s.", input.MutableAlias),
		})
	}

	if input.IdentityConflict {
		result.Evidence = append(result.Evidence, Evidence{
			Kind: EvidenceContradiction, Polarity: PolarityContradicts, Strength: 1, Source: "docker", ObservedAt: input.ObservedAt,
			Subject: input.ImmutableIdentity,
			Claim:   "The image exposes incompatible immutable identities.",
		})
		result.Warnings = append(result.Warnings, "conflicting immutable image identities")
		result.Conclusion = "Runtime provenance is unknown because immutable image identities conflict."
		return result
	}

	if input.OCIRevision == "" {
		result.Missing = append(result.Missing, "OCI revision metadata")
		result.Evidence = append(result.Evidence, Evidence{
			Kind: EvidenceDataQuality, Polarity: PolarityNeutral, Strength: 0, Source: "docker", ObservedAt: input.ObservedAt,
			Subject: firstNonEmpty(input.ImmutableIdentity, input.MutableAlias),
			Claim:   "The image does not declare org.opencontainers.image.revision.",
		})
		if input.ImmutableIdentity == "" && input.MutableAlias != "" {
			result.Level = LevelLow
			result.Score = 0.35
			result.Conclusion = "Only a mutable image alias is available; commit provenance is not established."
		} else {
			result.Conclusion = "Commit provenance is unknown because OCI revision metadata is absent."
		}
		return result
	}

	if input.RevisionInvalid {
		result.Evidence = append(result.Evidence, Evidence{
			Kind: EvidenceDataQuality, Polarity: PolarityNeutral, Strength: 0, Source: "docker", ObservedAt: input.ObservedAt,
			Subject: "invalid OCI revision",
			Claim:   "The image declares an OCI revision value that is not a complete Git SHA.",
		})
		result.Evidence = append(result.Evidence, Evidence{
			Kind: EvidenceContradiction, Polarity: PolarityContradicts, Strength: 1, Source: "git", ObservedAt: input.ObservedAt,
			Subject: "invalid OCI revision",
			Claim:   "The declared OCI revision cannot be resolved because it is not a valid complete Git SHA.",
		})
		result.Warnings = append(result.Warnings, "invalid OCI revision")
		result.Conclusion = "Commit provenance is unknown because the OCI revision is invalid."
		return result
	}
	result.Evidence = append(result.Evidence, Evidence{
		Kind: EvidenceIdentity, Polarity: PolaritySupports, Strength: 0.7, Source: "docker", ObservedAt: input.ObservedAt,
		Subject: input.OCIRevision,
		Claim:   fmt.Sprintf("The image declares OCI revision %s.", input.OCIRevision),
	})
	if input.RevisionMismatch {
		result.Evidence = append(result.Evidence, Evidence{
			Kind: EvidenceContradiction, Polarity: PolarityContradicts, Strength: 1, Source: "git", ObservedAt: input.ObservedAt,
			Subject: input.OCIRevision,
			Claim:   "The locally resolved commit identity does not equal the complete OCI revision.",
		})
		result.Warnings = append(result.Warnings, "resolved commit differs from OCI revision")
		result.Conclusion = "Commit provenance is unknown because the resolved commit identity contradicts the declared revision."
		return result
	}
	if input.ImageCreatedInvalid {
		result.Evidence = append(result.Evidence, Evidence{
			Kind: EvidenceDataQuality, Polarity: PolarityNeutral, Strength: 0, Source: "docker", ObservedAt: input.ObservedAt,
			Subject: "invalid OCI created timestamp",
			Claim:   "The image declares an OCI creation timestamp that is not valid RFC3339.",
		})
	}
	if input.RevisionNotFound || input.ResolvedCommitSHA == "" {
		result.Missing = append(result.Missing, "locally resolvable commit")
		result.Evidence = append(result.Evidence, Evidence{
			Kind: EvidenceDataQuality, Polarity: PolarityNeutral, Strength: 0, Source: "git", ObservedAt: input.ObservedAt,
			Subject: input.OCIRevision,
			Claim:   fmt.Sprintf("OCI revision %s does not resolve in the local repository.", input.OCIRevision),
		})
		result.Warnings = append(result.Warnings, "OCI revision not found in local repository")
		result.Conclusion = "Commit provenance is unknown because the declared revision is unavailable locally."
		return result
	}

	result.Evidence = append(result.Evidence, Evidence{
		Kind: EvidenceIdentity, Polarity: PolaritySupports, Strength: 1, Source: "git", ObservedAt: input.ObservedAt,
		Subject: input.ResolvedCommitSHA,
		Claim:   fmt.Sprintf("OCI revision %s resolves unambiguously to commit %s.", input.OCIRevision, input.ResolvedCommitSHA),
	})
	if input.ImageCreatedAt != nil && input.CommitTime != nil {
		if input.CommitTime.After(*input.ImageCreatedAt) {
			result.Evidence = append(result.Evidence, Evidence{
				Kind: EvidenceTemporal, Polarity: PolarityContradicts, Strength: 1, Source: "docker+git", ObservedAt: input.ObservedAt,
				Subject: input.ResolvedCommitSHA,
				Claim:   "The declared image creation time predates the resolved commit time.",
			})
			result.Warnings = append(result.Warnings, "image creation time predates resolved commit")
			result.Conclusion = "Commit provenance is unknown because image and commit timestamps contradict the declared chain."
			return result
		}
		result.Evidence = append(result.Evidence, Evidence{
			Kind: EvidenceTemporal, Polarity: PolaritySupports, Strength: 0.5, Source: "docker+git", ObservedAt: input.ObservedAt,
			Subject: input.ResolvedCommitSHA,
			Claim:   "The resolved commit time does not follow the declared image creation time.",
		})
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return "runtime-provenance"
}
