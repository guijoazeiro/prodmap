package inventory

import (
	"errors"
	"strings"
	"testing"

	"github.com/guijoazeiro/prodmap/internal/errs"
)

func TestAmbiguousSelectorErrorPreservesConflictAndSortsCandidates(t *testing.T) {
	err := NewAmbiguousSelectorError("ignored", []SelectorCandidate{
		{ID: "b", Type: "runtime", DisplayLabel: "runtime b"},
		{ID: "a", Type: "correlation", DisplayLabel: "correlation a"},
	})
	if !errors.Is(err, errs.ErrConflict) {
		t.Fatal("ambiguous selector must preserve ErrConflict")
	}
	if err.Candidates[0].ID != "a" || err.Candidates[1].ID != "b" {
		t.Fatalf("candidates are not deterministic: %#v", err.Candidates)
	}
	if strings.Contains(err.Error(), "ignored") {
		t.Fatalf("error must not echo an untrusted selector: %q", err.Error())
	}
}
