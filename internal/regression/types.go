// Package regression owns bounded deployment-centered comparison evaluation.
package regression

import (
	"context"
	"time"

	"github.com/guijoazeiro/prodmap/internal/baseline"
)

const (
	AlgorithmVersion               = "deployment-comparison/v1-experimental"
	ObservationAlgorithmVersion    = "observation-exact-window/v1-experimental"
	MaxContaminatingDeployments    = 100
	ClassificationAlgorithmVersion = "regression-threshold/v1-experimental"
)

type Query struct {
	DeploymentID string
	Metric       baseline.Metric
	Before       time.Duration
	After        time.Duration
	MinSamples   int64
	MinCoverage  float64
}

type Deployment struct {
	ID, ExternalID, Environment, Service string
	StartedAt                            time.Time
}

type Contamination struct {
	BeforeDeployments     []string
	AfterDeployments      []string
	ConcurrentDeployments []string
	Truncated             bool
}

type Input struct {
	Query         Query
	GeneratedAt   time.Time
	Deployment    Deployment
	Target        baseline.Target
	Before        []baseline.Window
	After         []baseline.Window
	Contamination Contamination
}

type Confidence struct {
	Level            string   `json:"level"`
	Basis            string   `json:"basis"`
	AlgorithmVersion string   `json:"algorithm_version"`
	Limitations      []string `json:"limitations"`
}

type Side struct {
	Status, AlgorithmVersion string
	WindowStart, WindowEnd   time.Time
	Value                    *float64
	SampleCount              int64
	CoverageRatio            *float64
	IsComplete               bool
	AcceptedWindows          []baseline.WindowSummary
	RejectedWindows          []baseline.RejectedWindow
}

type Result struct {
	ComparisonKey, Status, AlgorithmVersion string
	Deployment                              Deployment
	Metric                                  baseline.Metric
	Unit                                    string
	Before, After                           Side
	AbsoluteDelta, RelativeDelta            *float64
	Contamination                           Contamination
	BaselineConfidence                      Confidence
	ObservationConfidence                   Confidence
	RegressionConfidence                    Confidence
	Classification                          *Classification
	CausalityClaimed                        bool
}

type Classification struct {
	ClassificationKey string     `json:"classification_key"`
	Result            string     `json:"result"`
	Direction         string     `json:"direction"`
	AlgorithmVersion  string     `json:"algorithm_version"`
	Thresholds        Thresholds `json:"thresholds"`
	ObservedEffect    Effect     `json:"observed_effect"`
	Confidence        Confidence `json:"confidence"`
	CausalityClaimed  bool       `json:"causality_claimed"`
}

type Thresholds struct {
	AbsoluteMin *float64 `json:"absolute_min"`
	RelativeMin *float64 `json:"relative_min"`
	RequireAll  bool     `json:"require_all"`
	Unit        string   `json:"unit"`
}
type Effect struct {
	AbsoluteDelta *float64 `json:"absolute_delta"`
	RelativeDelta *float64 `json:"relative_delta"`
}

// Reader is owned by the analytical consumer and implemented by local SQLite.
type Reader interface {
	Comparison(context.Context, Query) (Input, error)
}
