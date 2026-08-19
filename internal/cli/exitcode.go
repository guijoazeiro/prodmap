package cli

import (
	"errors"

	"github.com/guijoazeiro/prodmap/internal/errs"
)

const (
	ErrorCodeInvalidArgument    = "INVALID_ARGUMENT"
	ErrorCodeInvalidConfig      = "INVALID_CONFIG"
	ErrorCodeNotFound           = "NOT_FOUND"
	ErrorCodeSourceUnavailable  = "SOURCE_UNAVAILABLE"
	ErrorCodeInsufficientData   = "INSUFFICIENT_DATA"
	ErrorCodeConflict           = "CONFLICT"
	ErrorCodeAccessDenied       = "ACCESS_DENIED"
	ErrorCodeIncompatibleSchema = "INCOMPATIBLE_SCHEMA"
	ErrorCodeInternal           = "INTERNAL"
)

// ExitCode maps inspectable domain error categories to the CLI process contract.
func ExitCode(err error) int {
	var configErr *errs.ConfigError

	switch {
	case err == nil:
		return 0
	case errors.As(err, &configErr):
		return 3
	case errors.Is(err, errs.ErrInvalid):
		return 2
	case errors.Is(err, errs.ErrUnavailable):
		return 4
	case errors.Is(err, errs.ErrInsufficient):
		return 5
	case errors.Is(err, errs.ErrConflict):
		return 6
	case errors.Is(err, errs.ErrUnauthorized):
		return 7
	case errors.Is(err, errs.ErrIncompatible):
		return 8
	default:
		return 1
	}
}

// PublicErrorCode maps an inspectable error to the stable JSON error code.
func PublicErrorCode(err error) string {
	var configErr *errs.ConfigError

	switch {
	case errors.As(err, &configErr):
		return ErrorCodeInvalidConfig
	case errors.Is(err, errs.ErrInvalid):
		return ErrorCodeInvalidArgument
	case errors.Is(err, errs.ErrNotFound):
		return ErrorCodeNotFound
	case errors.Is(err, errs.ErrUnavailable):
		return ErrorCodeSourceUnavailable
	case errors.Is(err, errs.ErrInsufficient):
		return ErrorCodeInsufficientData
	case errors.Is(err, errs.ErrConflict):
		return ErrorCodeConflict
	case errors.Is(err, errs.ErrUnauthorized):
		return ErrorCodeAccessDenied
	case errors.Is(err, errs.ErrIncompatible):
		return ErrorCodeIncompatibleSchema
	default:
		return ErrorCodeInternal
	}
}
