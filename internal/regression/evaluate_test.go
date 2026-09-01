package regression

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/baseline"
	"github.com/guijoazeiro/prodmap/internal/errs"
)

func TestEvaluateComputesAllSupportedMetricsAndDeltas(t *testing.T) {
	for _, test := range []struct {
		metric               baseline.Metric
		before, after, delta float64
	}{
		{baseline.RequestCount, 20, 30, 10},
		{baseline.ErrorRate, .1, .2, .1},
		{baseline.LatencyP50, 10, 20, 10},
		{baseline.LatencyP95, 20, 30, 10},
		{baseline.LatencyP99, 30, 40, 10},
	} {
		t.Run(string(test.metric), func(t *testing.T) {
			input := validInput(test.metric)
			input.Before[0] = validWindow(input.Deployment.StartedAt.Add(-input.Query.Before), input.Deployment.StartedAt, "before", 20, 2, 10, 20, 30)
			input.After[0] = validWindow(input.Deployment.StartedAt, input.Deployment.StartedAt.Add(input.Query.After), "after", 30, 6, 20, 30, 40)
			result, err := Evaluate(t.Context(), input)
			if err != nil || result.Status != "AVAILABLE" || result.AbsoluteDelta == nil || *result.AbsoluteDelta != test.delta || result.Before.Value == nil || *result.Before.Value != test.before || result.After.Value == nil || *result.After.Value != test.after || result.RegressionConfidence.Level != "LOW" || result.Classification != nil || result.CausalityClaimed {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if !slices.Contains(result.RegressionConfidence.Limitations, "temporal proximity does not establish causality") {
				t.Fatalf("limitations=%v", result.RegressionConfidence.Limitations)
			}
		})
	}
}

func TestEvaluateIsConservativeForInsufficiencyContaminationAndFutureEvidence(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Input)
	}{
		{"missing after", func(input *Input) { input.After = []baseline.Window{} }},
		{"missing before", func(input *Input) { input.Before = []baseline.Window{} }},
		{"ambiguous before", func(input *Input) { input.Before = append(input.Before, input.Before[0]); input.Before[1].ID = "other" }},
		{"ambiguous after", func(input *Input) { input.After = append(input.After, input.After[0]); input.After[1].ID = "other" }},
		{"low coverage", func(input *Input) { coverage := .2; input.After[0].CoverageRatio = &coverage }},
		{"invalid coverage", func(input *Input) { coverage := math.NaN(); input.After[0].CoverageRatio = &coverage }},
		{"invalid count", func(input *Input) { input.After[0].ErrorCount = input.After[0].RequestCount + 1 }},
		{"invalid percentiles", func(input *Input) { input.After[0].P95NS = input.After[0].P50NS - 1 }},
		{"metric unavailable", func(input *Input) { input.After[0].RequestCount = 0; input.After[0].ErrorCount = 0 }},
		{"future observation", func(input *Input) { input.After[0].ObservedAt = input.GeneratedAt.Add(time.Nanosecond) }},
		{"baseline contamination", func(input *Input) { input.Contamination.BeforeDeployments = []string{"b"} }},
		{"observation contamination", func(input *Input) { input.Contamination.AfterDeployments = []string{"a"} }},
		{"concurrent deployment", func(input *Input) { input.Contamination.ConcurrentDeployments = []string{"c"} }},
		{"future interval", func(input *Input) { input.GeneratedAt = input.Deployment.StartedAt.Add(10 * time.Minute) }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			input := validInput(baseline.LatencyP95)
			test.mutate(&input)
			result, err := Evaluate(t.Context(), input)
			if err != nil || result.Status != "UNKNOWN" || result.AbsoluteDelta != nil || result.RelativeDelta != nil || result.RegressionConfidence.Level != "UNKNOWN" || result.Classification != nil || result.CausalityClaimed {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			encoded, encodeErr := json.Marshal(result)
			if encodeErr != nil || strings.Contains(string(encoded), "NaN") || strings.Contains(string(encoded), "Inf") || result.BaselineConfidence.Level == "HIGH" || result.BaselineConfidence.Level == "EXACT" || result.ObservationConfidence.Level == "HIGH" || result.ObservationConfidence.Level == "EXACT" || result.RegressionConfidence.Level == "HIGH" || result.RegressionConfidence.Level == "EXACT" {
				t.Fatalf("encoded=%q err=%v", encoded, encodeErr)
			}
			for _, forbidden := range []string{"\"Level\":\"HIGH\"", "\"Level\":\"EXACT\"", "CANDIDATE", "CONFIRMED", "NO_SIGNAL"} {
				if strings.Contains(string(encoded), forbidden) {
					t.Fatalf("forbidden result term %q in %s", forbidden, encoded)
				}
			}
			for _, value := range append([]string{result.ObservationConfidence.Basis}, result.ObservationConfidence.Limitations...) {
				for _, forbidden := range []string{"baseline interval", "baseline window", "previous window"} {
					if strings.Contains(value, forbidden) {
						t.Fatalf("post-deployment observation was named as baseline: %+v", result.ObservationConfidence)
					}
				}
			}
		})
	}
}

