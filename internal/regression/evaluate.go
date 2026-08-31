package regression

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/guijoazeiro/prodmap/internal/baseline"
	"github.com/guijoazeiro/prodmap/internal/errs"
)

func Evaluate(ctx context.Context, input Input) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := ValidateQuery(input.Query); err != nil {
		return Result{}, err
	}
	if input.Deployment.ID == "" || input.Deployment.Environment == "" || input.Deployment.Service == "" || input.Deployment.StartedAt.IsZero() || input.GeneratedAt.IsZero() {
		return Result{}, fmt.Errorf("%w: incomplete deployment comparison input", errs.ErrInvalid)
	}
	deployment := input.Deployment
	deployment.StartedAt = deployment.StartedAt.UTC()
	generatedAt := input.GeneratedAt.UTC()
	beforeStart := deployment.StartedAt.Add(-input.Query.Before)
	afterEnd := deployment.StartedAt.Add(input.Query.After)
	before, baselineConfidence, err := evaluateSide(ctx, input, input.Before, beforeStart, deployment.StartedAt, baselineRole, len(input.Contamination.BeforeDeployments) != 0)
	if err != nil {
		return Result{}, err
	}
	after, observationConfidence, err := evaluateSide(ctx, input, input.After, deployment.StartedAt, afterEnd, observationRole, len(input.Contamination.AfterDeployments) != 0)
	if err != nil {
		return Result{}, err
	}
	result := Result{Status: "UNKNOWN", AlgorithmVersion: AlgorithmVersion, Deployment: deployment, Metric: input.Query.Metric, Unit: metricUnit(input.Query.Metric), Before: before, After: after, Contamination: normalizedContamination(input.Contamination), BaselineConfidence: baselineConfidence, ObservationConfidence: observationConfidence, Classification: nil, CausalityClaimed: false}
	comparisonLimitations := comparisonLimitations()
	if before.Status == "AVAILABLE" && after.Status == "AVAILABLE" && !result.Contamination.Truncated && len(result.Contamination.BeforeDeployments) == 0 && len(result.Contamination.AfterDeployments) == 0 && len(result.Contamination.ConcurrentDeployments) == 0 && !afterEnd.After(generatedAt) {
		absolute := *after.Value - *before.Value
		result.AbsoluteDelta = new(absolute)
		if *before.Value != 0 {
			relative := absolute / *before.Value
			result.RelativeDelta = new(relative)
		}
		result.Status = "AVAILABLE"
		result.RegressionConfidence = Confidence{Level: "LOW", Basis: "two exact windows are mechanically comparable; experimental algorithm caps confidence at LOW", AlgorithmVersion: AlgorithmVersion, Limitations: mergeLimitations(comparisonLimitations, roleSpecificLimitations(baselineRole, baselineConfidence.Limitations), roleSpecificLimitations(observationRole, observationConfidence.Limitations))}
	} else {
		limitations := []string{"comparison requires eligible exact baseline and observation windows without known contamination or concurrency"}
		if afterEnd.After(generatedAt) {
			limitations = append(limitations, "post-deployment interval is in the future relative to generated_at")
		}
		if len(result.Contamination.BeforeDeployments) != 0 || len(result.Contamination.AfterDeployments) != 0 || len(result.Contamination.ConcurrentDeployments) != 0 {
			limitations = append(limitations, "registered deployments contaminate or overlap the comparison intervals")
		}
		if result.Contamination.Truncated {
			limitations = append(limitations, "registered deployment contamination list exceeded its limit")
		}
		result.RegressionConfidence = Confidence{Level: "UNKNOWN", Basis: limitations[0], AlgorithmVersion: AlgorithmVersion, Limitations: limitations}
	}
	result.ComparisonKey = fingerprint(input, result)
	return result, nil
}

type windowRole string

const (
	baselineRole    windowRole = "BASELINE"
	observationRole windowRole = "OBSERVATION"
)

func evaluateSide(ctx context.Context, input Input, candidates []baseline.Window, start, end time.Time, role windowRole, contaminated bool) (Side, Confidence, error) {
	algorithm := baseline.AlgorithmVersion
	if role == observationRole {
		algorithm = ObservationAlgorithmVersion
	}
	query := baseline.Query{Environment: input.Deployment.Environment, ServiceKey: input.Deployment.Service, Metric: input.Query.Metric, At: end, Window: end.Sub(start), MinSamples: input.Query.MinSamples, MinCoverage: input.Query.MinCoverage, GeneratedAt: input.GeneratedAt.UTC()}
	copyCandidates := slices.Clone(candidates)
	if contaminated {
		for index := range copyCandidates {
			copyCandidates[index].Contaminated = true
		}
	}
	assessment, err := baseline.Evaluate(ctx, baseline.Input{Query: query, Target: input.Target, Candidates: copyCandidates})
	if err != nil {
		return Side{}, Confidence{}, err
	}
	confidence := confidenceForRole(role, assessment, candidates, end, input.GeneratedAt.UTC())
	return Side{Status: assessment.Status, AlgorithmVersion: algorithm, WindowStart: start.UTC(), WindowEnd: end.UTC(), Value: assessment.Value, SampleCount: assessment.SampleCount, CoverageRatio: assessment.CoverageRatio, IsComplete: assessment.IsComplete, AcceptedWindows: slices.Clone(assessment.AcceptedWindows), RejectedWindows: slices.Clone(assessment.RejectedWindows)}, confidence, nil
}

