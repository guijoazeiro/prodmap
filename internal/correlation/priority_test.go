package correlation

import "testing"

func TestDerivationPriorityIsSafetyBiasedAndIgnoresTransientDowngrades(t *testing.T) {
	tests := []struct {
		name   string
		result Result
		want   int
	}{
		{name: "contradiction", result: Result{Evidence: []Evidence{{Polarity: PolarityContradicts}}}, want: 400},
		{name: "exact", result: Result{Level: LevelExact}, want: 300},
		{name: "low", result: Result{Level: LevelLow}, want: 200},
		{name: "unknown fact", result: Result{Level: LevelUnknown}, want: 100},
		{name: "source unavailable", result: Result{Level: LevelUnknown, ResolutionAttempts: []ResolutionAttempt{{State: RevisionSourceUnavailable}}}, want: 0},
		{name: "query not executed", result: Result{Level: LevelUnknown, ResolutionAttempts: []ResolutionAttempt{{State: RevisionQueryNotRun}}}, want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := DerivationPriority(test.result); got != test.want {
				t.Fatalf("DerivationPriority() = %d, want %d", got, test.want)
			}
		})
	}
}