func TestEvaluateSupportsReductionEqualityAndUnknownCoverage(t *testing.T) {
	for _, test := range []struct {
		name         string
		before       int64
		after        int64
		wantDelta    float64
		wantRelative float64
	}{
		{name: "reduction", before: 30, after: 20, wantDelta: -10, wantRelative: -1.0 / 3.0},
		{name: "equal", before: 20, after: 20, wantDelta: 0, wantRelative: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := validInput(baseline.RequestCount)
			input.Before[0].RequestCount = test.before
			input.After[0].RequestCount = test.after
			result, err := Evaluate(t.Context(), input)
			if err != nil || result.Status != "AVAILABLE" || result.AbsoluteDelta == nil || result.RelativeDelta == nil || *result.AbsoluteDelta != test.wantDelta || math.Abs(*result.RelativeDelta-test.wantRelative) > 1e-12 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
	input := validInput(baseline.RequestCount)
	input.After[0].CoverageRatio = nil
	result, err := Evaluate(t.Context(), input)
	if err != nil || result.Status != "AVAILABLE" || result.After.CoverageRatio != nil {
		t.Fatalf("unknown coverage result=%+v err=%v", result, err)
	}
}

func TestEvaluatePreservesComponentLimitationsWithoutDuplicates(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(*Input)
		expected string
	}{
		{"baseline unknown coverage", func(input *Input) { input.Before[0].CoverageRatio = nil }, "baseline: coverage is unknown"},
		{"observation unknown coverage", func(input *Input) { input.After[0].CoverageRatio = nil }, "observation: coverage is unknown"},
		{"baseline incomplete", func(input *Input) { input.Before[0].IsComplete = false }, "baseline: capture completeness is unverified"},
		{"observation incomplete", func(input *Input) { input.After[0].IsComplete = false }, "observation: capture completeness is unverified"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			input := validInput(baseline.LatencyP95)
			test.mutate(&input)
			result, err := Evaluate(t.Context(), input)
			if err != nil || result.Status != "AVAILABLE" || !slices.Contains(result.RegressionConfidence.Limitations, test.expected) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			all := append(append([]string{}, result.BaselineConfidence.Limitations...), result.ObservationConfidence.Limitations...)
			if !slices.Contains(all, test.expected) {
				t.Fatalf("component limitations=%v", all)
			}
			if strings.Contains(strings.Join(result.ObservationConfidence.Limitations, " "), "baseline window") || strings.Contains(strings.Join(result.ObservationConfidence.Limitations, " "), "previous window") || len(result.RegressionConfidence.Limitations) != uniqueLimitations(result.RegressionConfidence.Limitations) {
				t.Fatalf("observation=%v regression=%v", result.ObservationConfidence, result.RegressionConfidence)
			}
		})
	}
	base := validInput(baseline.LatencyP95)
	first, err := Evaluate(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	base.Before[0].CoverageRatio = nil
	second, err := Evaluate(t.Context(), base)
	if err != nil || first.ComparisonKey == second.ComparisonKey {
		t.Fatalf("first=%+v second=%+v err=%v", first, second, err)
	}
}

func uniqueLimitations(values []string) int {
	seen := map[string]struct{}{}
	for _, value := range values {
		seen[value] = struct{}{}
	}
	return len(seen)
}

func TestEvaluateHandlesZeroBaselineAndIsDeterministic(t *testing.T) {
	input := validInput(baseline.LatencyP95)
	input.Before[0].P50NS = 0
	input.Before[0].P95NS = 0
	result, err := Evaluate(t.Context(), input)
	if err != nil || result.Status != "AVAILABLE" || result.RelativeDelta != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	input = validInput(baseline.LatencyP95)
	input.Contamination.BeforeDeployments = []string{"z", "a", "z"}
	first, err := Evaluate(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.Contamination.BeforeDeployments = []string{"a", "z"}
	second, err := Evaluate(t.Context(), input)
	if err != nil || first.ComparisonKey != second.ComparisonKey {
		t.Fatalf("first=%+v second=%+v err=%v", first, second, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Evaluate(ctx, input); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled err=%v", err)
	}
	input.Query.MinCoverage = math.NaN()
	if _, err := Evaluate(t.Context(), input); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("invalid err=%v", err)
	}
}

func TestValidateQueryRequiresCanonicalUUIDv7(t *testing.T) {
	query := validInput(baseline.RequestCount).Query
	query.DeploymentID = "not-a-uuid"
	if err := ValidateQuery(query); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("non-UUID err=%v", err)
	}
	query.DeploymentID = "01a049db-14e4-6798-983b-2946718efaa7"
	if err := ValidateQuery(query); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("non-v7 err=%v", err)
	}
	query.DeploymentID = "01a049db-14e4-7798-783b-2946718efaa7"
	if err := ValidateQuery(query); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("non-RFC variant err=%v", err)
	}
}

