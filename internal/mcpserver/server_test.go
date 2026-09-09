package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/guijoazeiro/prodmap/internal/baseline"
	"github.com/guijoazeiro/prodmap/internal/deployment"
	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/regression"
	"github.com/guijoazeiro/prodmap/internal/timeline"
	"github.com/guijoazeiro/prodmap/internal/topology"
)

func TestServerHandshakeSchemaAndSuccessfulToolCall(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	reader := fixtureReader{
		input:    fixtureInput(now),
		roots:    []topology.Node{{ID: "service-payment", Type: "service", LogicalKey: "payment-api", DisplayName: "payment-api"}},
		nodes:    []topology.Node{{ID: "service-payment", Type: "service", LogicalKey: "payment-api", DisplayName: "payment-api"}, {ID: "service-checkout", Type: "service", LogicalKey: "checkout-api", DisplayName: "checkout-api"}},
		edges:    []topology.Edge{{ID: "edge-1", From: "service-payment", To: "service-checkout", DependencyKind: "service", EvidenceIDs: []string{"evidence-1"}}},
		timeline: timeline.Result{Items: []timeline.Event{{ID: "event-1", Time: now.Add(-time.Minute), Kind: "deployment_running", Environment: "reference", Service: "payment-api", Subject: timeline.Subject{Type: "deployment", ID: validDeploymentID}, Source: timeline.Source{Kind: "ledger", ObservedAt: now.Add(-time.Minute)}, Concurrency: timeline.Concurrency{Detected: false, Count: 0}, Limitations: []string{}}}},
	}
	calls := 0
	session := connect(t, reader, func() time.Time { calls++; return now })
	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 2 || tools.Tools[0].Name != InvestigateDeploymentToolName || tools.Tools[1].Name != ListDeploymentsToolName || !strings.Contains(tools.Tools[0].Description, "bounded, read-only") {
		t.Fatalf("tools=%+v", tools.Tools)
	}
	inputSchema, ok := tools.Tools[0].InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("input schema type=%T", tools.Tools[0].InputSchema)
	}
	properties := inputSchema["properties"].(map[string]any)
	for _, prohibited := range []string{"project_dir", "data_dir", "path", "url", "token"} {
		if _, exists := properties[prohibited]; exists {
			t.Fatalf("prohibited input property %q", prohibited)
		}
	}
	metricSchema := properties["metric"].(map[string]any)
	if values := metricSchema["enum"].([]any); len(values) != 5 {
		t.Fatalf("metric enum=%v", values)
	}
	if tools.Tools[0].OutputSchema == nil {
		t.Fatal("missing typed output schema")
	}

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolName, Arguments: validArguments()})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || result.StructuredContent == nil || len(result.Content) != 1 {
		t.Fatalf("result=%+v", result)
	}
	structured, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content type=%T", result.Content[0])
	}
	var structuredValue, textualValue any
	if err := json.Unmarshal(structured, &structuredValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(text.Text), &textualValue); err != nil {
		t.Fatal(err)
	}
	structuredJSON, _ := json.Marshal(structuredValue)
	textualJSON, _ := json.Marshal(textualValue)
	if string(structuredJSON) != string(textualJSON) {
		t.Fatalf("structured=%s text=%s", structuredJSON, textualJSON)
	}
	output := structuredValue.(map[string]any)
	if output["status"] != "AVAILABLE" || output["causality_claimed"] != false || output["regression"].(map[string]any)["classification"].(map[string]any)["result"] != "CANDIDATE" || calls != 1 || len(output["topology"].(map[string]any)["edges"].([]any)) != 1 || len(output["timeline"].(map[string]any)["items"].([]any)) != 1 {
		t.Fatalf("output=%#v", output)
	}
	assertNoSecrets(t, string(structured))
}

