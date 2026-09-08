package investigation

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/baseline"
	"github.com/guijoazeiro/prodmap/internal/regression"
	"github.com/guijoazeiro/prodmap/internal/timeline"
	"github.com/guijoazeiro/prodmap/internal/topology"
)

func TestComposeAvailableCandidateIsDeterministicAndReferencesEvidence(t *testing.T) {
	generatedAt := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	reader := investigationReader{input: investigationInput(generatedAt)}
	reader.roots = []topology.Node{{ID: "service-payment", Type: "service", LogicalKey: "payment-api", DisplayName: "payment-api"}}
	reader.nodes = []topology.Node{reader.roots[0], {ID: "service-checkout", Type: "service", LogicalKey: "checkout-api", DisplayName: "checkout-api"}}
	reader.edges = []topology.Edge{{ID: "edge-1", From: "service-payment", To: "service-checkout", DependencyKind: "service", EvidenceIDs: []string{"evidence-2", "evidence-1", "evidence-1"}, Limitations: []string{"edge limitation"}}}
	reader.timeline = timeline.Result{Items: []timeline.Event{{ID: "event-2", Limitations: []string{"timeline limitation"}}, {ID: "event-1"}}}
	query := Query{Comparison: reader.input.Query, GeneratedAt: generatedAt}

	first, err := Compose(t.Context(), reader, query)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Compose(t.Context(), reader, query)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "AVAILABLE" || first.Regression.Classification == nil || first.Regression.Classification.Result != "CANDIDATE" {
		t.Fatalf("result=%+v", first)
	}
	if first.InvestigationVersion != Version || first.InvestigationKey == "" || first.InvestigationKey != second.InvestigationKey {
		t.Fatalf("keys first=%q second=%q", first.InvestigationKey, second.InvestigationKey)
	}
	wantReferences := EvidenceReferences{AcceptedWindowIDs: []string{"after-window", "before-window"}, RejectedWindowIDs: []string{}, GraphEvidenceIDs: []string{"evidence-1", "evidence-2"}, TimelineEventIDs: []string{"event-1", "event-2"}}
	if !reflect.DeepEqual(first.EvidenceReferences, wantReferences) {
		t.Fatalf("references=%+v want=%+v", first.EvidenceReferences, wantReferences)
	}
	if first.CausalityClaimed || contains(first.Limitations, "no observed topology relations matched the deployment service and time") {
		t.Fatalf("limitations=%v causality=%t", first.Limitations, first.CausalityClaimed)
	}
}

func TestComposeUnknownAndEmptyReadModelsAreNotErrors(t *testing.T) {
	generatedAt := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	input := investigationInput(generatedAt)
	input.Before = []baseline.Window{}
	reader := investigationReader{input: input, noRoots: true, timeline: timeline.Result{Items: []timeline.Event{}}}
	result, err := Compose(t.Context(), reader, Query{Comparison: input.Query, GeneratedAt: generatedAt})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "UNKNOWN" || result.Regression.Classification == nil || result.Regression.Classification.Confidence.Level != "UNKNOWN" {
		t.Fatalf("result=%+v", result)
	}
	if result.Topology.Nodes == nil || result.Topology.Edges == nil || result.Timeline.Items == nil || result.EvidenceReferences.AcceptedWindowIDs == nil || result.EvidenceReferences.RejectedWindowIDs == nil || result.EvidenceReferences.GraphEvidenceIDs == nil || result.EvidenceReferences.TimelineEventIDs == nil {
		t.Fatalf("nil collection result=%+v", result)
	}
	if !contains(result.Limitations, "no observed topology relations matched the deployment service and time") || !contains(result.Limitations, "no timeline events matched the investigation interval") {
		t.Fatalf("limitations=%v", result.Limitations)
	}
}

