package cli

import (
	"errors"
	"fmt"
	"testing"

	"github.com/guijoazeiro/prodmap/internal/errs"
)

func TestExitCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"success", nil, 0},
		{"invalid", errs.ErrInvalid, 2},
		{"invalid config", &errs.ConfigError{Err: errs.ErrInvalid}, 3},
		{"unavailable", errs.ErrUnavailable, 4},
		{"insufficient", errs.ErrInsufficient, 5},
		{"conflict", errs.ErrConflict, 6},
		{"unauthorized", errs.ErrUnauthorized, 7},
		{"incompatible", errs.ErrIncompatible, 8},
		{"not found uses fallback", errs.ErrNotFound, 1},
		{"unknown", errors.New("boom"), 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wrapped := test.err
			if wrapped != nil {
				wrapped = fmt.Errorf("operation failed: %w", wrapped)
			}
			if got := ExitCode(wrapped); got != test.want {
				t.Fatalf("ExitCode() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestPublicErrorCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		err  error
		want string
	}{
		{errs.ErrInvalid, ErrorCodeInvalidArgument},
		{&errs.ConfigError{Err: errs.ErrInvalid}, ErrorCodeInvalidConfig},
		{errs.ErrNotFound, ErrorCodeNotFound},
		{errs.ErrUnavailable, ErrorCodeSourceUnavailable},
		{errs.ErrInsufficient, ErrorCodeInsufficientData},
		{errs.ErrConflict, ErrorCodeConflict},
		{errs.ErrUnauthorized, ErrorCodeAccessDenied},
		{errs.ErrIncompatible, ErrorCodeIncompatibleSchema},
		{errors.New("boom"), ErrorCodeInternal},
	}
	for _, test := range tests {
		wrapped := fmt.Errorf("context: %w", test.err)
		if got := PublicErrorCode(wrapped); got != test.want {
			t.Errorf("PublicErrorCode(%v) = %q, want %q", test.err, got, test.want)
		}
	}
}
