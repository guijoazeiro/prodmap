package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/guijoazeiro/prodmap/internal/baseline"
	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/investigation"
	investigationpackage "github.com/guijoazeiro/prodmap/internal/investigationpackage"
	"github.com/guijoazeiro/prodmap/internal/regression"
	"github.com/guijoazeiro/prodmap/internal/timeline"
)

func writeInvestigateUsage(writer interface{ Write([]byte) (int, error) }) {
	fmt.Fprintln(writer, "usage: prodmap investigate --deployment <UUID> --metric <request_count|error_rate|latency_p50|latency_p95|latency_p99> [--before <5m..24h>] [--after <5m..24h>] [--min-samples <1..1000000>] [--min-coverage <0..1>] [--project-dir <path>] [--data-dir <path>] [--json]")
}

func (a *App) runInvestigate(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("investigate", flag.ContinueOnError)
	flags.Usage = func() { writeInvestigateUsage(flags.Output()) }
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
		return fmt.Errorf("parse investigate flags: %w: %v", errs.ErrInvalid, err)
	}
	comparisonQuery := regression.Query{DeploymentID: strings.TrimSpace(*deploymentID), Metric: baseline.Metric(*metric), Before: *before, After: *after, MinSamples: *minSamples, MinCoverage: *minCoverage}
	if flags.NArg() != 0 || regression.ValidateQuery(comparisonQuery) != nil {
		return fmt.Errorf("invalid investigate flags: %w", errs.ErrInvalid)
	}
	_, store, err := a.openInventoryReadOnly(ctx, common)
	if err != nil {
		return err
	}
	defer store.Close()
	generatedAt := a.Now().UTC()
	result, err := composeInvestigationSnapshot(ctx, store, investigation.Query{Comparison: comparisonQuery, GeneratedAt: generatedAt})
	if err != nil {
		return fmt.Errorf("compose investigation: %w", err)
	}
	data, err := investigationpackage.OutputFrom(result)
	if err != nil {
		return fmt.Errorf("render investigation output: %w", err)
	}
	if *common.jsonOutput {
		return WriteSuccess(a.Stdout, "investigate", generatedAt, data, []string{}, nil)
	}
	fmt.Fprintf(a.Stdout, "Investigation: %s %s\nDeployment: %s\nClassification: %s\nTopology: roots=%d nodes=%d edges=%d\nTimeline: events=%d\n", data.Status, data.Regression.Metric, data.Deployment.ID, data.Regression.Classification.Result, len(data.Topology.Roots), len(data.Topology.Nodes), len(data.Topology.Edges), len(data.Timeline.Items))
	for _, limitation := range data.Limitations {
		fmt.Fprintf(a.Stderr, "warning: %s\n", limitation)
	}
	return nil
}

type investigationOutput struct {
	InvestigationVersion string                                `json:"investigation_version"`
	InvestigationKey     string                                `json:"investigation_key"`
	Status               string                                `json:"status"`
	Deployment           investigationDeploymentOutput         `json:"deployment"`
	Regression           investigationRegressionOutput         `json:"regression"`
	Topology             investigationTopologyOutput           `json:"topology"`
	Timeline             investigationTimelineOutput           `json:"timeline"`
	EvidenceReferences   investigationEvidenceReferencesOutput `json:"evidence_references"`
	Limitations          []string                              `json:"limitations"`
	CausalityClaimed     bool                                  `json:"causality_claimed"`
}

type investigationEvidenceReferencesOutput struct {
	AcceptedWindowIDs []string `json:"accepted_window_ids"`
	RejectedWindowIDs []string `json:"rejected_window_ids"`
	GraphEvidenceIDs  []string `json:"graph_evidence_ids"`
	TimelineEventIDs  []string `json:"timeline_event_ids"`
}

type investigationDeploymentOutput struct {
	ID          string `json:"id"`
	Environment string `json:"environment"`
	Service     string `json:"service"`
	StartedAt   string `json:"started_at"`
}

