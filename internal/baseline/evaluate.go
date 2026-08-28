package baseline

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/identity"
)

func Evaluate(ctx context.Context, input Input) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	query := input.Query
	if err := validateQuery(query); err != nil {
		return Result{}, err
	}
	start := query.At.Add(-query.Window)
	result := Result{Status: "UNKNOWN", Method: "previous_window", AlgorithmVersion: AlgorithmVersion, Environment: query.Environment, Target: input.Target, Metric: query.Metric, Unit: metricUnit(query.Metric), At: query.At.UTC(), WindowStart: start.UTC(), WindowEnd: query.At.UTC(), WindowDuration: query.Window, AcceptedWindows: []WindowSummary{}, RejectedWindows: []RejectedWindow{}, Confidence: Confidence{Level: "UNKNOWN", AlgorithmVersion: AlgorithmVersion, Limitations: []string{}}, CausalityClaimed: false}
	unknown := func(basis string, limitations ...string) Result {
		result.Confidence.Basis = basis
		result.Confidence.Limitations = append(result.Confidence.Limitations, limitations...)
		if len(result.Confidence.Limitations) == 0 {
			result.Confidence.Limitations = append(result.Confidence.Limitations, basis)
		}
		result.BaselineKey = fingerprint(query, "")
		return result
	}
	generated := query.GeneratedAt.UTC()
	queryFuture := query.At.After(generated) || start.After(generated)
	if len(input.Candidates) == 0 {
		if queryFuture {
			return unknown("baseline interval is in the future relative to generated_at"), nil
		}
		return unknown("no exact previous telemetry window"), nil
	}
	candidates := slices.Clone(input.Candidates)
	slices.SortFunc(candidates, func(left, right Window) int { return cmp.Compare(left.ID, right.ID) })
	for _, candidate := range candidates {
		reason := rejectionReason(query, start, generated, queryFuture, candidate)
		if reason == "" && len(candidates) > 1 {
			reason = AmbiguousExactWindow
		}
		if reason != "" {
			result.RejectedWindows = append(result.RejectedWindows, rejectedWindow(candidate, reason))
		}
	}
	result.RejectedWindowsCount = len(result.RejectedWindows)
	if queryFuture {
		return unknown("baseline interval is in the future relative to generated_at"), nil
	}
	if len(candidates) > 1 {
		return unknown("multiple exact previous telemetry windows", "exact previous window is ambiguous"), nil
	}
	window := candidates[0]
	if len(result.RejectedWindows) != 0 {
		return unknown(rejectionBasis(result.RejectedWindows[0].Reason)), nil
	}
	summary := WindowSummary{ID: window.ID, Start: window.Start.UTC(), End: window.End.UTC(), SampleCount: window.RequestCount}
	value, ok := metricValue(query.Metric, window)
	if !ok { // rejectionReason validates this condition before the result can be accepted.
		return unknown("exact previous window cannot provide the requested metric"), nil
	}
	result.BaselineKey = fingerprint(query, window.ID)
	result.Status = "AVAILABLE"
	result.Value = new(value)
	result.SampleCount = window.RequestCount
	result.ReferenceWindows = 1
	result.CoverageRatio = window.CoverageRatio
	result.IsComplete = window.IsComplete
	result.AcceptedWindows = append(result.AcceptedWindows, summary)
	result.Confidence = Confidence{Level: "LOW", Basis: "single exact previous window; experimental algorithm caps confidence at LOW", AlgorithmVersion: AlgorithmVersion, Limitations: []string{}}
	if window.CoverageRatio == nil {
		result.Confidence.Limitations = append(result.Confidence.Limitations, "coverage is unknown")
	}
	if !window.IsComplete {
		result.Confidence.Limitations = append(result.Confidence.Limitations, "capture completeness is unverified")
	}
	result.Confidence.Limitations = append(result.Confidence.Limitations, "known deployment contamination checks only registered deployments started within [window_start, window_end); absence of a registered deployment does not establish absence of change, rollout, configuration, incident, or prior residual effect")
	return result, nil
}

func rejectionReason(query Query, start, generated time.Time, queryFuture bool, window Window) RejectionReason {
	if !isValidWindow(window) || !window.Start.Equal(start) || !window.End.Equal(query.At) {
		return InvalidWindow
	}
	if queryFuture || window.ObservedAt.After(generated) {
		return FutureEvidence
	}
	if window.Contaminated {
		return ContaminatedByDeployment
	}
	if query.Metric != RequestCount && window.RequestCount == 0 {
		return MetricUnavailable
	}
	if window.RequestCount < query.MinSamples {
		return InsufficientSamples
	}
	if window.CoverageRatio != nil && *window.CoverageRatio < query.MinCoverage {
		return InsufficientCoverage
	}
	return ""
}

