// Package investigation composes bounded read models into an agent-ready view.
package investigation

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/regression"
	"github.com/guijoazeiro/prodmap/internal/timeline"
	"github.com/guijoazeiro/prodmap/internal/topology"
)

const (
	Version       = "investigation-view/v1"
	graphMaxNodes = 100
)

// Reader is owned by the investigation consumer and implemented by local SQLite.
type Reader interface {
	regression.Reader
	topology.Reader
	Timeline(context.Context, timeline.Query) (timeline.Result, error)
}

type Query struct {
	Comparison  regression.Query
	GeneratedAt time.Time
}

type EvidenceReferences struct {
	AcceptedWindowIDs []string
	RejectedWindowIDs []string
	GraphEvidenceIDs  []string
	TimelineEventIDs  []string
}

type Result struct {
	InvestigationVersion string
	InvestigationKey     string
	Status               string
	Deployment           regression.Deployment
	Regression           regression.Result
	Topology             topology.Result
	Timeline             timeline.Result
	EvidenceReferences   EvidenceReferences
	Limitations          []string
	CausalityClaimed     bool
}

// Compose evaluates existing read models without adding analytical logic.
func Compose(ctx context.Context, reader Reader, query Query) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := regression.ValidateQuery(query.Comparison); err != nil || query.GeneratedAt.IsZero() {
		return Result{}, fmt.Errorf("%w: invalid investigation query", errs.ErrInvalid)
	}
	generatedAt := query.GeneratedAt.UTC()
	input, err := reader.Comparison(ctx, query.Comparison)
	if err != nil {
		return Result{}, err
	}
	input.GeneratedAt = generatedAt
	comparison, err := regression.Evaluate(ctx, input)
	if err != nil {
		return Result{}, err
	}
	classification, err := regression.Classify(ctx, comparison)
	if err != nil {
		return Result{}, err
	}
	comparison.Classification = &classification

	graph, graphLimitation, err := composeGraph(ctx, reader, comparison.Deployment)
	if err != nil {
		return Result{}, err
	}
	timelineResult, err := reader.Timeline(ctx, timeline.Query{
		Environment: comparison.Deployment.Environment,
		Service:     comparison.Deployment.Service,
		Since:       comparison.Deployment.StartedAt.Add(-query.Comparison.Before),
		Until:       comparison.Deployment.StartedAt.Add(query.Comparison.After),
		Limit:       timeline.DefaultLimit,
	})
	if err != nil {
		return Result{}, err
	}
	if timelineResult.Items == nil {
		timelineResult.Items = []timeline.Event{}
	}

	limitations := collectLimitations(comparison, graph, timelineResult, graphLimitation)
	result := Result{
		InvestigationVersion: Version,
		Status:               comparison.Status,
		Deployment:           comparison.Deployment,
		Regression:           comparison,
		Topology:             graph,
		Timeline:             timelineResult,
		EvidenceReferences:   evidenceReferences(comparison, graph, timelineResult),
		Limitations:          limitations,
		CausalityClaimed:     false,
	}
	result.InvestigationKey, err = key(result)
	if err != nil {
		return Result{}, fmt.Errorf("encode investigation key: %w", err)
	}
	return result, nil
}

func composeGraph(ctx context.Context, reader topology.Reader, deployment regression.Deployment) (topology.Result, string, error) {
	result, err := topology.Build(ctx, reader, topology.Query{
		Service:       deployment.Service,
		Environment:   deployment.Environment,
		At:            deployment.StartedAt.UTC(),
		Depth:         1,
		MinConfidence: topology.Low,
		MaxNodes:      graphMaxNodes,
	})
	if errors.Is(err, errs.ErrNotFound) {
		return topology.Result{At: deployment.StartedAt.UTC(), Environment: deployment.Environment, Roots: []string{}, Nodes: []topology.Node{}, Edges: []topology.Edge{}, Warnings: []string{}}, "no observed topology relations matched the deployment service and time", nil
	}
	if err != nil {
		return topology.Result{}, "", err
	}
	if result.Roots == nil {
		result.Roots = []string{}
	}
	if result.Nodes == nil {
		result.Nodes = []topology.Node{}
	}
	if result.Edges == nil {
		result.Edges = []topology.Edge{}
	}
	if result.Warnings == nil {
		result.Warnings = []string{}
	}
	if len(result.Edges) == 0 {
		return result, "no observed topology relations matched the deployment service and time", nil
	}
	return result, "", nil
}

