// Package regression owns bounded deployment-centered comparison evaluation.
package regression

import (
	"context"
	"time"

	"github.com/guijoazeiro/prodmap/internal/baseline"
)

const (
	AlgorithmVersion            = "deployment-comparison/v1-experimental"
	ObservationAlgorithmVersion = "observation-exact-window/v1-experimental"
	MaxContaminatingDeployments = 100
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
	Level, Basis, AlgorithmVersion string
	Limitations                    []string
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
	Classification                          *string
	CausalityClaimed                        bool
}

// Reader is owned by the analytical consumer and implemented by local SQLite.
type Reader interface {
	Comparison(context.Context, Query) (Input, error)
}
