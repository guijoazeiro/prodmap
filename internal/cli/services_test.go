package cli

import "testing"

func TestNormalizeUnknownPreservesExplicitNoHealthcheck(t *testing.T) {
	if got := normalizeUnknown("none"); got != "none" {
		t.Fatalf("normalizeUnknown(none) = %q, want none", got)
	}
	for _, value := range []string{"", "unknown"} {
		if got := normalizeUnknown(value); got != "UNKNOWN" {
			t.Fatalf("normalizeUnknown(%q) = %q, want UNKNOWN", value, got)
		}
	}
}
