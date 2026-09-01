package cli

import (
	"context"
	"flag"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/guijoazeiro/prodmap/internal/baseline"
	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/identity"
)

func writeBaselineUsage(writer interface{ Write([]byte) (int, error) }) {
	fmt.Fprintln(writer, "usage: prodmap baseline (--service <logical-key> | --endpoint <UUID>) [--environment <environment>] --metric <request_count|error_rate|latency_p50|latency_p95|latency_p99> --at <RFC3339> [--window <5m..24h>] [--min-samples <1..1000000>] [--min-coverage <0..1>] [--project-dir <path>] [--data-dir <path>] [--json]")
}

func (a *App) runBaseline(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("baseline", flag.ContinueOnError)
	flags.Usage = func() { writeBaselineUsage(flags.Output()) }
	common := addInventoryFlags(flags)
	service := flags.String("service", "", "logical service key")
	endpoint := flags.String("endpoint", "", "endpoint UUID")
	environment := flags.String("environment", "default", "telemetry environment")
	metric := flags.String("metric", "", "baseline metric")
	atValue := flags.String("at", "", "reference timestamp in RFC3339")
	window := flags.Duration("window", baseline.DefaultWindow, "exact previous window")
	minSamples := flags.Int64("min-samples", baseline.DefaultMinSamples, "minimum samples")
	minCoverage := flags.Float64("min-coverage", baseline.DefaultMinCoverage, "minimum known coverage")
	help, err := a.parseCommandFlags(flags, args)
	if help {
		return nil
	}
	if err != nil {
		return fmt.Errorf("parse baseline flags: %w: %v", errs.ErrInvalid, err)
	}
	if flags.NArg() != 0 || (strings.TrimSpace(*service) == "") == (strings.TrimSpace(*endpoint) == "") || *atValue == "" {
		return fmt.Errorf("baseline requires exactly one of --service or --endpoint, --at, and no positional arguments: %w", errs.ErrInvalid)
	}
	env, err := identity.ValidEnvironment(*environment)
	if err != nil {
		return err
	}
	at, err := parseRFC3339(*atValue, "--at")
	if err != nil {
		return err
	}
	if !baseline.ValidMetric(baseline.Metric(*metric)) || *window < baseline.MinWindow || *window > baseline.MaxWindow || *minSamples < 1 || *minSamples > 1_000_000 || math.IsNaN(*minCoverage) || math.IsInf(*minCoverage, 0) || *minCoverage < 0 || *minCoverage > 1 {
		return fmt.Errorf("invalid baseline flags: %w", errs.ErrInvalid)
	}
	generatedAt := a.Now().UTC()
	query := baseline.Query{Environment: env, ServiceKey: strings.TrimSpace(*service), EndpointID: strings.TrimSpace(*endpoint), Metric: baseline.Metric(*metric), At: at, Window: *window, MinSamples: *minSamples, MinCoverage: *minCoverage, GeneratedAt: generatedAt}
	_, store, err := a.openInventory(ctx, common)
	if err != nil {
		return err
	}
	defer store.Close()
	input, err := store.Baseline(ctx, query)
	if err != nil {
		return fmt.Errorf("query baseline: %w", err)
	}
	result, err := baseline.Evaluate(ctx, input)
	if err != nil {
		return err
	}
	data := baselineOutputFrom(result)
	if *common.jsonOutput {
		return WriteSuccess(a.Stdout, "baseline", generatedAt, data, []string{}, nil)
	}
	fmt.Fprintf(a.Stdout, "Baseline: %s %s\nTarget: %s\nWindow: [%s,%s)\nConfidence: %s\n", data.Status, data.Metric, data.Target.ID, data.Window.Start, data.Window.End, data.Confidence.Level)
	if data.Value != nil {
		fmt.Fprintf(a.Stdout, "Value: %v %s\n", *data.Value, data.Unit)
	}
	for _, limitation := range data.Confidence.Limitations {
		fmt.Fprintf(a.Stderr, "warning: %s\n", limitation)
	}
	return nil
}

