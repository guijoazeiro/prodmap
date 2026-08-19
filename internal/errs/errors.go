// Package errs defines stable, inspectable error categories used by Prodmap.
package errs

import "errors"

var (
	ErrInvalid      = errors.New("invalid input")
	ErrNotFound     = errors.New("not found")
	ErrUnavailable  = errors.New("dependency unavailable")
	ErrInsufficient = errors.New("insufficient data")
	ErrConflict     = errors.New("conflict")
	ErrUnauthorized = errors.New("unauthorized")
	ErrIncompatible = errors.New("incompatible version")
)

// ConfigError identifies a configuration failure while preserving its category.
type ConfigError struct {
	Err error
}

func (e *ConfigError) Error() string {
	if e == nil || e.Err == nil {
		return "invalid configuration"
	}
	return "invalid configuration: " + e.Err.Error()
}

func (e *ConfigError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
