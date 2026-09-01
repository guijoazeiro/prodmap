// Package baseline owns conservative, query-time baseline evaluation.
package baseline

import (
	"context"
	"time"
)

const (
	AlgorithmVersion         = "baseline-previous-window/v1-experimental"
	DefaultWindow            = 30 * time.Minute
	DefaultMinSamples  int64 = 10
	DefaultMinCoverage       = 0.80
	MinWindow                = 5 * time.Minute
	MaxWindow                = 24 * time.Hour
	MaxCandidates            = 100
)

type Metric string

const (
	RequestCount Metric = "request_count"
	ErrorRate    Metric = "error_rate"
	LatencyP50   Metric = "latency_p50"
	LatencyP95   Metric = "latency_p95"
	LatencyP99   Metric = "latency_p99"
)

type TargetKind string

const (
	ServiceTarget  TargetKind = "service"
	EndpointTarget TargetKind = "endpoint"
)

type Query struct {
	Environment string
	ServiceKey  string
	EndpointID  string
	Metric      Metric
	At          time.Time
	Window      time.Duration
	MinSamples  int64
	MinCoverage float64
	GeneratedAt time.Time
}

type Target struct {
	Kind      TargetKind `json:"kind"`
	ID        string     `json:"id"`
	Service   string     `json:"service"`
	Protocol  string     `json:"protocol,omitempty"`
	Operation string     `json:"operation,omitempty"`
}

type Window struct {
	ID            string
	Start         time.Time
	End           time.Time
	ObservedAt    time.Time
	RequestCount  int64
	ErrorCount    int64
	P50NS         int64
	P95NS         int64
	P99NS         int64
	CoverageRatio *float64
	IsComplete    bool
	Contaminated  bool
}

type Input struct {
	Query      Query
	Target     Target
	Candidates []Window
}

type WindowSummary struct {
	ID          string    `json:"id"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	SampleCount int64     `json:"sample_count"`
}

type RejectionReason string

const (
	AmbiguousExactWindow     RejectionReason = "AMBIGUOUS_EXACT_WINDOW"
	ContaminatedByDeployment RejectionReason = "CONTAMINATED_BY_DEPLOYMENT"
	InsufficientSamples      RejectionReason = "INSUFFICIENT_SAMPLES"
	InsufficientCoverage     RejectionReason = "INSUFFICIENT_COVERAGE"
	FutureEvidence           RejectionReason = "FUTURE_EVIDENCE"
	InvalidWindow            RejectionReason = "INVALID_WINDOW"
	MetricUnavailable        RejectionReason = "METRIC_UNAVAILABLE"
)

type RejectedWindow struct {
	ID            string          `json:"id"`
	Start         time.Time       `json:"start"`
	End           time.Time       `json:"end"`
	ObservedAt    time.Time       `json:"observed_at"`
	SampleCount   int64           `json:"sample_count"`
	CoverageRatio *float64        `json:"coverage_ratio"`
	IsComplete    bool            `json:"is_complete"`
	Contaminated  bool            `json:"contaminated"`
	Reason        RejectionReason `json:"reason"`
}

type Confidence struct {
	Level            string   `json:"level"`
	Basis            string   `json:"basis"`
	AlgorithmVersion string   `json:"algorithm_version"`
	Limitations      []string `json:"limitations"`
}

type Result struct {
	BaselineKey          string           `json:"baseline_key"`
	Status               string           `json:"status"`
	Method               string           `json:"method"`
	AlgorithmVersion     string           `json:"algorithm_version"`
	Environment          string           `json:"environment"`
	Target               Target           `json:"target"`
	Metric               Metric           `json:"metric"`
	Unit                 string           `json:"unit"`
	At                   time.Time        `json:"at"`
	WindowStart          time.Time        `json:"window_start"`
	WindowEnd            time.Time        `json:"window_end"`
	WindowDuration       time.Duration    `json:"window_duration"`
	Value                *float64         `json:"value"`
	Dispersion           *float64         `json:"dispersion"`
	SampleCount          int64            `json:"sample_count"`
	ReferenceWindows     int              `json:"reference_windows"`
	CoverageRatio        *float64         `json:"coverage_ratio"`
	IsComplete           bool             `json:"is_complete"`
	AcceptedWindows      []WindowSummary  `json:"accepted_windows"`
	RejectedWindows      []RejectedWindow `json:"rejected_windows"`
	RejectedWindowsCount int              `json:"rejected_windows_count"`
	Confidence           Confidence       `json:"confidence"`
	CausalityClaimed     bool             `json:"causality_claimed"`
}

// Reader is defined by baseline, its consumer, and implemented by local SQLite.
type Reader interface {
	Baseline(context.Context, Query) (Input, error)
}
