package cli

import (
	"fmt"
	"io"
	"runtime"
	"time"
)

// BuildInfo describes the executable and its public schema contract.
type BuildInfo struct {
	Version       string `json:"version"`
	Commit        string `json:"commit"`
	BuildDate     string `json:"build_date"`
	GoVersion     string `json:"go_version"`
	SchemaVersion string `json:"schema_version"`
}

// DefaultBuildInfo returns reproducible development defaults.
func DefaultBuildInfo() BuildInfo {
	return BuildInfo{
		Version:       "dev",
		Commit:        "unknown",
		BuildDate:     "unknown",
		GoVersion:     runtime.Version(),
		SchemaVersion: SchemaVersion,
	}
}

// WriteVersion writes either a human-readable result or one JSON envelope.
func WriteVersion(w io.Writer, info BuildInfo, jsonOutput bool, generatedAt time.Time) error {
	if jsonOutput {
		return WriteSuccess(w, "version", generatedAt, info, nil, nil)
	}
	_, err := fmt.Fprintf(w, "prodmap %s\ncommit: %s\nbuilt: %s\ngo: %s\nschema: %s\n",
		info.Version, info.Commit, info.BuildDate, info.GoVersion, info.SchemaVersion)
	return err
}
