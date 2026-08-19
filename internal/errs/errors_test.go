package errs

import (
	"errors"
	"fmt"
	"testing"
)

func TestCategoriesRemainInspectableWhenWrapped(t *testing.T) {
	categories := []error{
		ErrInvalid,
		ErrNotFound,
		ErrUnavailable,
		ErrInsufficient,
		ErrConflict,
		ErrUnauthorized,
		ErrIncompatible,
	}

	for _, category := range categories {
		wrapped := fmt.Errorf("operation failed: %w", category)
		if !errors.Is(wrapped, category) {
			t.Fatalf("errors.Is(%q, %q) = false", wrapped, category)
		}
	}
}
