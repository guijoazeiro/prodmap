package regression

import (
	"context"
	"github.com/guijoazeiro/prodmap/internal/baseline"
	"math"
	"strings"
	"testing"
)

func TestClassificationThresholdCorpus(t *testing.T) {
	for _, x := range []struct {
		n    string
		m    baseline.Metric
		b, a float64
		want string
	}{
		{"p50", baseline.LatencyP50, 250e6, 300e6, "CANDIDATE"}, {"p95", baseline.LatencyP95, 250e6, 300e6, "CANDIDATE"}, {"p99", baseline.LatencyP99, 250e6, 300e6, "CANDIDATE"}, {"below absolute", baseline.LatencyP95, 250e6, 299999999, "NO_SIGNAL"}, {"below both", baseline.LatencyP95, 100e6, 149999999, "NO_SIGNAL"}, {"absolute only", baseline.LatencyP95, 400e6, 450e6, "NO_SIGNAL"}, {"relative only", baseline.LatencyP95, 100e6, 120e6, "NO_SIGNAL"}, {"reduction", baseline.LatencyP95, 100e6, 90e6, "NO_SIGNAL"}, {"equal", baseline.LatencyP95, 100e6, 100e6, "NO_SIGNAL"}, {"error exact", baseline.ErrorRate, 0, .05, "CANDIDATE"}, {"error below", baseline.ErrorRate, 0, .049999, "NO_SIGNAL"}, {"error nonzero", baseline.ErrorRate, .10, .15, "CANDIDATE"}, {"request", baseline.RequestCount, 20, 30, "UNKNOWN"},
	} {
		t.Run(x.n, func(t *testing.T) {
			c, e := Classify(t.Context(), comparison(x.m, x.b, x.a))
			if e != nil || c.Result != x.want {
				t.Fatalf("%+v %v", c, e)
			}
		})
	}
}

func TestErrorRateThresholdUsesAtMostTwoULPs(t *testing.T) {
	threshold := .05
	for _, test := range []struct {
		name  string
		delta float64
		want  string
	}{
		{"subtraction", .15 - .10, "CANDIDATE"}, {"literal", threshold, "CANDIDATE"}, {"two ULP", twoULPsBelow(threshold), "CANDIDATE"}, {"three ULP", math.Nextafter(twoULPsBelow(threshold), 0), "NO_SIGNAL"}, {"materially below", .049999, "NO_SIGNAL"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := comparison(baseline.ErrorRate, 0, test.delta)
			c, e := Classify(t.Context(), r)
			if e != nil || c.Result != test.want {
				t.Fatalf("%+v %v", c, e)
			}
		})
	}
}
func TestClassifyUnknownConditions(t *testing.T) {
	for _, x := range []struct {
		n string
		f func(*Result)
	}{
		{"comparison unknown", func(r *Result) { r.Status = "UNKNOWN" }}, {"before contamination", func(r *Result) { r.Contamination.BeforeDeployments = []string{"x"} }}, {"after contamination", func(r *Result) { r.Contamination.AfterDeployments = []string{"x"} }}, {"concurrent", func(r *Result) { r.Contamination.ConcurrentDeployments = []string{"x"} }}, {"truncated", func(r *Result) { r.Contamination.Truncated = true }}, {"absolute nil", func(r *Result) { r.AbsoluteDelta = nil }}, {"relative nil", func(r *Result) { r.RelativeDelta = nil }},
	} {
		t.Run(x.n, func(t *testing.T) {
			r := comparison(baseline.LatencyP95, 100e6, 151e6)
			x.f(&r)
			if x.n != "comparison unknown" {
				r.Status = "UNKNOWN"
			}
			c, e := Classify(t.Context(), r)
			if e != nil || c.Result != "UNKNOWN" {
				t.Fatalf("%+v %v", c, e)
			}
		})
	}
}
func TestClassifyRejectsStructuralInput(t *testing.T) {
	for _, x := range []struct {
		n string
		f func(*Result)
	}{
		{"status", func(r *Result) { r.Status = "BAD" }}, {"algorithm", func(r *Result) { r.AlgorithmVersion = "bad" }}, {"key", func(r *Result) { r.ComparisonKey = "sha256:ABC" }}, {"unit", func(r *Result) { r.Unit = "bad" }}, {"confidence", func(r *Result) { r.BaselineConfidence.Level = "HIGH" }}, {"confidence algorithm", func(r *Result) { r.RegressionConfidence.AlgorithmVersion = "bad" }}, {"causality", func(r *Result) { r.CausalityClaimed = true }}, {"classification", func(r *Result) { r.Classification = &Classification{} }}, {"empty id", func(r *Result) { r.Contamination.BeforeDeployments = []string{""} }},
	} {
		t.Run(x.n, func(t *testing.T) {
			r := comparison(baseline.LatencyP95, 100e6, 151e6)
			x.f(&r)
			if _, e := Classify(t.Context(), r); e == nil {
				t.Fatal("accepted")
			}
		})
	}
	for _, v := range []float64{math.NaN(), math.Inf(1)} {
		r := comparison(baseline.LatencyP95, 100e6, 151e6)
		r.AbsoluteDelta = &v
		if _, e := Classify(t.Context(), r); e == nil {
			t.Fatal("nonfinite accepted")
		}
	}
}