func TestInvestigationKeyCapturesCanonicalTopologyAndTimeline(t *testing.T) {
	generatedAt := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	reader := investigationReader{input: investigationInput(generatedAt)}
	reader.roots = []topology.Node{{ID: "service-payment", Type: "service", LogicalKey: "payment-api", DisplayName: "payment-api"}}
	reader.nodes = []topology.Node{
		reader.roots[0],
		{ID: "service-checkout", Type: "service", LogicalKey: "checkout-api", DisplayName: "checkout-api"},
		{ID: "dependency-postgres", Type: "dependency", LogicalKey: "postgres", DisplayName: "postgres"},
	}
	reader.edges = []topology.Edge{
		{ID: "edge-service", From: "service-payment", To: "service-checkout", DependencyKind: "service", WindowStart: generatedAt.Add(-20 * time.Minute), WindowEnd: generatedAt.Add(-15 * time.Minute), RequestCount: 10, ErrorCount: 1, DurationSumNS: 100, Confidence: topology.Medium, Basis: "matched span", AlgorithmVersion: topology.AlgorithmVersion, EvidenceIDs: []string{"edge-evidence-b", "edge-evidence-a"}, Limitations: []string{"edge limitation"}},
		{ID: "edge-database", From: "service-payment", To: "dependency-postgres", DependencyKind: "database", WindowStart: generatedAt.Add(-20 * time.Minute), WindowEnd: generatedAt.Add(-15 * time.Minute), RequestCount: 5, ErrorCount: 0, DurationSumNS: 50, Confidence: topology.Low, Basis: "semantic target", AlgorithmVersion: topology.AlgorithmVersion, EvidenceIDs: []string{"database-evidence"}, Limitations: []string{}},
	}
	reader.timeline = timeline.Result{Since: generatedAt.Add(-30 * time.Minute), Until: generatedAt.Add(-10 * time.Minute), Environment: "reference", NextCursor: "opaque-next-page", Items: []timeline.Event{
		{ID: "event-runtime", Time: generatedAt.Add(-12 * time.Minute), Kind: "runtime_observed", Environment: "reference", Service: "payment-api", RelationType: "runtime", Subject: timeline.Subject{Type: "runtime", ID: "runtime-payment"}, Source: timeline.Source{Kind: "docker", ObservedAt: generatedAt.Add(-11 * time.Minute)}, Confidence: timeline.Confidence{Level: "LOW", Basis: "observed runtime"}, Runtime: &timeline.Runtime{State: "running", Health: "healthy", RestartCount: 1, ArtifactIdentity: "sha256:must-not-enter-key"}, Concurrency: timeline.Concurrency{Detected: true, Count: 2}, Limitations: []string{"runtime limitation"}},
		{ID: "event-deployment", Time: generatedAt.Add(-15 * time.Minute), Kind: "deployment_running", Environment: "reference", Service: "payment-api", RelationType: "deployment", Subject: timeline.Subject{Type: "deployment", ID: "deployment-payment"}, Source: timeline.Source{Kind: "ledger", ObservedAt: generatedAt.Add(-14 * time.Minute)}, Confidence: timeline.Confidence{Level: "LOW", Basis: "declared deployment"}, Deployment: &timeline.Deployment{Status: "running", Strategy: "unknown", ProvenanceStatus: "MATCHED", ProvenanceConfidence: timeline.Confidence{Level: "LOW", Basis: "verified ledger"}}, Limitations: []string{"deployment limitation"}},
	}}

	first, err := Compose(t.Context(), reader, Query{Comparison: reader.input.Query, GeneratedAt: generatedAt})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Compose(t.Context(), reader, Query{Comparison: reader.input.Query, GeneratedAt: generatedAt.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if first.InvestigationKey != second.InvestigationKey {
		t.Fatalf("generated_at/freshness-only change altered key: first=%q second=%q", first.InvestigationKey, second.InvestigationKey)
	}

	reordered := cloneInvestigationResult(first)
	slices.Reverse(reordered.Topology.Nodes)
	slices.Reverse(reordered.Topology.Edges)
	slices.Reverse(reordered.Timeline.Items)
	slices.Reverse(reordered.EvidenceReferences.GraphEvidenceIDs)
	slices.Reverse(reordered.Limitations)
	if got := investigationKey(t, reordered); got != first.InvestigationKey {
		t.Fatalf("irrelevant collection order altered key: got=%q want=%q", got, first.InvestigationKey)
	}

	for name, mutate := range map[string]func(*Result){
		"edge identity":   func(result *Result) { result.Topology.Edges[0].ID = "different-edge" },
		"edge count":      func(result *Result) { result.Topology.Edges[0].RequestCount++ },
		"edge confidence": func(result *Result) { result.Topology.Edges[0].Confidence = topology.High },
		"timeline event":  func(result *Result) { result.Timeline.Items[0].Kind = "deployment_failed" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := cloneInvestigationResult(first)
			mutate(&changed)
			if got := investigationKey(t, changed); got == first.InvestigationKey {
				t.Fatal("semantic investigation content did not alter the key")
			}
		})
	}
}

func investigationKey(t *testing.T, result Result) string {
	t.Helper()
	value, err := CalculateKey(result)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func cloneInvestigationResult(result Result) Result {
	clone := result
	clone.Topology.Roots = slices.Clone(result.Topology.Roots)
	clone.Topology.Nodes = slices.Clone(result.Topology.Nodes)
	clone.Topology.Edges = slices.Clone(result.Topology.Edges)
	for index := range clone.Topology.Edges {
		clone.Topology.Edges[index].EvidenceIDs = slices.Clone(result.Topology.Edges[index].EvidenceIDs)
		clone.Topology.Edges[index].Limitations = slices.Clone(result.Topology.Edges[index].Limitations)
	}
	clone.Timeline.Items = slices.Clone(result.Timeline.Items)
	for index := range clone.Timeline.Items {
		clone.Timeline.Items[index].Limitations = slices.Clone(result.Timeline.Items[index].Limitations)
	}
	clone.EvidenceReferences = EvidenceReferences{AcceptedWindowIDs: slices.Clone(result.EvidenceReferences.AcceptedWindowIDs), RejectedWindowIDs: slices.Clone(result.EvidenceReferences.RejectedWindowIDs), GraphEvidenceIDs: slices.Clone(result.EvidenceReferences.GraphEvidenceIDs), TimelineEventIDs: slices.Clone(result.EvidenceReferences.TimelineEventIDs)}
	clone.Limitations = slices.Clone(result.Limitations)
	return clone
}

type investigationReader struct {
	input        regression.Input
	roots, nodes []topology.Node
	edges        []topology.Edge
	timeline     timeline.Result
	noRoots      bool
}

func (r investigationReader) Comparison(_ context.Context, _ regression.Query) (regression.Input, error) {
	return r.input, nil
}

func (r investigationReader) ResolveGraphRoots(_ context.Context, _, _ string, _ bool) ([]topology.Node, error) {
	if r.noRoots {
		return []topology.Node{}, nil
	}
	return r.roots, nil
}

func (r investigationReader) ObservedGraph(_ context.Context, _ string, _ time.Time, _ topology.Confidence, _ []string, _ int) ([]topology.Node, []topology.Edge, bool, error) {
	return r.nodes, r.edges, false, nil
}

func (r investigationReader) Timeline(_ context.Context, _ timeline.Query) (timeline.Result, error) {
	return r.timeline, nil
}

func investigationInput(generatedAt time.Time) regression.Input {
	deploymentAt := generatedAt.Add(-30 * time.Minute)
	beforeStart := deploymentAt.Add(-5 * time.Minute)
	afterEnd := deploymentAt.Add(5 * time.Minute)
	before, after := 250_000_000, 300_000_000
	return regression.Input{
		Query:         regression.Query{DeploymentID: "01a05d48-09b3-742b-8d9b-54f79d43b28f", Metric: baseline.LatencyP95, Before: 5 * time.Minute, After: 5 * time.Minute, MinSamples: 1},
		Deployment:    regression.Deployment{ID: "01a05d48-09b3-742b-8d9b-54f79d43b28f", Environment: "reference", Service: "payment-api", StartedAt: deploymentAt},
		Target:        baseline.Target{Kind: baseline.ServiceTarget, ID: "service-payment", Service: "payment-api"},
		Before:        []baseline.Window{{ID: "before-window", Start: beforeStart, End: deploymentAt, ObservedAt: generatedAt.Add(-time.Minute), RequestCount: 10, P50NS: int64(before), P95NS: int64(before), P99NS: int64(before)}},
		After:         []baseline.Window{{ID: "after-window", Start: deploymentAt, End: afterEnd, ObservedAt: generatedAt.Add(-time.Minute), RequestCount: 10, P50NS: int64(after), P95NS: int64(after), P99NS: int64(after)}},
		Contamination: regression.Contamination{BeforeDeployments: []string{}, AfterDeployments: []string{}, ConcurrentDeployments: []string{}},
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