func TestServerRejectsInvalidAndUnknownInput(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	session := connect(t, fixtureReader{input: fixtureInput(now)}, func() time.Time { return now })
	for name, arguments := range map[string]map[string]any{
		"missing deployment": {"metric": "latency_p95"},
		"invalid enum":       {"deployment": validDeploymentID, "metric": "other"},
		"invalid duration":   {"deployment": validDeploymentID, "metric": "latency_p95", "before": "1s"},
		"invalid samples":    {"deployment": validDeploymentID, "metric": "latency_p95", "min_samples": 0},
		"invalid coverage":   {"deployment": validDeploymentID, "metric": "latency_p95", "min_coverage": 1.1},
		"unknown field":      {"deployment": validDeploymentID, "metric": "latency_p95", "project_dir": "/tmp"},
	} {
		t.Run(name, func(t *testing.T) {
			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolName, Arguments: arguments})
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || len(result.Content) == 0 {
				t.Fatalf("result=%+v", result)
			}
			text := result.Content[0].(*mcp.TextContent).Text
			if strings.Contains(text, "/tmp") || strings.Contains(strings.ToLower(text), "sqlite") {
				t.Fatalf("unsanitized error=%q", text)
			}
		})
	}
}

func TestServerUnknownAndNoSignalRemainConservative(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	for name, input := range map[string]regression.Input{
		"unknown": func() regression.Input { value := fixtureInput(now); value.Before = []baseline.Window{}; return value }(),
		"no signal": func() regression.Input {
			value := fixtureInput(now)
			value.After[0].P50NS, value.After[0].P95NS = 299_999_999, 299_999_999
			return value
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			session := connect(t, fixtureReader{input: input}, func() time.Time { return now })
			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolName, Arguments: validArguments()})
			if err != nil || result.IsError {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			data, _ := json.Marshal(result.StructuredContent)
			var output map[string]any
			if err := json.Unmarshal(data, &output); err != nil {
				t.Fatal(err)
			}
			classification := output["regression"].(map[string]any)["classification"].(map[string]any)
			if got := classification["result"]; got != map[string]string{"unknown": "UNKNOWN", "no signal": "NO_SIGNAL"}[name] {
				t.Fatalf("classification=%#v", classification)
			}
		})
	}
}

func TestServerAllowsAuthenticationCapabilityServiceNames(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	reader := fixtureReader{
		input: fixtureInput(now),
		roots: []topology.Node{{ID: "service-payment", Type: "service", LogicalKey: "payment-api", DisplayName: "payment-api"}},
		nodes: []topology.Node{
			{ID: "service-payment", Type: "service", LogicalKey: "payment-api", DisplayName: "payment-api"},
			{ID: "service-token", Type: "service", LogicalKey: "token-service", DisplayName: "token-service"},
		},
		edges: []topology.Edge{{ID: "edge-token", From: "service-payment", To: "service-token", DependencyKind: "service", EvidenceIDs: []string{"evidence-token"}, Limitations: []string{}}},
	}
	session := connect(t, reader, func() time.Time { return now })
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolName, Arguments: validArguments()})
	if err != nil || result.IsError {
		t.Fatalf("tool result=%+v err=%v", result, err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil || !strings.Contains(string(encoded), "token-service") || strings.Contains(string(encoded), "fake-sensitive-value") {
		t.Fatalf("structured output=%s err=%v", encoded, err)
	}
}

func TestServerMapsNotFoundAndCancellationWithoutDetails(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	reader := fixtureReader{input: fixtureInput(now), comparisonErr: errs.ErrNotFound}
	session := connect(t, reader, func() time.Time { return now })
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolName, Arguments: validArguments()})
	if err != nil || !result.IsError || result.Content[0].(*mcp.TextContent).Text != "NOT_FOUND" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err = handle(ctx, fixtureConfig(fixtureReader{input: fixtureInput(now)}, func() time.Time { return now }), Input{Deployment: validDeploymentID, Metric: "latency_p95"})
	if err == nil || err.Error() != "SOURCE_UNAVAILABLE" {
		t.Fatalf("err=%v", err)
	}
	deadline, cancel := context.WithTimeout(t.Context(), 5*time.Millisecond)
	defer cancel()
	_, _, err = handle(deadline, fixtureConfig(fixtureReader{input: fixtureInput(now), wait: true}, func() time.Time { return now }), Input{Deployment: validDeploymentID, Metric: "latency_p95"})
	if err == nil || err.Error() != "SOURCE_UNAVAILABLE" {
		t.Fatalf("deadline err=%v", err)
	}
}