func TestClassifyAdditionalStructuralCases(t *testing.T) {
	mutations := []func(*Result){func(r *Result) { r.BaselineConfidence.Level = "HIGH" }, func(r *Result) { r.ObservationConfidence.Level = "HIGH" }, func(r *Result) { r.RegressionConfidence.Level = "HIGH" }, func(r *Result) { r.BaselineConfidence.AlgorithmVersion = "bad" }, func(r *Result) { r.ObservationConfidence.AlgorithmVersion = "bad" }, func(r *Result) { r.RegressionConfidence.AlgorithmVersion = "bad" }, func(r *Result) { r.Before.Status = "BAD" }, func(r *Result) { r.After.Status = "BAD" }, func(r *Result) { r.Before.Value = nil }, func(r *Result) { r.After.Value = nil }}
	for _, mutate := range mutations {
		r := comparison(baseline.LatencyP95, 100e6, 151e6)
		mutate(&r)
		if _, err := Classify(t.Context(), r); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
	zero := comparison(baseline.LatencyP95, 0, 100e6)
	c, err := Classify(t.Context(), zero)
	if err != nil || c.Result != "UNKNOWN" {
		t.Fatalf("zero=%+v err=%v", c, err)
	}
}
func TestClassificationKeyAndInputImmutability(t *testing.T) {
	r := comparison(baseline.LatencyP95, 100e6, 151e6)
	a, _ := Classify(t.Context(), r)
	b, _ := Classify(t.Context(), comparison(baseline.LatencyP95, 100e6, 151e6))
	if a.ClassificationKey != b.ClassificationKey || !validKey(a.ClassificationKey) || strings.Contains(a.ClassificationKey, "0x") {
		t.Fatal(a.ClassificationKey)
	}
	r.AbsoluteDelta = new(float64)
	*r.AbsoluteDelta = 99
	c, _ := Classify(t.Context(), r)
	if a.ClassificationKey == c.ClassificationKey {
		t.Fatal("key unchanged")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, e := Classify(ctx, r); e == nil {
		t.Fatal("cancel accepted")
	}
}
func comparison(m baseline.Metric, b, a float64) Result {
	d := a - b
	var rel *float64
	if b != 0 {
		v := d / b
		rel = &v
	}
	return Result{ComparisonKey: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Status: "AVAILABLE", AlgorithmVersion: AlgorithmVersion, Metric: m, Unit: metricUnit(m), Before: Side{Status: "AVAILABLE", Value: &b}, After: Side{Status: "AVAILABLE", Value: &a}, AbsoluteDelta: &d, RelativeDelta: rel, BaselineConfidence: Confidence{Level: "LOW", AlgorithmVersion: baseline.AlgorithmVersion}, ObservationConfidence: Confidence{Level: "LOW", AlgorithmVersion: ObservationAlgorithmVersion}, RegressionConfidence: Confidence{Level: "LOW", AlgorithmVersion: AlgorithmVersion}, Contamination: Contamination{BeforeDeployments: []string{}, AfterDeployments: []string{}, ConcurrentDeployments: []string{}}}
}