func confidenceForRole(role windowRole, assessment baseline.Result, candidates []baseline.Window, end, generatedAt time.Time) Confidence {
	algorithm := baseline.AlgorithmVersion
	availableBasis, missingBasis, ambiguousBasis := "single exact baseline window; experimental algorithm caps confidence at LOW", "no exact baseline window", "multiple exact baseline windows"
	if role == observationRole {
		algorithm = ObservationAlgorithmVersion
		availableBasis, missingBasis, ambiguousBasis = "single exact post-deployment observation window; experimental algorithm caps confidence at LOW", "no exact post-deployment observation window", "multiple exact post-deployment observation windows"
	}
	if assessment.Status == "AVAILABLE" {
		return Confidence{Level: "LOW", Basis: availableBasis, AlgorithmVersion: algorithm, Limitations: mergeLimitations(componentLimitations(role, assessment.Confidence.Limitations), comparisonLimitations())}
	}
	basis := missingBasis
	if end.After(generatedAt) {
		if role == observationRole {
			basis = "post-deployment observation interval is in the future relative to generated_at"
		} else {
			basis = "baseline interval is in the future relative to generated_at"
		}
	} else if len(candidates) > 1 {
		basis = ambiguousBasis
	} else if len(assessment.RejectedWindows) != 0 {
		basis = rejectionBasis(role, assessment.RejectedWindows[0].Reason)
	}
	return Confidence{Level: "UNKNOWN", Basis: basis, AlgorithmVersion: algorithm, Limitations: []string{basis}}
}

func rejectionBasis(role windowRole, reason baseline.RejectionReason) string {
	prefix := "baseline window"
	if role == observationRole {
		prefix = "post-deployment observation window"
	}
	switch reason {
	case baseline.ContaminatedByDeployment:
		return prefix + " is contaminated by a registered deployment"
	case baseline.InsufficientSamples:
		return prefix + " has insufficient samples"
	case baseline.InsufficientCoverage:
		return prefix + " has insufficient coverage"
	case baseline.FutureEvidence:
		return prefix + " contains evidence in the future relative to generated_at"
	case baseline.MetricUnavailable:
		return prefix + " cannot provide the requested metric"
	default:
		return prefix + " is invalid"
	}
}

func componentLimitations(role windowRole, limitations []string) []string {
	result := make([]string, 0, len(limitations))
	prefix := "baseline"
	if role == observationRole {
		prefix = "observation"
	}
	for _, limitation := range limitations {
		result = append(result, prefix+": "+limitation)
	}
	return result
}

func roleSpecificLimitations(role windowRole, limitations []string) []string {
	prefix := "baseline: "
	if role == observationRole {
		prefix = "observation: "
	}
	result := []string{}
	for _, limitation := range limitations {
		if strings.HasPrefix(limitation, prefix) {
			result = append(result, limitation)
		}
	}
	return result
}

func mergeLimitations(groups ...[]string) []string {
	seen := make(map[string]struct{})
	result := []string{}
	for _, group := range groups {
		for _, limitation := range group {
			if _, found := seen[limitation]; found {
				continue
			}
			seen[limitation] = struct{}{}
			result = append(result, limitation)
		}
	}
	return result
}

// ValidateQuery rejects malformed command parameters before any storage access.
func ValidateQuery(query Query) error {
	if !validUUIDv7(query.DeploymentID) || !baseline.ValidMetric(query.Metric) || query.Before < baseline.MinWindow || query.Before > baseline.MaxWindow || query.After < baseline.MinWindow || query.After > baseline.MaxWindow || (query.Metric == baseline.RequestCount && query.Before != query.After) || query.MinSamples < 1 || query.MinSamples > 1_000_000 || math.IsNaN(query.MinCoverage) || math.IsInf(query.MinCoverage, 0) || query.MinCoverage < 0 || query.MinCoverage > 1 {
		return fmt.Errorf("%w: invalid deployment comparison query", errs.ErrInvalid)
	}
	return nil
}

func validUUIDv7(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[14] != '7' || value[18] != '-' || value[19] < '8' || value[19] > 'b' || value[23] != '-' {
		return false
	}
	for index := range len(value) {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !('0' <= value[index] && value[index] <= '9' || 'a' <= value[index] && value[index] <= 'f') {
			return false
		}
	}
	return true
}

