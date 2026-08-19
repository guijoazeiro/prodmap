package cli

import (
	"encoding/json"
	"io"
	"time"
)

const SchemaVersion = "1.0"

// ErrorPayload is the stable public representation of a command failure.
type ErrorPayload struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details"`
}

type successEnvelope struct {
	SchemaVersion string   `json:"schema_version"`
	GeneratedAt   string   `json:"generated_at"`
	Command       string   `json:"command"`
	Data          any      `json:"data"`
	Warnings      []string `json:"warnings"`
	Pagination    any      `json:"pagination"`
}

type errorEnvelope struct {
	SchemaVersion string       `json:"schema_version"`
	GeneratedAt   string       `json:"generated_at"`
	Command       string       `json:"command"`
	Error         ErrorPayload `json:"error"`
}

// WriteSuccess writes exactly one JSON success envelope to w.
func WriteSuccess(w io.Writer, command string, generatedAt time.Time, data any, warnings []string, pagination any) error {
	if warnings == nil {
		warnings = []string{}
	}
	return json.NewEncoder(w).Encode(successEnvelope{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   formatGeneratedAt(generatedAt),
		Command:       command,
		Data:          data,
		Warnings:      warnings,
		Pagination:    pagination,
	})
}

// WriteError writes exactly one JSON error envelope to w.
func WriteError(w io.Writer, command string, generatedAt time.Time, payload ErrorPayload) error {
	if payload.Details == nil {
		payload.Details = map[string]any{}
	}
	return json.NewEncoder(w).Encode(errorEnvelope{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   formatGeneratedAt(generatedAt),
		Command:       command,
		Error:         payload,
	})
}

func formatGeneratedAt(generatedAt time.Time) string {
	return generatedAt.UTC().Format(time.RFC3339Nano)
}
