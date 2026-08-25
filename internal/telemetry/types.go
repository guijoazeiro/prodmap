// Package telemetry owns sanitized trace-derived ingestion contracts.
package telemetry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/identity"
	"github.com/guijoazeiro/prodmap/internal/topology"
)

const (
	FormatOTLPJSONL             = "otlp-jsonl"
	AggregationVersion          = "otel-window/v1"
	EvidenceFingerprintV1       = "sha256-v1"
	MaxFileBytes          int64 = 64 << 20
	MaxLineBytes                = 1 << 20
	MaxLines                    = 10000
	MaxSpans                    = 100000
	MaxServices                 = 1000
	MaxEndpoints                = 5000
	MaxDependencies             = 5000
	MaxEvidenceSamples          = 4
	MaxLinksPerSpan             = 32
	MaxLinks                    = 100000
)

type Stats struct {
	Lines            int
	ResourceSpans    int
	SpansSeen        int
	SpansAccepted    int
	SpansIgnored     int
	Services         int
	Endpoints        int
	Dependencies     int
	Observations     int
	TelemetryWindows int
}

type Service struct {
	LogicalKey  string
	DisplayName string
}

type Endpoint struct {
	Key           string
	ServiceKey    string
	Protocol      string
	Operation     string
	RouteTemplate string
}

type Dependency struct {
	Key         string
	LogicalKey  string
	Kind        string
	DisplayName string
}

type Evidence struct {
	Fingerprint string
	Claim       string
	ObservedAt  time.Time
}

type DependencyObservation struct {
	Key                  string
	FromServiceKey       string
	OriginEndpointKey    string
	DependencyKey        string
	TargetServiceKey     string
	WindowStart          time.Time
	WindowEnd            time.Time
	RequestCount         int64
	ErrorCount           int64
	DurationSumNS        int64
	Confidence           topology.Confidence
	Basis                string
	EvidenceFingerprints []string
	Limitations          []string
}

type Window struct {
	Key           string
	ServiceKey    string
	EndpointKey   string
	WindowStart   time.Time
	WindowEnd     time.Time
	RequestCount  int64
	ErrorCount    int64
	DurationSumNS int64
	P50NS         int64
	P95NS         int64
	P99NS         int64
	IsComplete    bool
	CoverageRatio *float64
}

type Snapshot struct {
	SourceKey    string
	SourceHash   string
	Environment  string
	WindowStart  time.Time
	WindowEnd    time.Time
	ObservedAt   time.Time
	Stats        Stats
	Services     []Service
	Endpoints    []Endpoint
	Dependencies []Dependency
	Observations []DependencyObservation
	Windows      []Window
	Evidence     []Evidence
	Warnings     []string
}

type IngestResult struct {
	IngestionID      string
	SourceHash       string
	Environment      string
	WindowStart      time.Time
	WindowEnd        time.Time
	Stats            Stats
	IdempotentReplay bool
	Warnings         []string
}

type Store interface {
	SaveTelemetry(context.Context, Snapshot) (IngestResult, error)
}

type Decoder interface {
	Decode(context.Context, []byte, string, string, string, time.Time, time.Time, time.Time) (Snapshot, error)
}

// LoadFrozenFile reads one bounded immutable input before SQLite is opened.
func LoadFrozenFile(ctx context.Context, path, environment string, start, end, observedAt time.Time, decoder Decoder) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%w: open telemetry file", classifyFileError(err))
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Snapshot{}, fmt.Errorf("%w: inspect telemetry file", classifyFileError(err))
	}
	if !info.Mode().IsRegular() {
		return Snapshot{}, fmt.Errorf("%w: telemetry file must be regular", errs.ErrInvalid)
	}
	if info.Size() > MaxFileBytes {
		return Snapshot{}, fmt.Errorf("%w: telemetry file exceeds %d bytes", errs.ErrInvalid, MaxFileBytes)
	}
	content, err := io.ReadAll(io.LimitReader(file, MaxFileBytes+1))
	if err != nil {
		return Snapshot{}, fmt.Errorf("%w: read telemetry file", classifyFileError(err))
	}
	if int64(len(content)) > MaxFileBytes {
		return Snapshot{}, fmt.Errorf("%w: telemetry file exceeds %d bytes", errs.ErrInvalid, MaxFileBytes)
	}
	after, err := file.Stat()
	if err != nil {
		return Snapshot{}, fmt.Errorf("%w: recheck telemetry file", classifyFileError(err))
	}
	if after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return Snapshot{}, fmt.Errorf("%w: telemetry file changed while it was being read", errs.ErrConflict)
	}
	digest := sha256.Sum256(content)
	hash := "sha256:" + hex.EncodeToString(digest[:])
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%w: resolve telemetry file", errs.ErrInvalid)
	}
	sourceDigest := sha256.Sum256([]byte(filepath.Clean(absolute)))
	return decoder.Decode(ctx, content, "file:"+hex.EncodeToString(sourceDigest[:]), hash, environment, start, end, observedAt)
}

func classifyFileError(err error) error {
	if os.IsPermission(err) {
		return errs.ErrUnauthorized
	}
	if os.IsNotExist(err) {
		return errs.ErrNotFound
	}
	return errs.ErrUnavailable
}

func NearestRank(values []int64, percentile float64) int64 {
	if len(values) == 0 {
		return 0
	}
	copyValues := append([]int64(nil), values...)
	sort.Slice(copyValues, func(i, j int) bool { return copyValues[i] < copyValues[j] })
	index := int(math.Ceil(percentile*float64(len(copyValues)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(copyValues) {
		index = len(copyValues) - 1
	}
	return copyValues[index]
}

func ValidEnvironment(value string) (string, error) {
	return identity.ValidEnvironment(value)
}
