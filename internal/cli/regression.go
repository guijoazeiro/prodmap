package cli

import (
	"context"
	"flag"
	"fmt"
	"slices"
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
	classification, err := regression.Classify(ctx, result)
	if err != nil {
		return err
	}
	result.Classification = &classification
	data := regressionOutputFrom(result)
	if *common.jsonOutput {
		return WriteSuccess(a.Stdout, "regression", generatedAt, data, []string{}, nil)
	}
	fmt.Fprintf(a.Stdout, "Regression comparison: %s %s\nDeployment: %s\nBaseline confidence: %s\nObservation confidence: %s\nComparison confidence: %s\n", data.Status, data.Metric, data.Deployment.ID, data.BaselineConfidence.Level, data.ObservationConfidence.Level, data.RegressionConfidence.Level)
	fmt.Fprintf(a.Stdout, "Classification: %s\nClassification direction: %s\nClassification confidence: %s\nClassification algorithm: %s %s\n", data.Classification.Result, data.Classification.Direction, data.Classification.Confidence.Level, data.Classification.Algorithm, data.Classification.AlgorithmVersion)
	fmt.Fprintf(a.Stdout, "Classification observed effect: absolute_delta=%s relative_delta=%s %s\n", optionalFloatString(data.Classification.ObservedEffect.AbsoluteDelta), optionalFloatString(data.Classification.ObservedEffect.RelativeDelta), data.Unit)
	fmt.Fprintf(a.Stdout, "Classification thresholds: absolute_min=%s relative_min=%s require_all=%t unit=%s\nClassification key: %s\n", optionalFloatString(data.Classification.Thresholds.AbsoluteMin), optionalFloatString(data.Classification.Thresholds.RelativeMin), data.Classification.Thresholds.RequireAll, data.Classification.Thresholds.Unit, data.Classification.ClassificationKey)
	for _, limitation := range mergeRegressionWarnings(data.RegressionConfidence.Limitations, data.Classification.Confidence.Limitations) {
		fmt.Fprintf(a.Stderr, "warning: %s\n", limitation)
	}
	return nil
}

type regressionOutput struct {
	ComparisonKey         string                          `json:"comparison_key"`
	Status                string                          `json:"status"`
	AlgorithmVersion      string                          `json:"algorithm_version"`
	Deployment            regressionDeploymentOutput      `json:"deployment"`
	Metric                baseline.Metric                 `json:"metric"`
	Unit                  string                          `json:"unit"`
	Before                regressionSideOutput            `json:"before"`
	After                 regressionSideOutput            `json:"after"`
	AbsoluteDelta         *float64                        `json:"absolute_delta"`
	RelativeDelta         *float64                        `json:"relative_delta"`
	Contamination         regressionContaminationOutput   `json:"contamination"`
	BaselineConfidence    regressionConfidenceOutput      `json:"baseline_confidence"`
	ObservationConfidence regressionConfidenceOutput      `json:"observation_confidence"`
	RegressionConfidence  regressionConfidenceOutput      `json:"regression_confidence"`
	Classification        *regressionClassificationOutput `json:"classification"`
	CausalityClaimed      bool                            `json:"causality_claimed"`
}

type regressionClassificationOutput struct {
	ClassificationKey string                         `json:"classification_key"`
	Result            string                         `json:"result"`
	Direction         string                         `json:"direction"`
	Algorithm         string                         `json:"algorithm"`
	AlgorithmVersion  string                         `json:"algorithm_version"`
	Thresholds        regressionThresholdsOutput     `json:"thresholds"`
	ObservedEffect    regressionObservedEffectOutput `json:"observed_effect"`
	Confidence        regressionConfidenceOutput     `json:"confidence"`
	CausalityClaimed  bool                           `json:"causality_claimed"`
}

type regressionThresholdsOutput struct {
	AbsoluteMin *float64 `json:"absolute_min"`
	RelativeMin *float64 `json:"relative_min"`
	RequireAll  bool     `json:"require_all"`
	Unit        string   `json:"unit"`
}

