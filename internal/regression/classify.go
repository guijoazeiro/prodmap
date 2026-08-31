package regression

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"

	"github.com/guijoazeiro/prodmap/internal/baseline"
	"github.com/guijoazeiro/prodmap/internal/errs"
)

func Classify(ctx context.Context, comparison Result) (Classification, error) {
	if err := ctx.Err(); err != nil {
		return Classification{}, err
	}
	if comparison.AlgorithmVersion != AlgorithmVersion || !validKey(comparison.ComparisonKey) || comparison.CausalityClaimed || !validMetricUnit(comparison.Metric, comparison.Unit) {
		return Classification{}, fmt.Errorf("%w: incompatible comparison", errs.ErrIncompatible)
	}
	c := Classification{Result: "UNKNOWN", Direction: direction(comparison.AbsoluteDelta), AlgorithmVersion: ClassificationAlgorithmVersion, ObservedEffect: Effect{comparison.AbsoluteDelta, comparison.RelativeDelta}, CausalityClaimed: false}
	c.Thresholds = thresholds(comparison.Metric)
	unknown := func(b string) Classification {
		c.Confidence = Confidence{Level: "UNKNOWN", Basis: b, AlgorithmVersion: ClassificationAlgorithmVersion, Limitations: []string{b, "temporal proximity does not establish causality"}}
		c.ClassificationKey = classificationKey(comparison, c)
		return c
	}
	if comparison.Status != "AVAILABLE" || comparison.RegressionConfidence.Level != "LOW" || comparison.BaselineConfidence.Level != "LOW" || comparison.ObservationConfidence.Level != "LOW" || comparison.AbsoluteDelta == nil || !finite(comparison.AbsoluteDelta) || comparison.Contamination.Truncated || len(comparison.Contamination.BeforeDeployments) > 0 || len(comparison.Contamination.AfterDeployments) > 0 || len(comparison.Contamination.ConcurrentDeployments) > 0 {
		return unknown("comparison is insufficient for conservative classification"), nil
	}
	if comparison.Metric == baseline.RequestCount {
		return unknown("request_count is not classifiable by the experimental threshold algorithm"), nil
	}
	if comparison.Metric != baseline.ErrorRate && (!finite(comparison.RelativeDelta) || comparison.RelativeDelta == nil) {
		return unknown("relative delta is required for latency classification"), nil
	}
	candidate := false
	if comparison.Metric == baseline.ErrorRate {
		candidate = *comparison.AbsoluteDelta >= .05
	} else {
		candidate = *comparison.AbsoluteDelta >= 50_000_000 && *comparison.RelativeDelta >= .20
	}
	if candidate {
		c.Result = "CANDIDATE"
		c.Confidence = Confidence{Level: "LOW", Basis: "experimental thresholds were met", AlgorithmVersion: ClassificationAlgorithmVersion, Limitations: []string{"thresholds are experimental hypotheses", "candidate does not mean confirmed regression", "temporal proximity does not establish causality"}}
	} else {
		c.Result = "NO_SIGNAL"
		c.Confidence = Confidence{Level: "LOW", Basis: "no material signal under regression-threshold/v1-experimental", AlgorithmVersion: ClassificationAlgorithmVersion, Limitations: []string{"thresholds are experimental hypotheses", "no signal does not prove healthy behavior", "temporal proximity does not establish causality"}}
	}
	c.ClassificationKey = classificationKey(comparison, c)
	return c, nil
}
func thresholds(m baseline.Metric) Thresholds {
	if m == baseline.ErrorRate {
		v := .05
		return Thresholds{AbsoluteMin: &v, Unit: "ratio"}
	}
	if m == baseline.RequestCount {
		return Thresholds{Unit: "requests"}
	}
	a, r := float64(50_000_000), .20
	return Thresholds{AbsoluteMin: &a, RelativeMin: &r, RequireAll: true, Unit: "nanoseconds"}
}
func direction(v *float64) string {
	if v == nil || !finite(v) {
		return "UNKNOWN"
	}
	if *v > 0 {
		return "INCREASE"
	}
	if *v < 0 {
		return "DECREASE"
	}
	return "UNCHANGED"
}
func finite(v *float64) bool { return v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) }
func validMetricUnit(m baseline.Metric, u string) bool {
	return (m == baseline.ErrorRate && u == "ratio") || (m == baseline.RequestCount && u == "requests") || ((m == baseline.LatencyP50 || m == baseline.LatencyP95 || m == baseline.LatencyP99) && u == "nanoseconds")
}
func validKey(v string) bool {
	if len(v) != 71 || !strings.HasPrefix(v, "sha256:") || strings.Trim(v[7:], "0") == "" {
		return false
	}
	for _, r := range v[7:] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
func classificationKey(r Result, c Classification) string {
	p := strings.Join([]string{"algorithm", c.AlgorithmVersion, "comparison", r.ComparisonKey, "metric", string(r.Metric), "unit", r.Unit, "result", c.Result, "direction", c.Direction, "absolute_min", floatPart(c.Thresholds.AbsoluteMin), "relative_min", floatPart(c.Thresholds.RelativeMin), "require_all", fmt.Sprint(c.Thresholds.RequireAll), "threshold_unit", c.Thresholds.Unit, "absolute_delta", floatPart(c.ObservedEffect.AbsoluteDelta), "relative_delta", floatPart(c.ObservedEffect.RelativeDelta), "confidence", c.Confidence.Level, c.Confidence.Basis}, "\x00")
	for _, l := range c.Confidence.Limitations {
		p += "\x00" + l
	}
	s := sha256.Sum256([]byte(p))
	return "sha256:" + hex.EncodeToString(s[:])
}
func floatPart(v *float64) string {
	if v == nil {
		return "null"
	}
	return fmt.Sprintf("%.17g", *v)
}