func metricUnit(metric baseline.Metric) string {
	if metric == baseline.RequestCount {
		return "requests"
	}
	if metric == baseline.ErrorRate {
		return "ratio"
	}
	return "nanoseconds"
}

func comparisonLimitations() []string {
	return []string{
		"comparison uses only two exact windows",
		"no historical or seasonal baseline is available",
		"percentile metrics are sensitive to the observed sample set",
		"known deployment contamination is limited to registered deployments",
		"temporal proximity does not establish causality",
	}
}

func normalizedContamination(value Contamination) Contamination {
	result := Contamination{BeforeDeployments: sortedUnique(value.BeforeDeployments), AfterDeployments: sortedUnique(value.AfterDeployments), ConcurrentDeployments: sortedUnique(value.ConcurrentDeployments), Truncated: value.Truncated}
	return result
}

func sortedUnique(values []string) []string {
	result := slices.Sorted(maps.Keys(mapSet(values)))
	if result == nil {
		return []string{}
	}
	return result
}

func mapSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value != "" {
			result[value] = struct{}{}
		}
	}
	return result
}

func fingerprint(input Input, result Result) string {
	parts := []string{"comparison_algorithm", AlgorithmVersion, "baseline_algorithm", baseline.AlgorithmVersion, "observation_algorithm", ObservationAlgorithmVersion, "deployment", result.Deployment.ID, result.Deployment.StartedAt.UTC().Format(time.RFC3339Nano), result.Deployment.Environment, result.Deployment.Service, "query", string(result.Metric), input.Query.Before.String(), input.Query.After.String(), fmt.Sprint(input.Query.MinSamples), canonicalFloat(input.Query.MinCoverage), "status", result.Status}
	parts = append(parts, sideFingerprint("before", result.Before)...)
	parts = append(parts, sideFingerprint("after", result.After)...)
	parts = append(parts, confidenceFingerprint("baseline_confidence", result.BaselineConfidence)...)
	parts = append(parts, confidenceFingerprint("observation_confidence", result.ObservationConfidence)...)
	parts = append(parts, confidenceFingerprint("regression_confidence", result.RegressionConfidence)...)
	parts = append(parts, valueFingerprint("before_value", result.Before.Value)...)
	parts = append(parts, valueFingerprint("after_value", result.After.Value)...)
	parts = append(parts, valueFingerprint("absolute_delta", result.AbsoluteDelta)...)
	parts = append(parts, valueFingerprint("relative_delta", result.RelativeDelta)...)
	for _, id := range result.Contamination.BeforeDeployments {
		parts = append(parts, "contamination_before", id)
	}
	for _, id := range result.Contamination.AfterDeployments {
		parts = append(parts, "contamination_after", id)
	}
	for _, id := range result.Contamination.ConcurrentDeployments {
		parts = append(parts, "contamination_concurrent", id)
	}
	parts = append(parts, fmt.Sprint(result.Contamination.Truncated))
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func sideFingerprint(name string, side Side) []string {
	parts := []string{name, side.Status, side.AlgorithmVersion, side.WindowStart.UTC().Format(time.RFC3339Nano), side.WindowEnd.UTC().Format(time.RFC3339Nano), fmt.Sprint(side.SampleCount), fmt.Sprint(side.IsComplete)}
	parts = append(parts, valueFingerprint("coverage", side.CoverageRatio)...)
	for _, window := range side.AcceptedWindows {
		parts = append(parts, "accepted", window.ID, window.Start.UTC().Format(time.RFC3339Nano), window.End.UTC().Format(time.RFC3339Nano), fmt.Sprint(window.SampleCount))
	}
	for _, window := range side.RejectedWindows {
		parts = append(parts, "rejected", window.ID, window.Start.UTC().Format(time.RFC3339Nano), window.End.UTC().Format(time.RFC3339Nano), window.ObservedAt.UTC().Format(time.RFC3339Nano), fmt.Sprint(window.SampleCount), fmt.Sprint(window.IsComplete), fmt.Sprint(window.Contaminated), string(window.Reason))
		parts = append(parts, valueFingerprint("rejected_coverage", window.CoverageRatio)...)
	}
	return parts
}

func confidenceFingerprint(name string, confidence Confidence) []string {
	parts := []string{name, confidence.Level, confidence.Basis, confidence.AlgorithmVersion}
	for _, limitation := range confidence.Limitations {
		parts = append(parts, "limitation", limitation)
	}
	return parts
}

func valueFingerprint(name string, value *float64) []string {
	if value == nil {
		return []string{name, "null"}
	}
	return []string{name, canonicalFloat(*value)}
}

func canonicalFloat(value float64) string {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "invalid"
	}
	return fmt.Sprintf("%.17g", value)
}
