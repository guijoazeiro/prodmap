package baseline

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/errs"
)

func TestEvaluateAcceptsEachSupportedMetric(t *testing.T) {
	at := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	coverage := 0.9
	for _, test := range []struct {
		metric Metric
		want   float64
	}{
		{RequestCount, 12},
		{ErrorRate, 0.25},
		{LatencyP50, 10},
		{LatencyP95, 20},
		{LatencyP99, 30},
	} {
		t.Run(string(test.metric), func(t *testing.T) {
			result, err := Evaluate(t.Context(), validInput(at, test.metric, Window{ID: "window-1", Start: at.Add(-DefaultWindow), End: at, ObservedAt: at, RequestCount: 12, ErrorCount: 3, P50NS: 10, P95NS: 20, P99NS: 30, CoverageRatio: &coverage, IsComplete: true}))
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != "AVAILABLE" || result.Confidence.Level != "LOW" || result.Value == nil || *result.Value != test.want || result.CausalityClaimed || !slices.Contains(result.Confidence.Limitations, "known deployment contamination checks only registered deployments started within [window_start, window_end); absence of a registered deployment does not establish absence of change, rollout, configuration, incident, or prior residual effect") {
				t.Fatalf("result=%+v", result)
			}
			if len(result.AcceptedWindows) != 1 || len(result.RejectedWindows) != 0 || result.BaselineKey == "" {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestEvaluateIsConservativeForRejectedWindows(t *testing.T) {
	at := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	coverage := 0.5
	cases := []struct {
		name   string
		input  Input
		reason RejectionReason
	}{
		{"none", validInput(at, RequestCount), ""},
		{"ambiguous", validInput(at, RequestCount, validWindow(at, "a"), validWindow(at, "b")), AmbiguousExactWindow},
		{"contaminated", validInput(at, RequestCount, Window{ID: "a", Start: at.Add(-DefaultWindow), End: at, ObservedAt: at, RequestCount: 12, Contaminated: true}), ContaminatedByDeployment},
		{"samples", validInput(at, RequestCount, Window{ID: "a", Start: at.Add(-DefaultWindow), End: at, ObservedAt: at, RequestCount: 9}), InsufficientSamples},
		{"zero denominator", validInput(at, ErrorRate, Window{ID: "a", Start: at.Add(-DefaultWindow), End: at, ObservedAt: at, RequestCount: 0}), MetricUnavailable},
		{"coverage", validInput(at, RequestCount, Window{ID: "a", Start: at.Add(-DefaultWindow), End: at, ObservedAt: at, RequestCount: 12, CoverageRatio: &coverage}), InsufficientCoverage},
		{"future window", validInput(at, RequestCount, Window{ID: "a", Start: at.Add(-DefaultWindow), End: at, ObservedAt: at.Add(time.Nanosecond), RequestCount: 12}), FutureEvidence},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			result, err := Evaluate(t.Context(), test.input)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != "UNKNOWN" || result.Confidence.Level != "UNKNOWN" || result.Value != nil || result.CausalityClaimed || result.BaselineKey == "" || len(result.Confidence.Limitations) == 0 {
				t.Fatalf("result=%+v", result)
			}
			if test.reason == "" {
				if len(result.RejectedWindows) != 0 || result.RejectedWindowsCount != 0 {
					t.Fatalf("unexpected rejected=%+v", result.RejectedWindows)
				}
				return
			}
			if len(result.RejectedWindows) == 0 || result.RejectedWindows[0].Reason != test.reason || result.RejectedWindowsCount != len(result.RejectedWindows) {
				t.Fatalf("rejected=%+v", result.RejectedWindows)
			}
		})
	}
}

func TestEvaluateRecordsRejectedWindowsDeterministically(t *testing.T) {
	at := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	coverage := .9
	first := Window{ID: "b", Start: at.Add(-DefaultWindow), End: at, ObservedAt: at, RequestCount: 12, CoverageRatio: &coverage, IsComplete: false}
	second := Window{ID: "a", Start: at.Add(-DefaultWindow), End: at, ObservedAt: at, RequestCount: 12, CoverageRatio: &coverage, IsComplete: false}
	result, err := Evaluate(t.Context(), validInput(at, RequestCount, first, second))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "UNKNOWN" || len(result.AcceptedWindows) != 0 || result.RejectedWindowsCount != 2 || len(result.RejectedWindows) != 2 {
		t.Fatalf("result=%+v", result)
	}
	for index, id := range []string{"a", "b"} {
		window := result.RejectedWindows[index]
		if window.ID != id || window.Reason != AmbiguousExactWindow || window.CoverageRatio == nil || *window.CoverageRatio != coverage || window.IsComplete || window.Contaminated || !window.ObservedAt.Equal(at) {
			t.Fatalf("rejected[%d]=%+v", index, window)
		}
	}
}

func TestEvaluateRejectsInvalidCandidateEvidenceWithoutJSONSpecialValues(t *testing.T) {
	at := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	base := validWindow(at, "window")
	cases := []struct {
		name   string
		mutate func(*Window)
	}{
		{"empty ID", func(window *Window) { window.ID = "" }},
		{"zero start", func(window *Window) { window.Start = time.Time{} }},
		{"zero observed", func(window *Window) { window.ObservedAt = time.Time{} }},
		{"inverted interval", func(window *Window) { window.Start = window.End }},
		{"negative request count", func(window *Window) { window.RequestCount = -1 }},
		{"negative error count", func(window *Window) { window.ErrorCount = -1 }},
		{"error count above request count", func(window *Window) { window.ErrorCount = window.RequestCount + 1 }},
		{"negative percentile", func(window *Window) { window.P50NS = -1 }},
		{"unordered percentiles", func(window *Window) { window.P50NS, window.P95NS, window.P99NS = 3, 2, 1 }},
		{"coverage NaN", func(window *Window) { value := math.NaN(); window.CoverageRatio = &value }},
		{"coverage infinity", func(window *Window) { value := math.Inf(1); window.CoverageRatio = &value }},
		{"coverage negative", func(window *Window) { value := -.1; window.CoverageRatio = &value }},
		{"coverage above one", func(window *Window) { value := 1.1; window.CoverageRatio = &value }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			window := base
			test.mutate(&window)
			result, err := Evaluate(t.Context(), validInput(at, RequestCount, window))
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != "UNKNOWN" || len(result.RejectedWindows) != 1 || result.RejectedWindows[0].Reason != InvalidWindow {
				t.Fatalf("result=%+v", result)
			}
			encoded, err := json.Marshal(result)
			if err != nil || strings.Contains(string(encoded), "NaN") || strings.Contains(string(encoded), "Inf") {
				t.Fatalf("encoded=%q err=%v", encoded, err)
			}
		})
	}
}

func TestEvaluateIsIndependentOfCandidateOrderAndRespectsCancellation(t *testing.T) {
	at := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	input := validInput(at, RequestCount, validWindow(at, "b"), validWindow(at, "a"))
	first, err := Evaluate(t.Context(), input)
	if err != nil || first.Confidence.Basis != "multiple exact previous telemetry windows" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	input.Candidates[0], input.Candidates[1] = input.Candidates[1], input.Candidates[0]
	second, err := Evaluate(t.Context(), input)
	if err != nil || first.BaselineKey != second.BaselineKey || first.Confidence.Basis != second.Confidence.Basis {
		t.Fatalf("first=%+v second=%+v err=%v", first, second, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Evaluate(ctx, validInput(at, RequestCount, validWindow(at, "a"))); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled err=%v", err)
	}
}

func TestEvaluateAllowsUnknownCoverageAndIncompleteCaptureOnlyAtLow(t *testing.T) {
	at := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	result, err := Evaluate(t.Context(), validInput(at, RequestCount, Window{ID: "a", Start: at.Add(-DefaultWindow), End: at, ObservedAt: at, RequestCount: 12}))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "AVAILABLE" || result.Confidence.Level != "LOW" || len(result.Confidence.Limitations) != 3 || !slices.Contains(result.Confidence.Limitations, "known deployment contamination checks only registered deployments started within [window_start, window_end); absence of a registered deployment does not establish absence of change, rollout, configuration, incident, or prior residual effect") || result.CoverageRatio != nil || result.IsComplete {
		t.Fatalf("result=%+v", result)
	}
}

func TestEvaluateRejectsInvalidQueryBeforeEvaluation(t *testing.T) {
	at := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	input := validInput(at, RequestCount, validWindow(at, "a"))
	input.Query.MinCoverage = math.NaN()
	if _, err := Evaluate(context.Background(), input); err == nil || !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("err=%v", err)
	}
}

func TestFingerprintChangesForEveryInputDimension(t *testing.T) {
	at := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	base := validInput(at, RequestCount, validWindow(at, "a"))
	first, err := Evaluate(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	mutations := []func(*Input){
		func(in *Input) { in.Query.Environment = "other" },
		func(in *Input) { in.Query.ServiceKey = "payments" },
		func(in *Input) { in.Query.Metric = ErrorRate },
		func(in *Input) {
			in.Query.At = in.Query.At.Add(time.Minute)
			in.Candidates[0].Start = in.Query.At.Add(-in.Query.Window)
			in.Candidates[0].End = in.Query.At
		},
		func(in *Input) {
			in.Query.Window = 31 * time.Minute
			in.Candidates[0].Start = in.Query.At.Add(-in.Query.Window)
		},
		func(in *Input) { in.Query.MinSamples = 11 },
		func(in *Input) { in.Query.MinCoverage = .7 },
		func(in *Input) { in.Candidates[0].ID = "b" },
	}
	for _, mutate := range mutations {
		candidate := base
		candidate.Candidates = append([]Window{}, base.Candidates...)
		mutate(&candidate)
		got, err := Evaluate(t.Context(), candidate)
		if err != nil {
			t.Fatal(err)
		}
		if got.BaselineKey == first.BaselineKey {
			t.Fatalf("fingerprint did not change for %+v", candidate.Query)
		}
	}
}

func validInput(at time.Time, metric Metric, windows ...Window) Input {
	return Input{Query: Query{Environment: "reference", ServiceKey: "checkout", Metric: metric, At: at, Window: DefaultWindow, MinSamples: DefaultMinSamples, MinCoverage: DefaultMinCoverage, GeneratedAt: at}, Target: Target{Kind: ServiceTarget, ID: "service-1", Service: "checkout"}, Candidates: windows}
}

func validWindow(at time.Time, id string) Window {
	coverage := 1.0
	return Window{ID: id, Start: at.Add(-DefaultWindow), End: at, ObservedAt: at, RequestCount: 12, CoverageRatio: &coverage, IsComplete: true}
}