type baselineOutput struct {
	BaselineKey          string                    `json:"baseline_key"`
	Status               string                    `json:"status"`
	Method               string                    `json:"method"`
	AlgorithmVersion     string                    `json:"algorithm_version"`
	Environment          string                    `json:"environment"`
	Target               baseline.Target           `json:"target"`
	Metric               baseline.Metric           `json:"metric"`
	Unit                 string                    `json:"unit"`
	At                   string                    `json:"at"`
	Window               baselineWindowOutput      `json:"window"`
	Value                *float64                  `json:"value"`
	Dispersion           *float64                  `json:"dispersion"`
	SampleCount          int64                     `json:"sample_count"`
	ReferenceWindows     int                       `json:"reference_windows"`
	CoverageRatio        *float64                  `json:"coverage_ratio"`
	IsComplete           bool                      `json:"is_complete"`
	AcceptedWindows      []baseline.WindowSummary  `json:"accepted_windows"`
	RejectedWindows      []baseline.RejectedWindow `json:"rejected_windows"`
	RejectedWindowsCount int                       `json:"rejected_windows_count"`
	Confidence           baselineConfidenceOutput  `json:"confidence"`
	CausalityClaimed     bool                      `json:"causality_claimed"`
}

type baselineWindowOutput struct {
	Start           string `json:"start"`
	End             string `json:"end"`
	DurationSeconds int64  `json:"duration_seconds"`
}

type baselineConfidenceOutput struct {
	Level            string   `json:"level"`
	Basis            string   `json:"basis"`
	AlgorithmVersion string   `json:"algorithm_version"`
	Limitations      []string `json:"limitations"`
}

func baselineOutputFrom(result baseline.Result) baselineOutput {
	accepted := make([]baseline.WindowSummary, 0, len(result.AcceptedWindows))
	for _, window := range result.AcceptedWindows {
		accepted = append(accepted, baseline.WindowSummary{ID: window.ID, Start: window.Start.UTC(), End: window.End.UTC(), SampleCount: window.SampleCount})
	}
	rejected := make([]baseline.RejectedWindow, 0, len(result.RejectedWindows))
	for _, window := range result.RejectedWindows {
		var coverage *float64
		if window.CoverageRatio != nil {
			coverage = new(*window.CoverageRatio)
		}
		rejected = append(rejected, baseline.RejectedWindow{ID: window.ID, Start: window.Start.UTC(), End: window.End.UTC(), ObservedAt: window.ObservedAt.UTC(), SampleCount: window.SampleCount, CoverageRatio: coverage, IsComplete: window.IsComplete, Contaminated: window.Contaminated, Reason: window.Reason})
	}
	limitations := append([]string{}, result.Confidence.Limitations...)
	return baselineOutput{BaselineKey: result.BaselineKey, Status: result.Status, Method: result.Method, AlgorithmVersion: result.AlgorithmVersion, Environment: result.Environment, Target: result.Target, Metric: result.Metric, Unit: result.Unit, At: result.At.UTC().Format(time.RFC3339Nano), Window: baselineWindowOutput{Start: result.WindowStart.UTC().Format(time.RFC3339Nano), End: result.WindowEnd.UTC().Format(time.RFC3339Nano), DurationSeconds: int64(result.WindowDuration.Seconds())}, Value: result.Value, Dispersion: result.Dispersion, SampleCount: result.SampleCount, ReferenceWindows: result.ReferenceWindows, CoverageRatio: result.CoverageRatio, IsComplete: result.IsComplete, AcceptedWindows: accepted, RejectedWindows: rejected, RejectedWindowsCount: result.RejectedWindowsCount, Confidence: baselineConfidenceOutput{Level: result.Confidence.Level, Basis: result.Confidence.Basis, AlgorithmVersion: result.Confidence.AlgorithmVersion, Limitations: limitations}, CausalityClaimed: false}
}