func TestServerCreatesAndClosesOneSnapshotPerToolCall(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	reader := fixtureReader{input: fixtureInput(now)}
	var mu sync.Mutex
	opened, closed := 0, 0
	config := Config{
		OpenReader: func(context.Context) (Reader, func() error, error) {
			mu.Lock()
			opened++
			mu.Unlock()
			return reader, func() error {
				mu.Lock()
				closed++
				mu.Unlock()
				return nil
			}, nil
		},
		Now: func() time.Time { return now },
	}
	session := connectConfig(t, config)
	if _, err := session.ListTools(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if opened != 0 || closed != 0 {
		mu.Unlock()
		t.Fatalf("handshake/tools-list opened snapshots: opened=%d closed=%d", opened, closed)
	}
	mu.Unlock()
	for range 2 {
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolName, Arguments: validArguments()})
		if err != nil || result.IsError {
			t.Fatalf("tool call result=%+v err=%v", result, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if opened != 2 || closed != 2 {
		t.Fatalf("snapshots opened=%d closed=%d, want 2 each", opened, closed)
	}
}

func TestServerClosesSnapshotAfterComposeErrorAndCancellation(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	for name, ctx := range map[string]context.Context{
		"compose error": t.Context(),
		"cancelled":     func() context.Context { value, cancel := context.WithCancel(t.Context()); cancel(); return value }(),
	} {
		t.Run(name, func(t *testing.T) {
			closed := 0
			reader := fixtureReader{input: fixtureInput(now), comparisonErr: errs.ErrNotFound}
			config := Config{
				OpenReader: func(context.Context) (Reader, func() error, error) {
					return reader, func() error { closed++; return nil }, nil
				},
				Now: func() time.Time { return now },
			}
			_, _, err := handle(ctx, config, Input{Deployment: validDeploymentID, Metric: "latency_p95"})
			if err == nil || closed != 1 {
				t.Fatalf("error=%v closed=%d", err, closed)
			}
		})
	}
}

func TestServerRejectsOversizeOutputAndConcurrentCalls(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	large := fixtureInput(now)
	large.Before[0].ID = strings.Repeat("a", maxResponseBytes)
	_, _, err := handle(t.Context(), fixtureConfig(fixtureReader{input: large}, func() time.Time { return now }), Input{Deployment: validDeploymentID, Metric: "latency_p95"})
	if err == nil || err.Error() != "INCOMPATIBLE_SCHEMA" {
		t.Fatalf("oversize err=%v", err)
	}
	session := connect(t, fixtureReader{input: fixtureInput(now), discovery: deployment.DiscoveryResult{Items: []deployment.DiscoveryItem{{ID: validDeploymentID, Environment: "reference", Service: "payment-api", Status: "running", Strategy: "unknown", StartedAt: now.Add(-time.Minute), Provenance: deployment.DiscoveryProvenance{Status: "UNKNOWN", Confidence: "UNKNOWN", Basis: "fixture", Limitations: []string{}}}}}}, func() time.Time { return now })
	var group sync.WaitGroup
	errs := make(chan error, 16)
	for index := range 16 {
		group.Go(func() {
			name, arguments := InvestigateDeploymentToolName, validArguments()
			if index%2 == 1 {
				name, arguments = ListDeploymentsToolName, map[string]any{"limit": 1}
			}
			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: arguments})
			if err != nil {
				errs <- err
				return
			}
			if result.IsError {
				errs <- errors.New("tool error")
			}
		})
	}
	group.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestServerHasNoResourcesOrPrompts(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	session := connect(t, fixtureReader{input: fixtureInput(now)}, func() time.Time { return now })
	resources, err := session.ListResources(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	prompts, err := session.ListPrompts(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resources.Resources) != 0 || len(prompts.Prompts) != 0 {
		t.Fatalf("resources=%+v prompts=%+v", resources.Resources, prompts.Prompts)
	}
}

const validDeploymentID = "01a05d48-09b3-742b-8d9b-54f79d43b28f"

func validArguments() map[string]any {
	return map[string]any{"deployment": validDeploymentID, "metric": "latency_p95", "before": "5m", "after": "5m", "min_samples": 1, "min_coverage": 0.8}
}

func connect(t *testing.T, reader Reader, now func() time.Time) *mcp.ClientSession {
	t.Helper()
	return connectConfig(t, fixtureConfig(reader, now))
}

func connectConfig(t *testing.T, config Config) *mcp.ClientSession {
	t.Helper()
	server, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "prodmap-test", Version: "v1"}, nil)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	return clientSession
}