func TestValidateQueryRejectsUnequalRequestCountDurations(t *testing.T) {
	query := validInput(baseline.RequestCount).Query
	query.After = 45 * time.Minute
	if err := ValidateQuery(query); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("request_count unequal duration err=%v", err)
	}
	query.Metric = baseline.LatencyP95
	if err := ValidateQuery(query); err != nil {
		t.Fatalf("latency unequal duration err=%v", err)
	}
	query.Metric = baseline.ErrorRate
	if err := ValidateQuery(query); err != nil {
		t.Fatalf("error_rate unequal duration err=%v", err)
	}
}

func TestComparisonKeyCapturesUnknownReasonAndIgnoresCandidateOrder(t *testing.T) {
	future := validInput(baseline.LatencyP95)
	future.GeneratedAt = future.Deployment.StartedAt.Add(10 * time.Minute)
	future.After = []baseline.Window{}
	futureResult, err := Evaluate(t.Context(), future)
	if err != nil || futureResult.Status != "UNKNOWN" {
		t.Fatalf("future result=%+v err=%v", futureResult, err)
	}
	missing := validInput(baseline.LatencyP95)
	missing.After = []baseline.Window{}
	missingResult, err := Evaluate(t.Context(), missing)
	if err != nil || missingResult.Status != "UNKNOWN" || futureResult.ObservationConfidence.Basis == missingResult.ObservationConfidence.Basis || futureResult.ComparisonKey == missingResult.ComparisonKey {
		t.Fatalf("future=%+v missing=%+v err=%v", futureResult, missingResult, err)
	}
	ambiguous := validInput(baseline.LatencyP95)
	second := ambiguous.After[0]
	second.ID = "z"
	ambiguous.After = append(ambiguous.After, second)
	first, err := Evaluate(t.Context(), ambiguous)
	if err != nil {
		t.Fatal(err)
	}
	ambiguous.After[0], ambiguous.After[1] = ambiguous.After[1], ambiguous.After[0]
	secondResult, err := Evaluate(t.Context(), ambiguous)
	if err != nil || first.ComparisonKey != secondResult.ComparisonKey {
		t.Fatalf("first=%+v second=%+v err=%v", first, secondResult, err)
	}
}

func validInput(metric baseline.Metric) Input {
	deploymentAt := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
	query := Query{DeploymentID: "01a049db-14e4-7798-983b-2946718efaa7", Metric: metric, Before: 30 * time.Minute, After: 30 * time.Minute, MinSamples: 10, MinCoverage: .8}
	return Input{Query: query, GeneratedAt: deploymentAt.Add(time.Hour), Deployment: Deployment{ID: query.DeploymentID, ExternalID: "safe-id", Environment: "reference", Service: "payment-api", StartedAt: deploymentAt}, Target: baseline.Target{Kind: baseline.ServiceTarget, ID: "service-1", Service: "payment-api"}, Before: []baseline.Window{validWindow(deploymentAt.Add(-query.Before), deploymentAt, "before", 20, 2, 10, 20, 30)}, After: []baseline.Window{validWindow(deploymentAt, deploymentAt.Add(query.After), "after", 30, 6, 20, 30, 40)}, Contamination: Contamination{BeforeDeployments: []string{}, AfterDeployments: []string{}, ConcurrentDeployments: []string{}}}
}

func validWindow(start, end time.Time, id string, requests, errors, p50, p95, p99 int64) baseline.Window {
	coverage := 1.0
	return baseline.Window{ID: id, Start: start, End: end, ObservedAt: end, RequestCount: requests, ErrorCount: errors, P50NS: p50, P95NS: p95, P99NS: p99, CoverageRatio: &coverage, IsComplete: true}
}
