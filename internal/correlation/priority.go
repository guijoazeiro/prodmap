package correlation

// DerivationPriority defines the deterministic Phase 1 choice of the current
// conclusion for a runtime and algorithm. A factual contradiction must replace
// a previously favorable derivation, while transient source unavailability
// must never downgrade a conclusion based on successfully resolved facts.
func DerivationPriority(result Result) int {
	for _, evidence := range result.Evidence {
		if evidence.Polarity == PolarityContradicts {
			return 400
		}
	}
	if result.Level == LevelExact {
		return 300
	}
	if result.Level == LevelLow || result.Level == LevelMedium || result.Level == LevelHigh {
		return 200
	}
	for _, attempt := range result.ResolutionAttempts {
		if attempt.State == RevisionSourceUnavailable || attempt.State == RevisionQueryNotRun {
			return 0
		}
	}
	return 100
}