func fixtureConfig(reader Reader, now func() time.Time) Config {
	return Config{
		OpenReader: func(context.Context) (Reader, func() error, error) {
			return reader, func() error { return nil }, nil
		},
		Now: now,
	}
}

type fixtureReader struct {
	input         regression.Input
	comparisonErr error
	wait          bool
	discovery     deployment.DiscoveryResult
	roots, nodes  []topology.Node
	edges         []topology.Edge
	timeline      timeline.Result
}

func (r fixtureReader) Comparison(ctx context.Context, query regression.Query) (regression.Input, error) {
	if r.wait {
		<-ctx.Done()
		return regression.Input{}, ctx.Err()
	}
	input := r.input
	input.Query = query
	return input, r.comparisonErr
}

func (r fixtureReader) DiscoverDeployments(_ context.Context, _ deployment.DiscoveryQuery) (deployment.DiscoveryResult, error) {
	if r.discovery.Items == nil {
		return deployment.DiscoveryResult{Items: []deployment.DiscoveryItem{}}, nil
	}
	return r.discovery, nil
}

func TestListDeploymentsUsesClosedSchemaAndAllowlistedOutput(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	started := now.Add(-time.Hour)
	reader := fixtureReader{input: fixtureInput(now), discovery: deployment.DiscoveryResult{Items: []deployment.DiscoveryItem{{ID: validDeploymentID, Environment: "reference", Service: "token-service", Status: "succeeded", Strategy: "rolling", StartedAt: started, Provenance: deployment.DiscoveryProvenance{Status: "MATCHED", Confidence: "HIGH", Basis: "immutable deployment record", Limitations: []string{}}}}}}
	session := connect(t, reader, func() time.Time { return now })
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ListDeploymentsToolName, Arguments: map[string]any{"limit": 1}})
	if err != nil || result.IsError {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatal(err)
	}
	if output["schema_version"] != DiscoveryVersion || output["generated_at"] != now.Format(time.RFC3339Nano) || len(output["items"].([]any)) != 1 || output["next_cursor"] != nil {
		t.Fatalf("output=%#v", output)
	}
	if strings.Contains(string(encoded), "external_id") || !strings.Contains(string(encoded), "token-service") {
		t.Fatalf("output=%s", encoded)
	}
}

