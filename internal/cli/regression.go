package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/guijoazeiro/prodmap/internal/baseline"
	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/regression"
)

func writeRegressionUsage(writer interface{ Write([]byte) (int, error) }) {
	fmt.Fprintln(writer, "usage: prodmap regression --deployment <UUID> --metric <request_count|error_rate|latency_p50|latency_p95|latency_p99> [--before <5m..24h>] [--after <5m..24h>] [--min-samples <1..1000000>] [--min-coverage <0..1>] [--project-dir <path>] [--data-dir <path>] [--json]")
}

func (a *App) runRegression(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("regression", flag.ContinueOnError)
	flags.Usage = func() { writeRegressionUsage(flags.Output()) }
	common := addInventoryFlags(flags)
	deploymentID := flags.String("deployment", "", "internal deployment UUID")
	metric := flags.String("metric", "", "comparison metric")
	before := flags.Duration("before", baseline.DefaultWindow, "exact baseline duration")
	after := flags.Duration("after", baseline.DefaultWindow, "exact observation duration")
	minSamples := flags.Int64("min-samples", baseline.DefaultMinSamples, "minimum samples")
	minCoverage := flags.Float64("min-coverage", baseline.DefaultMinCoverage, "minimum known coverage")
	help, err := a.parseCommandFlags(flags, args)
	if help {
		return nil
	}
	if err != nil {
		return fmt.Errorf("parse regression flags: %w: %v", errs.ErrInvalid, err)
	}
	query := regression.Query{DeploymentID: strings.TrimSpace(*deploymentID), Metric: baseline.Metric(*metric), Before: *before, After: *after, MinSamples: *minSamples, MinCoverage: *minCoverage}
	if flags.NArg() != 0 || regression.ValidateQuery(query) != nil {
		return fmt.Errorf("invalid regression flags: %w", errs.ErrInvalid)
	}
	_, store, err := a.openInventory(ctx, common)
	if err != nil {
		return err
	}
	defer store.Close()
	input, err := store.Comparison(ctx, query)
	if err != nil {
		return fmt.Errorf("query deployment comparison: %w", err)
	}
	generatedAt := a.Now().UTC()
	input.GeneratedAt = generatedAt
	result, err := regression.Evaluate(ctx, input)
	if err != nil {
		return err
	}
	data := regressionOutputFrom(result)
	if *common.jsonOutput {
		return WriteSuccess(a.Stdout, "regression", generatedAt, data, []string{}, nil)
	}
	fmt.Fprintf(a.Stdout, "Regression comparison: %s %s\nDeployment: %s\nBaseline confidence: %s\nObservation confidence: %s\nComparison confidence: %s\n", data.Status, data.Metric, data.Deployment.ID, data.BaselineConfidence.Level, data.ObservationConfidence.Level, data.RegressionConfidence.Level)
	if data.AbsoluteDelta != nil {
		fmt.Fprintf(a.Stdout, "Absolute delta: %v %s\n", *data.AbsoluteDelta, data.Unit)
	}
	for _, limitation := range data.RegressionConfidence.Limitations {
		fmt.Fprintf(a.Stderr, "warning: %s\n", limitation)
	}
	return nil
}

type regressionOutput struct {
	ComparisonKey         string                        `json:"comparison_key"`
	Status                string                        `json:"status"`
	AlgorithmVersion      string                        `json:"algorithm_version"`
	Deployment            regressionDeploymentOutput    `json:"deployment"`
	Metric                baseline.Metric               `json:"metric"`
	Unit                  string                        `json:"unit"`
	Before                regressionSideOutput          `json:"before"`
	After                 regressionSideOutput          `json:"after"`
	AbsoluteDelta         *float64                      `json:"absolute_delta"`
	RelativeDelta         *float64                      `json:"relative_delta"`
	Contamination         regressionContaminationOutput `json:"contamination"`
	BaselineConfidence    regressionConfidenceOutput    `json:"baseline_confidence"`
	ObservationConfidence regressionConfidenceOutput    `json:"observation_confidence"`
	RegressionConfidence  regressionConfidenceOutput    `json:"regression_confidence"`
	Classification        *string                       `json:"classification"`
	CausalityClaimed      bool                          `json:"causality_claimed"`
}