func isValidWindow(window Window) bool {
	if window.ID == "" || window.Start.IsZero() || window.End.IsZero() || window.ObservedAt.IsZero() || !window.Start.Before(window.End) || window.RequestCount < 0 || window.ErrorCount < 0 || window.ErrorCount > window.RequestCount || window.P50NS < 0 || window.P95NS < window.P50NS || window.P99NS < window.P95NS {
		return false
	}
	return window.CoverageRatio == nil || (!math.IsNaN(*window.CoverageRatio) && !math.IsInf(*window.CoverageRatio, 0) && *window.CoverageRatio >= 0 && *window.CoverageRatio <= 1)
}

func rejectedWindow(window Window, reason RejectionReason) RejectedWindow {
	var coverage *float64
	if window.CoverageRatio != nil && !math.IsNaN(*window.CoverageRatio) && !math.IsInf(*window.CoverageRatio, 0) && *window.CoverageRatio >= 0 && *window.CoverageRatio <= 1 {
		coverage = new(*window.CoverageRatio)
	}
	return RejectedWindow{ID: window.ID, Start: window.Start.UTC(), End: window.End.UTC(), ObservedAt: window.ObservedAt.UTC(), SampleCount: window.RequestCount, CoverageRatio: coverage, IsComplete: window.IsComplete, Contaminated: window.Contaminated, Reason: reason}
}

func rejectionBasis(reason RejectionReason) string {
	switch reason {
	case ContaminatedByDeployment:
		return "exact previous window is contaminated by a deployment"
	case InsufficientSamples:
		return "exact previous window has insufficient samples"
	case InsufficientCoverage:
		return "exact previous window has insufficient coverage"
	case FutureEvidence:
		return "telemetry window timestamp is in the future relative to generated_at"
	case MetricUnavailable:
		return "exact previous window cannot provide the requested metric"
	default:
		return "exact previous telemetry window is invalid"
	}
}

func validateQuery(query Query) error {
	if _, err := identity.ValidEnvironment(query.Environment); err != nil || query.Environment == "" {
		return fmt.Errorf("%w: invalid baseline environment", errs.ErrInvalid)
	}
	if (strings.TrimSpace(query.ServiceKey) == "") == (strings.TrimSpace(query.EndpointID) == "") {
		return fmt.Errorf("%w: exactly one baseline target is required", errs.ErrInvalid)
	}
	if !ValidMetric(query.Metric) || query.At.IsZero() || query.GeneratedAt.IsZero() || query.Window < MinWindow || query.Window > MaxWindow || query.MinSamples < 1 || query.MinSamples > 1_000_000 || math.IsNaN(query.MinCoverage) || math.IsInf(query.MinCoverage, 0) || query.MinCoverage < 0 || query.MinCoverage > 1 {
		return fmt.Errorf("%w: invalid baseline query", errs.ErrInvalid)
	}
	return nil
}

// ValidMetric reports whether metric is part of the bounded Baseline Foundation contract.
func ValidMetric(metric Metric) bool {
	return metric == RequestCount || metric == ErrorRate || metric == LatencyP50 || metric == LatencyP95 || metric == LatencyP99
}

func metricValue(metric Metric, window Window) (float64, bool) {
	if window.RequestCount <= 0 && metric != RequestCount {
		return 0, false
	}
	switch metric {
	case RequestCount:
		return float64(window.RequestCount), true
	case ErrorRate:
		return float64(window.ErrorCount) / float64(window.RequestCount), true
	case LatencyP50:
		return float64(window.P50NS), true
	case LatencyP95:
		return float64(window.P95NS), true
	case LatencyP99:
		return float64(window.P99NS), true
	default:
		return 0, false
	}
}

func metricUnit(metric Metric) string {
	if metric == ErrorRate {
		return "ratio"
	}
	if metric == RequestCount {
		return "requests"
	}
	return "nanoseconds"
}

func fingerprint(query Query, windowID string) string {
	parts := []string{AlgorithmVersion, query.Environment, string(targetKind(query)), targetID(query), string(query.Metric), query.At.UTC().Format(time.RFC3339Nano), query.Window.String(), fmt.Sprint(query.MinSamples), fmt.Sprintf("%.17g", query.MinCoverage), windowID}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func targetKind(query Query) TargetKind {
	if query.EndpointID != "" {
		return EndpointTarget
	}
	return ServiceTarget
}

func targetID(query Query) string {
	if query.EndpointID != "" {
		return query.EndpointID
	}
	return query.ServiceKey
}
