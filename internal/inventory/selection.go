package inventory

import (
	"fmt"
	"sort"
	"strings"

	"github.com/guijoazeiro/prodmap/internal/errs"
)

type SelectorCandidate struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	DisplayLabel string `json:"display_label"`
}

type AmbiguousSelectorError struct {
	Selector   string
	Candidates []SelectorCandidate
}

func NewAmbiguousSelectorError(selector string, candidates []SelectorCandidate) *AmbiguousSelectorError {
	safe := append([]SelectorCandidate(nil), candidates...)
	sort.Slice(safe, func(i, j int) bool {
		if safe[i].Type != safe[j].Type {
			return safe[i].Type < safe[j].Type
		}
		return safe[i].ID < safe[j].ID
	})
	return &AmbiguousSelectorError{Selector: selector, Candidates: safe}
}

func (e *AmbiguousSelectorError) Error() string {
	if e == nil {
		return errs.ErrConflict.Error()
	}
	labels := make([]string, 0, len(e.Candidates))
	for _, candidate := range e.Candidates {
		labels = append(labels, fmt.Sprintf("%s %s", candidate.Type, candidate.ID))
	}
	return fmt.Sprintf("selector is ambiguous; candidates: %s: %v", strings.Join(labels, ", "), errs.ErrConflict)
}

func (e *AmbiguousSelectorError) Unwrap() error { return errs.ErrConflict }
