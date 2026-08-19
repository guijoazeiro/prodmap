package cli

import (
	"bytes"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDefaultBuildInfo(t *testing.T) {
	t.Parallel()
	got := DefaultBuildInfo()
	if got.Version != "dev" || got.Commit != "unknown" || got.BuildDate != "unknown" || got.GoVersion != runtime.Version() || got.SchemaVersion != SchemaVersion {
		t.Fatalf("DefaultBuildInfo() = %#v", got)
	}
}

func TestWriteVersionHuman(t *testing.T) {
	t.Parallel()
	info := BuildInfo{Version: "1.2.3", Commit: "abc123", BuildDate: "2026-08-19", GoVersion: "go1.26.5", SchemaVersion: SchemaVersion}
	var output bytes.Buffer
	if err := WriteVersion(&output, info, false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	want := "prodmap 1.2.3\ncommit: abc123\nbuilt: 2026-08-19\ngo: go1.26.5\nschema: 1.0\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestWriteVersionJSON(t *testing.T) {
	t.Parallel()
	info := DefaultBuildInfo()
	var output bytes.Buffer
	if err := WriteVersion(&output, info, true, time.Date(2026, 8, 19, 15, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if strings.Count(strings.TrimSpace(output.String()), "\n") != 0 {
		t.Fatalf("expected one JSON document, got %q", output.String())
	}
	var got struct {
		SchemaVersion string    `json:"schema_version"`
		Command       string    `json:"command"`
		Data          BuildInfo `json:"data"`
	}
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != SchemaVersion || got.Command != "version" || got.Data != info {
		t.Fatalf("unexpected JSON version: %#v", got)
	}
}