func evidenceReferences(comparison regression.Result, graph topology.Result, timelineResult timeline.Result) EvidenceReferences {
	accepted, rejected := []string{}, []string{}
	for _, side := range []regression.Side{comparison.Before, comparison.After} {
		for _, window := range side.AcceptedWindows {
			accepted = append(accepted, window.ID)
		}
		for _, window := range side.RejectedWindows {
			rejected = append(rejected, window.ID)
		}
	}
	graphEvidence, timelineIDs := []string{}, []string{}
	for _, edge := range graph.Edges {
		graphEvidence = append(graphEvidence, edge.EvidenceIDs...)
	}
	for _, event := range timelineResult.Items {
		timelineIDs = append(timelineIDs, event.ID)
	}
	return EvidenceReferences{
		AcceptedWindowIDs: uniqueSorted(accepted),
		RejectedWindowIDs: uniqueSorted(rejected),
		GraphEvidenceIDs:  uniqueSorted(graphEvidence),
		TimelineEventIDs:  uniqueSorted(timelineIDs),
	}
}

func collectLimitations(comparison regression.Result, graph topology.Result, timelineResult timeline.Result, graphLimitation string) []string {
	limitations := []string{}
	for _, confidence := range []regression.Confidence{comparison.BaselineConfidence, comparison.ObservationConfidence, comparison.RegressionConfidence, comparison.Classification.Confidence} {
		limitations = append(limitations, confidence.Limitations...)
	}
	limitations = append(limitations, graph.Warnings...)
	if graphLimitation != "" {
		limitations = append(limitations, graphLimitation)
	}
	for _, edge := range graph.Edges {
		limitations = append(limitations, edge.Limitations...)
	}
	if len(timelineResult.Items) == 0 {
		limitations = append(limitations, "no timeline events matched the investigation interval")
	}
	if timelineResult.NextCursor != "" {
		limitations = append(limitations, "timeline results were limited by the existing page limit")
	}
	for _, event := range timelineResult.Items {
		limitations = append(limitations, event.Limitations...)
	}
	return uniqueSorted(limitations)
}

func uniqueSorted(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	values = slices.Clone(values)
	slices.Sort(values)
	return slices.Compact(values)
}

func key(result Result) (string, error) {
	payload, err := json.Marshal(canonicalInvestigationFrom(result))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// canonicalInvestigation deliberately excludes generated_at and freshness: both
// describe rendering time, not the reproducible analytical content.
type canonicalInvestigation struct {
	Version            string                      `json:"version"`
	Status             string                      `json:"status"`
	ComparisonKey      string                      `json:"comparison_key"`
	ClassificationKey  string                      `json:"classification_key"`
	DeploymentID       string                      `json:"deployment_id"`
	Topology           canonicalTopology           `json:"topology"`
	Timeline           canonicalTimeline           `json:"timeline"`
	EvidenceReferences canonicalEvidenceReferences `json:"evidence_references"`
	Limitations        []string                    `json:"limitations"`
	CausalityClaimed   bool                        `json:"causality_claimed"`
}

type canonicalEvidenceReferences struct {
	AcceptedWindowIDs []string `json:"accepted_window_ids"`
	RejectedWindowIDs []string `json:"rejected_window_ids"`
	GraphEvidenceIDs  []string `json:"graph_evidence_ids"`
	TimelineEventIDs  []string `json:"timeline_event_ids"`
}

type canonicalTopology struct {
	At          string          `json:"at"`
	Environment string          `json:"environment"`
	Roots       []string        `json:"roots"`
	Nodes       []canonicalNode `json:"nodes"`
	Edges       []canonicalEdge `json:"edges"`
	Truncated   bool            `json:"truncated"`
}

type canonicalNode struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	LogicalKey  string `json:"logical_key"`
	DisplayName string `json:"display_name"`
}

type canonicalEdge struct {
	ID               string   `json:"id"`
	From             string   `json:"from"`
	To               string   `json:"to"`
	DependencyKind   string   `json:"dependency_kind"`
	WindowStart      string   `json:"window_start"`
	WindowEnd        string   `json:"window_end"`
	RequestCount     int64    `json:"request_count"`
	ErrorCount       int64    `json:"error_count"`
	DurationSumNS    int64    `json:"duration_sum_ns"`
	Confidence       string   `json:"confidence"`
	Basis            string   `json:"basis"`
	AlgorithmVersion string   `json:"algorithm_version"`
	EvidenceIDs      []string `json:"evidence_ids"`
	Limitations      []string `json:"limitations"`
}