type regressionObservedEffectOutput struct {
	AbsoluteDelta *float64 `json:"absolute_delta"`
	RelativeDelta *float64 `json:"relative_delta"`
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
	return regressionOutput{ComparisonKey: result.ComparisonKey, Status: result.Status, AlgorithmVersion: result.AlgorithmVersion, Deployment: regressionDeploymentOutput{ID: result.Deployment.ID, ExternalID: result.Deployment.ExternalID, Environment: result.Deployment.Environment, Service: result.Deployment.Service, StartedAt: result.Deployment.StartedAt.UTC().Format(time.RFC3339Nano)}, Metric: result.Metric, Unit: result.Unit, Before: regressionSideOutputFrom(result.Before), After: regressionSideOutputFrom(result.After), AbsoluteDelta: copyOptionalFloat(result.AbsoluteDelta), RelativeDelta: copyOptionalFloat(result.RelativeDelta), Contamination: regressionContaminationOutput{BeforeDeployments: copyStrings(result.Contamination.BeforeDeployments), AfterDeployments: copyStrings(result.Contamination.AfterDeployments), ConcurrentDeployments: copyStrings(result.Contamination.ConcurrentDeployments), Truncated: result.Contamination.Truncated}, BaselineConfidence: regressionConfidenceOutputFrom(result.BaselineConfidence), ObservationConfidence: regressionConfidenceOutputFrom(result.ObservationConfidence), RegressionConfidence: regressionConfidenceOutputFrom(result.RegressionConfidence), Classification: regressionClassificationOutputFrom(result.Classification), CausalityClaimed: false}
}

func regressionSideOutputFrom(side regression.Side) regressionSideOutput {
	accepted := make([]baseline.WindowSummary, 0, len(side.AcceptedWindows))
	for _, window := range side.AcceptedWindows {
		accepted = append(accepted, baseline.WindowSummary{ID: window.ID, Start: window.Start.UTC(), End: window.End.UTC(), SampleCount: window.SampleCount})
	}
	rejected := make([]baseline.RejectedWindow, 0, len(side.RejectedWindows))
	for _, window := range side.RejectedWindows {
		rejected = append(rejected, baseline.RejectedWindow{ID: window.ID, Start: window.Start.UTC(), End: window.End.UTC(), ObservedAt: window.ObservedAt.UTC(), SampleCount: window.SampleCount, CoverageRatio: copyOptionalFloat(window.CoverageRatio), IsComplete: window.IsComplete, Contaminated: window.Contaminated, Reason: window.Reason})
	}
	return regressionSideOutput{Status: side.Status, AlgorithmVersion: side.AlgorithmVersion, Window: regressionWindowOutput{Start: side.WindowStart.UTC().Format(time.RFC3339Nano), End: side.WindowEnd.UTC().Format(time.RFC3339Nano)}, Value: copyOptionalFloat(side.Value), SampleCount: side.SampleCount, CoverageRatio: copyOptionalFloat(side.CoverageRatio), IsComplete: side.IsComplete, AcceptedWindows: accepted, RejectedWindows: rejected}
}

func regressionConfidenceOutputFrom(confidence regression.Confidence) regressionConfidenceOutput {
	return regressionConfidenceOutput{Level: confidence.Level, Basis: confidence.Basis, AlgorithmVersion: confidence.AlgorithmVersion, Limitations: copyStrings(confidence.Limitations)}
}

func regressionClassificationOutputFrom(classification *regression.Classification) *regressionClassificationOutput {
	if classification == nil {
		return nil
	}
	algorithm, _, _ := strings.Cut(classification.AlgorithmVersion, "/")
	return &regressionClassificationOutput{ClassificationKey: classification.ClassificationKey, Result: classification.Result, Direction: classification.Direction, Algorithm: algorithm, AlgorithmVersion: classification.AlgorithmVersion, Thresholds: regressionThresholdsOutput{AbsoluteMin: copyOptionalFloat(classification.Thresholds.AbsoluteMin), RelativeMin: copyOptionalFloat(classification.Thresholds.RelativeMin), RequireAll: classification.Thresholds.RequireAll, Unit: classification.Thresholds.Unit}, ObservedEffect: regressionObservedEffectOutput{AbsoluteDelta: copyOptionalFloat(classification.ObservedEffect.AbsoluteDelta), RelativeDelta: copyOptionalFloat(classification.ObservedEffect.RelativeDelta)}, Confidence: regressionConfidenceOutputFrom(classification.Confidence), CausalityClaimed: false}
}

func copyOptionalFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	return new(*value)
}

func copyStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return slices.Clone(values)
}

func optionalFloatString(value *float64) string {
	if value == nil {
		return "null"
	}
	return fmt.Sprint(*value)
}

func mergeRegressionWarnings(groups ...[]string) []string {
	seen := make(map[string]struct{})
	merged := make([]string, 0)
	for _, group := range groups {
		for _, limitation := range group {
			if _, ok := seen[limitation]; ok {
				continue
			}
			seen[limitation] = struct{}{}
			merged = append(merged, limitation)
		}
	}
	slices.Sort(merged)
	return merged
}
