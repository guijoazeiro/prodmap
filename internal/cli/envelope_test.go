package cli

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestWriteSuccess(t *testing.T) {
	t.Parallel()
	generatedAt := time.Date(2026, 8, 19, 12, 34, 56, 123, time.FixedZone("test", -3*60*60))
	var output bytes.Buffer
	if err := WriteSuccess(&output, "doctor", generatedAt, map[string]any{"status": "pass"}, nil, nil); err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got["schema_version"] != SchemaVersion || got["command"] != "doctor" {
		t.Fatalf("unexpected envelope: %#v", got)
	}
	if got["generated_at"] != "2026-08-19T15:34:56.000000123Z" {
		t.Fatalf("generated_at = %q", got["generated_at"])
	}
	if warnings, ok := got["warnings"].([]any); !ok || len(warnings) != 0 {
		t.Fatalf("warnings = %#v, want empty array", got["warnings"])
	}
	if got["pagination"] != nil {
		t.Fatalf("pagination = %#v, want null", got["pagination"])
	}
}

func TestWriteError(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	payload := ErrorPayload{Code: ErrorCodeInsufficientData, Message: "not enough windows", Details: map[string]any{"required": 3}}
	if err := WriteError(&output, "regression", time.Date(2026, 8, 19, 15, 0, 0, 0, time.UTC), payload); err != nil {
		t.Fatal(err)
	}

	var got struct {
		SchemaVersion string       `json:"schema_version"`
		GeneratedAt   string       `json:"generated_at"`
		Command       string       `json:"command"`
		Error         ErrorPayload `json:"error"`
	}
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got.SchemaVersion != SchemaVersion || got.GeneratedAt != "2026-08-19T15:00:00Z" || got.Command != "regression" {
		t.Fatalf("unexpected envelope: %#v", got)
	}
	if got.Error.Code != payload.Code || got.Error.Message != payload.Message || got.Error.Retryable {
		t.Fatalf("unexpected error: %#v", got.Error)
	}
	if got.Error.Details["required"] != float64(3) {
		t.Fatalf("details = %#v", got.Error.Details)
	}
}