type canonicalTimeline struct {
	Since       string                   `json:"since"`
	Until       string                   `json:"until"`
	Environment string                   `json:"environment"`
	Limited     bool                     `json:"limited"`
	Events      []canonicalTimelineEvent `json:"events"`
}

type canonicalTimelineEvent struct {
	ID                  string                       `json:"id"`
	Time                string                       `json:"time"`
	Kind                string                       `json:"kind"`
	Environment         string                       `json:"environment"`
	Service             string                       `json:"service"`
	RelationType        string                       `json:"relation_type"`
	SubjectType         string                       `json:"subject_type"`
	SubjectID           string                       `json:"subject_id"`
	SourceKind          string                       `json:"source_kind"`
	SourceObservedAt    string                       `json:"source_observed_at"`
	ConfidenceLevel     string                       `json:"confidence_level"`
	ConfidenceBasis     string                       `json:"confidence_basis"`
	Deployment          *canonicalTimelineDeployment `json:"deployment"`
	Runtime             *canonicalRuntime            `json:"runtime"`
	ConcurrencyDetected bool                         `json:"concurrency_detected"`
	ConcurrencyCount    int                          `json:"concurrency_count"`
	Limitations         []string                     `json:"limitations"`
}

type canonicalTimelineDeployment struct {
	Status                    string `json:"status"`
	Strategy                  string `json:"strategy"`
	ProvenanceStatus          string `json:"provenance_status"`
	ProvenanceConfidenceLevel string `json:"provenance_confidence_level"`
	ProvenanceConfidenceBasis string `json:"provenance_confidence_basis"`
}

type canonicalRuntime struct {
	State        string `json:"state"`
	Health       string `json:"health"`
	RestartCount int    `json:"restart_count"`
}

func canonicalInvestigationFrom(result Result) canonicalInvestigation {
	classificationKey := ""
	if result.Regression.Classification != nil {
		classificationKey = result.Regression.Classification.ClassificationKey
	}
	return canonicalInvestigation{
		Version:           result.InvestigationVersion,
		Status:            result.Status,
		ComparisonKey:     result.Regression.ComparisonKey,
		ClassificationKey: classificationKey,
		DeploymentID:      result.Deployment.ID,
		Topology:          canonicalTopologyFrom(result.Topology),
		Timeline:          canonicalTimelineFrom(result.Timeline),
		EvidenceReferences: canonicalEvidenceReferences{
			AcceptedWindowIDs: uniqueSorted(result.EvidenceReferences.AcceptedWindowIDs),
			RejectedWindowIDs: uniqueSorted(result.EvidenceReferences.RejectedWindowIDs),
			GraphEvidenceIDs:  uniqueSorted(result.EvidenceReferences.GraphEvidenceIDs),
			TimelineEventIDs:  uniqueSorted(result.EvidenceReferences.TimelineEventIDs),
		},
		Limitations:      uniqueSorted(result.Limitations),
		CausalityClaimed: false,
	}
}

func canonicalTopologyFrom(result topology.Result) canonicalTopology {
	output := canonicalTopology{At: result.At.UTC().Format(time.RFC3339Nano), Environment: result.Environment, Roots: uniqueSorted(result.Roots), Nodes: make([]canonicalNode, 0, len(result.Nodes)), Edges: make([]canonicalEdge, 0, len(result.Edges)), Truncated: result.Truncated}
	for _, node := range result.Nodes {
		output.Nodes = append(output.Nodes, canonicalNode{ID: node.ID, Type: node.Type, LogicalKey: node.LogicalKey, DisplayName: node.DisplayName})
	}
	for _, edge := range result.Edges {
		output.Edges = append(output.Edges, canonicalEdge{ID: edge.ID, From: edge.From, To: edge.To, DependencyKind: edge.DependencyKind, WindowStart: edge.WindowStart.UTC().Format(time.RFC3339Nano), WindowEnd: edge.WindowEnd.UTC().Format(time.RFC3339Nano), RequestCount: edge.RequestCount, ErrorCount: edge.ErrorCount, DurationSumNS: edge.DurationSumNS, Confidence: string(edge.Confidence), Basis: edge.Basis, AlgorithmVersion: edge.AlgorithmVersion, EvidenceIDs: uniqueSorted(edge.EvidenceIDs), Limitations: uniqueSorted(edge.Limitations)})
	}
	slices.SortFunc(output.Nodes, canonicalNodeCompare)
	slices.SortFunc(output.Edges, canonicalEdgeCompare)
	return output
}