type investigationRegressionOutput struct {
	ComparisonKey         string                          `json:"comparison_key"`
	Status                string                          `json:"status"`
	AlgorithmVersion      string                          `json:"algorithm_version"`
	Deployment            investigationDeploymentOutput   `json:"deployment"`
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

type investigationTopologyOutput struct {
	At          string            `json:"at"`
	Environment string            `json:"environment"`
	Roots       []string          `json:"roots"`
	Nodes       []graphNodeOutput `json:"nodes"`
	Edges       []graphEdgeOutput `json:"edges"`
	Truncated   bool              `json:"truncated"`
}

type investigationTimelineOutput struct {
	Since       string                       `json:"since"`
	Until       string                       `json:"until"`
	Environment string                       `json:"environment"`
	Items       []investigationTimelineEvent `json:"items"`
}

type investigationTimelineEvent struct {
	ID               string                                 `json:"id"`
	Time             string                                 `json:"time"`
	Kind             string                                 `json:"kind"`
	Environment      string                                 `json:"environment"`
	Service          string                                 `json:"service"`
	RelationType     string                                 `json:"relation_type"`
	Subject          investigationSubjectOutput             `json:"subject"`
	Source           timelineSourceOutput                   `json:"source"`
	Confidence       confidenceOutput                       `json:"confidence"`
	Deployment       *investigationTimelineDeploymentOutput `json:"deployment"`
	Runtime          *investigationRuntimeOutput            `json:"runtime"`
	Concurrency      investigationConcurrencyOutput         `json:"concurrency"`
	Limitations      []string                               `json:"limitations"`
	CausalityClaimed bool                                   `json:"causality_claimed"`
}

type investigationRuntimeOutput struct {
	State        string `json:"state"`
	Health       string `json:"health"`
	RestartCount int    `json:"restart_count"`
}

type investigationTimelineDeploymentOutput struct {
	Status               string           `json:"status"`
	Strategy             string           `json:"strategy"`
	ProvenanceStatus     string           `json:"provenance_status"`
	ProvenanceConfidence confidenceOutput `json:"provenance_confidence"`
}

type investigationConcurrencyOutput struct {
	Detected bool `json:"detected"`
	Count    int  `json:"count"`
}

type investigationSubjectOutput struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func investigationOutputFrom(result investigation.Result, generatedAt time.Time) investigationOutput {
	topologyOutput := investigationTopologyOutput{At: result.Topology.At.UTC().Format(time.RFC3339Nano), Environment: result.Topology.Environment, Roots: copyStrings(result.Topology.Roots), Nodes: make([]graphNodeOutput, 0, len(result.Topology.Nodes)), Edges: make([]graphEdgeOutput, 0, len(result.Topology.Edges)), Truncated: result.Topology.Truncated}
	for _, node := range result.Topology.Nodes {
		topologyOutput.Nodes = append(topologyOutput.Nodes, graphNodeOutput{ID: node.ID, Type: node.Type, LogicalKey: node.LogicalKey, DisplayName: node.DisplayName})
	}
	for _, edge := range result.Topology.Edges {
		topologyOutput.Edges = append(topologyOutput.Edges, graphEdgeOutput{ID: edge.ID, From: edge.From, To: edge.To, RelationType: "OBSERVED", DependencyKind: edge.DependencyKind, WindowStart: edge.WindowStart.UTC().Format(time.RFC3339Nano), WindowEnd: edge.WindowEnd.UTC().Format(time.RFC3339Nano), RequestCount: edge.RequestCount, ErrorCount: edge.ErrorCount, DurationSumNS: edge.DurationSumNS, Confidence: graphConfidenceOutput{Level: edge.Confidence, Basis: edge.Basis, AlgorithmVersion: edge.AlgorithmVersion}, EvidenceIDs: copyStrings(edge.EvidenceIDs), Limitations: copyStrings(edge.Limitations)})
	}
	timelineOutput := investigationTimelineOutput{Since: result.Timeline.Since.UTC().Format(time.RFC3339Nano), Until: result.Timeline.Until.UTC().Format(time.RFC3339Nano), Environment: result.Timeline.Environment, Items: make([]investigationTimelineEvent, 0, len(result.Timeline.Items))}
	for _, event := range result.Timeline.Items {
		timelineOutput.Items = append(timelineOutput.Items, investigationTimelineEventFrom(event, generatedAt))
	}
	return investigationOutput{InvestigationVersion: result.InvestigationVersion, InvestigationKey: result.InvestigationKey, Status: result.Status, Deployment: investigationDeploymentOutputFrom(result.Deployment), Regression: investigationRegressionOutputFrom(result.Regression), Topology: topologyOutput, Timeline: timelineOutput, EvidenceReferences: investigationEvidenceReferencesOutput{AcceptedWindowIDs: copyStrings(result.EvidenceReferences.AcceptedWindowIDs), RejectedWindowIDs: copyStrings(result.EvidenceReferences.RejectedWindowIDs), GraphEvidenceIDs: copyStrings(result.EvidenceReferences.GraphEvidenceIDs), TimelineEventIDs: copyStrings(result.EvidenceReferences.TimelineEventIDs)}, Limitations: copyStrings(result.Limitations), CausalityClaimed: false}
}

func investigationDeploymentOutputFrom(deployment regression.Deployment) investigationDeploymentOutput {
	return investigationDeploymentOutput{ID: deployment.ID, Environment: deployment.Environment, Service: deployment.Service, StartedAt: deployment.StartedAt.UTC().Format(time.RFC3339Nano)}
}

func investigationRegressionOutputFrom(result regression.Result) investigationRegressionOutput {
	output := regressionOutputFrom(result)
	return investigationRegressionOutput{ComparisonKey: output.ComparisonKey, Status: output.Status, AlgorithmVersion: output.AlgorithmVersion, Deployment: investigationDeploymentOutputFrom(result.Deployment), Metric: output.Metric, Unit: output.Unit, Before: output.Before, After: output.After, AbsoluteDelta: output.AbsoluteDelta, RelativeDelta: output.RelativeDelta, Contamination: output.Contamination, BaselineConfidence: output.BaselineConfidence, ObservationConfidence: output.ObservationConfidence, RegressionConfidence: output.RegressionConfidence, Classification: output.Classification, CausalityClaimed: false}
}

func investigationTimelineEventFrom(event timeline.Event, generatedAt time.Time) investigationTimelineEvent {
	value := timelineOutputFrom(event, generatedAt)
	var deployment *investigationTimelineDeploymentOutput
	if value.Deployment != nil {
		deployment = &investigationTimelineDeploymentOutput{Status: value.Deployment.Status, Strategy: value.Deployment.Strategy, ProvenanceStatus: value.Deployment.ProvenanceStatus, ProvenanceConfidence: confidenceOutput{Level: value.Deployment.ProvenanceConfidence.Level, Basis: value.Deployment.ProvenanceConfidence.Basis}}
	}
	var runtime *investigationRuntimeOutput
	if value.Runtime != nil {
		runtime = &investigationRuntimeOutput{State: value.Runtime.State, Health: value.Runtime.Health, RestartCount: value.Runtime.RestartCount}
	}
	return investigationTimelineEvent{ID: value.ID, Time: value.Time, Kind: value.Kind, Environment: value.Environment, Service: value.Service, RelationType: value.RelationType, Subject: investigationSubjectOutput{Type: value.Subject.Type, ID: value.Subject.ID}, Source: value.Source, Confidence: value.Confidence, Deployment: deployment, Runtime: runtime, Concurrency: investigationConcurrencyOutput{Detected: value.Concurrency.Detected, Count: value.Concurrency.Count}, Limitations: copyStrings(value.Limitations), CausalityClaimed: false}
}