type regressionDeploymentOutput struct {
	ID          string `json:"id"`
	ExternalID  string `json:"external_id"`
	Environment string `json:"environment"`
	Service     string `json:"service"`
	StartedAt   string `json:"started_at"`
}

type regressionSideOutput struct {
	Status           string                    `json:"status"`
	AlgorithmVersion string                    `json:"algorithm_version"`
	Window           regressionWindowOutput    `json:"window"`
	Value            *float64                  `json:"value"`
	SampleCount      int64                     `json:"sample_count"`
	CoverageRatio    *float64                  `json:"coverage_ratio"`
	IsComplete       bool                      `json:"is_complete"`
	AcceptedWindows  []baseline.WindowSummary  `json:"accepted_windows"`
	RejectedWindows  []baseline.RejectedWindow `json:"rejected_windows"`
}

type regressionWindowOutput struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type regressionContaminationOutput struct {
	BeforeDeployments     []string `json:"before_deployments"`
	AfterDeployments      []string `json:"after_deployments"`
	ConcurrentDeployments []string `json:"concurrent_deployments"`
	Truncated             bool     `json:"truncated"`
}

type regressionConfidenceOutput struct {
	Level            string   `json:"level"`
	Basis            string   `json:"basis"`
	AlgorithmVersion string   `json:"algorithm_version"`
	Limitations      []string `json:"limitations"`
}

func regressionOutputFrom(result regression.Result) regressionOutput {
	return regressionOutput{ComparisonKey: result.ComparisonKey, Status: result.Status, AlgorithmVersion: result.AlgorithmVersion, Deployment: regressionDeploymentOutput{ID: result.Deployment.ID, ExternalID: result.Deployment.ExternalID, Environment: result.Deployment.Environment, Service: result.Deployment.Service, StartedAt: result.Deployment.StartedAt.UTC().Format(time.RFC3339Nano)}, Metric: result.Metric, Unit: result.Unit, Before: regressionSideOutputFrom(result.Before), After: regressionSideOutputFrom(result.After), AbsoluteDelta: result.AbsoluteDelta, RelativeDelta: result.RelativeDelta, Contamination: regressionContaminationOutput{BeforeDeployments: append([]string{}, result.Contamination.BeforeDeployments...), AfterDeployments: append([]string{}, result.Contamination.AfterDeployments...), ConcurrentDeployments: append([]string{}, result.Contamination.ConcurrentDeployments...), Truncated: result.Contamination.Truncated}, BaselineConfidence: regressionConfidenceOutputFrom(result.BaselineConfidence), ObservationConfidence: regressionConfidenceOutputFrom(result.ObservationConfidence), RegressionConfidence: regressionConfidenceOutputFrom(result.RegressionConfidence), Classification: nil, CausalityClaimed: false}
}

func regressionSideOutputFrom(side regression.Side) regressionSideOutput {
	return regressionSideOutput{Status: side.Status, AlgorithmVersion: side.AlgorithmVersion, Window: regressionWindowOutput{Start: side.WindowStart.UTC().Format(time.RFC3339Nano), End: side.WindowEnd.UTC().Format(time.RFC3339Nano)}, Value: side.Value, SampleCount: side.SampleCount, CoverageRatio: side.CoverageRatio, IsComplete: side.IsComplete, AcceptedWindows: append([]baseline.WindowSummary{}, side.AcceptedWindows...), RejectedWindows: append([]baseline.RejectedWindow{}, side.RejectedWindows...)}
}

func regressionConfidenceOutputFrom(confidence regression.Confidence) regressionConfidenceOutput {
	return regressionConfidenceOutput{Level: confidence.Level, Basis: confidence.Basis, AlgorithmVersion: confidence.AlgorithmVersion, Limitations: append([]string{}, confidence.Limitations...)}
}