func canonicalTimelineFrom(result timeline.Result) canonicalTimeline {
	output := canonicalTimeline{Since: result.Since.UTC().Format(time.RFC3339Nano), Until: result.Until.UTC().Format(time.RFC3339Nano), Environment: result.Environment, Limited: result.NextCursor != "", Events: make([]canonicalTimelineEvent, 0, len(result.Items))}
	for _, event := range result.Items {
		output.Events = append(output.Events, canonicalTimelineEventFrom(event))
	}
	slices.SortFunc(output.Events, canonicalTimelineEventCompare)
	return output
}

func canonicalTimelineEventFrom(event timeline.Event) canonicalTimelineEvent {
	output := canonicalTimelineEvent{ID: event.ID, Time: event.Time.UTC().Format(time.RFC3339Nano), Kind: event.Kind, Environment: event.Environment, Service: event.Service, RelationType: event.RelationType, SubjectType: event.Subject.Type, SubjectID: event.Subject.ID, SourceKind: event.Source.Kind, SourceObservedAt: event.Source.ObservedAt.UTC().Format(time.RFC3339Nano), ConfidenceLevel: event.Confidence.Level, ConfidenceBasis: event.Confidence.Basis, ConcurrencyDetected: event.Concurrency.Detected, ConcurrencyCount: event.Concurrency.Count, Limitations: uniqueSorted(event.Limitations)}
	if event.Deployment != nil {
		output.Deployment = &canonicalTimelineDeployment{Status: event.Deployment.Status, Strategy: event.Deployment.Strategy, ProvenanceStatus: event.Deployment.ProvenanceStatus, ProvenanceConfidenceLevel: event.Deployment.ProvenanceConfidence.Level, ProvenanceConfidenceBasis: event.Deployment.ProvenanceConfidence.Basis}
	}
	if event.Runtime != nil {
		output.Runtime = &canonicalRuntime{State: event.Runtime.State, Health: event.Runtime.Health, RestartCount: event.Runtime.RestartCount}
	}
	return output
}

func canonicalNodeCompare(left, right canonicalNode) int {
	return cmp.Or(cmp.Compare(left.Type, right.Type), cmp.Compare(left.LogicalKey, right.LogicalKey), cmp.Compare(left.DisplayName, right.DisplayName), cmp.Compare(left.ID, right.ID))
}

func canonicalEdgeCompare(left, right canonicalEdge) int {
	return cmp.Or(cmp.Compare(left.From, right.From), cmp.Compare(left.To, right.To), cmp.Compare(left.DependencyKind, right.DependencyKind), cmp.Compare(left.WindowStart, right.WindowStart), cmp.Compare(left.WindowEnd, right.WindowEnd), cmp.Compare(left.RequestCount, right.RequestCount), cmp.Compare(left.ErrorCount, right.ErrorCount), cmp.Compare(left.DurationSumNS, right.DurationSumNS), cmp.Compare(left.Confidence, right.Confidence), cmp.Compare(left.Basis, right.Basis), cmp.Compare(left.AlgorithmVersion, right.AlgorithmVersion), cmp.Compare(strings.Join(left.EvidenceIDs, "\x00"), strings.Join(right.EvidenceIDs, "\x00")), cmp.Compare(strings.Join(left.Limitations, "\x00"), strings.Join(right.Limitations, "\x00")), cmp.Compare(left.ID, right.ID))
}

func canonicalTimelineEventCompare(left, right canonicalTimelineEvent) int {
	if left.Time != right.Time {
		return -cmp.Compare(left.Time, right.Time)
	}
	return cmp.Or(cmp.Compare(timeline.Priority(left.Kind), timeline.Priority(right.Kind)), cmp.Compare(canonicalTimelineEventString(left), canonicalTimelineEventString(right)))
}

func canonicalTimelineEventString(event canonicalTimelineEvent) string {
	payload, err := json.Marshal(event)
	if err != nil {
		return ""
	}
	return string(payload)
}