func TestListDeploymentsRejectsInputBeforeSnapshot(t *testing.T) {
	opened := 0
	server, err := New(Config{Now: time.Now, OpenReader: func(context.Context) (Reader, func() error, error) {
		opened++
		return fixtureReader{}, func() error { return nil }, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v1"}, nil)
	session, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	for _, arguments := range []map[string]any{{"environment": ""}, {"limit": 101}, {"status": "invalid"}, {"cursor": ""}, {"path": "/tmp"}} {
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ListDeploymentsToolName, Arguments: arguments})
		if err != nil || !result.IsError {
			t.Fatalf("arguments=%v result=%+v err=%v", arguments, result, err)
		}
	}
	if opened != 0 {
		t.Fatalf("invalid list input opened %d snapshots", opened)
	}
}
func (r fixtureReader) ResolveGraphRoots(_ context.Context, _, _ string, _ bool) ([]topology.Node, error) {
	return r.roots, nil
}
func (r fixtureReader) ObservedGraph(_ context.Context, _ string, at time.Time, _ topology.Confidence, _ []string, _ int) ([]topology.Node, []topology.Edge, bool, error) {
	edges := slices.Clone(r.edges)
	for index := range edges {
		if edges[index].WindowStart.IsZero() {
			edges[index].WindowStart = at.Add(-5 * time.Minute)
			edges[index].WindowEnd = at
		}
		if edges[index].Confidence == "" {
			edges[index].Confidence = topology.Low
		}
		if edges[index].Basis == "" {
			edges[index].Basis = "fixture observed relation"
		}
		if edges[index].AlgorithmVersion == "" {
			edges[index].AlgorithmVersion = topology.AlgorithmVersion
		}
	}
	return r.nodes, edges, false, nil
}
func (r fixtureReader) Timeline(_ context.Context, query timeline.Query) (timeline.Result, error) {
	if r.timeline.Items == nil {
		return timeline.Result{Since: query.Since, Until: query.Until, Environment: query.Environment, Items: []timeline.Event{}}, nil
	}
	result := r.timeline
	result.Since, result.Until, result.Environment = query.Since, query.Until, query.Environment
	result.Items = slices.Clone(r.timeline.Items)
	for index := range result.Items {
		event := &result.Items[index]
		if event.Time.Before(query.Since) || !event.Time.Before(query.Until) {
			event.Time = query.Since
		}
		if event.Source.ObservedAt.IsZero() {
			event.Source.ObservedAt = event.Time
		}
		if event.Confidence.Level == "" {
			event.Confidence = timeline.Confidence{Level: "HIGH", Basis: "fixture source"}
		}
		if event.Kind == "runtime_observed" {
			event.RelationType, event.Subject.Type, event.Source.Kind = "OBSERVED", "runtime_instance", "docker"
			if event.Runtime == nil {
				event.Runtime = &timeline.Runtime{State: "running", Health: "healthy"}
			}
			continue
		}
		event.RelationType, event.Subject.Type, event.Source.Kind = "DECLARED", "deployment", "deployment_ledger"
		if event.Deployment == nil {
			event.Deployment = &timeline.Deployment{Status: "running", Strategy: "unknown", ProvenanceStatus: "MATCHED", ProvenanceConfidence: timeline.Confidence{Level: "LOW", Basis: "fixture ledger"}}
		}
	}
	return result, nil
}

func fixtureInput(now time.Time) regression.Input {
	deploymentAt := now.Add(-30 * time.Minute)
	coverage := 0.9
	return regression.Input{
		Query:         regression.Query{DeploymentID: validDeploymentID, Metric: baseline.LatencyP95, Before: 5 * time.Minute, After: 5 * time.Minute, MinSamples: 1},
		Deployment:    regression.Deployment{ID: validDeploymentID, Environment: "reference", Service: "payment-api", StartedAt: deploymentAt},
		Target:        baseline.Target{Kind: baseline.ServiceTarget, ID: "service-payment", Service: "payment-api"},
		Before:        []baseline.Window{{ID: "before-window", Start: deploymentAt.Add(-5 * time.Minute), End: deploymentAt, ObservedAt: now.Add(-time.Minute), RequestCount: 10, P50NS: 250_000_000, P95NS: 250_000_000, P99NS: 250_000_000, CoverageRatio: &coverage, IsComplete: true}},
		After:         []baseline.Window{{ID: "after-window", Start: deploymentAt, End: deploymentAt.Add(5 * time.Minute), ObservedAt: now.Add(-time.Minute), RequestCount: 10, P50NS: 300_000_000, P95NS: 300_000_000, P99NS: 300_000_000, CoverageRatio: &coverage, IsComplete: true}},
		Contamination: regression.Contamination{BeforeDeployments: []string{}, AfterDeployments: []string{}, ConcurrentDeployments: []string{}},
	}
}

func assertNoSecrets(t *testing.T, value string) {
	t.Helper()
	for _, forbidden := range []string{"external_id", "git_head", "vcs_revision", "image_reference", "artifact_identity", "Authorization", "Bearer", "token", "/tmp/"} {
		if strings.Contains(value, forbidden) {
			t.Fatalf("forbidden value %q in output", forbidden)
		}
	}
}
