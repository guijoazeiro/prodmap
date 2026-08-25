package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/ptrace"
	collecttracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

var phase2AMatrixStart = time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

func TestPhase2ARealCLIScenarioMatrix(t *testing.T) {
	t.Run("database dependency without remote server", testPhase2ADatabaseCLI)
	t.Run("unobserved peer target is not a ghost service", testPhase2AUnobservedPeerCLI)
	t.Run("concrete path missing endpoint missing service client without target and sensitive attribute", testPhase2AMissingAndSensitiveCLI)
	t.Run("trace resources and lines out of order", testPhase2AOutOfOrderLinesCLI)
	t.Run("cyclic observed graph", testPhase2ACycleCLI)
	t.Run("LOW MEDIUM HIGH and exact filters", testPhase2AConfidenceCLI)
	t.Run("deterministic nearest-rank percentiles", testPhase2APercentilesCLI)
	t.Run("deterministic node truncation", testPhase2ATruncationCLI)
	t.Run("out-of-order windows and half-open temporal boundaries", testPhase2AWindowOrderAndBoundariesCLI)
	t.Run("content conflict on the same source and window", testPhase2AConflictCLI)
	t.Run("all ambiguity not found and root without relations", testPhase2AGraphSelectorsCLI)
	t.Run("invalid timestamp and oversized line are atomic", testPhase2AInvalidFilesCLI)
	t.Run("human warnings and json false stream contract", testPhase2AStreamContractCLI)
	t.Run("telemetry target resolution is temporal", testPhase2ATemporalTargetCLI)
	t.Run("producer consumer SpanLink persistence and replay", testPhase2ASpanLinkCLI)
}

func testPhase2ASpanLinkCLI(t *testing.T) {
	run := func(t *testing.T, reverse bool) string {
		project := t.TempDir()
		producer := span(40, 2, 0, tracepb.Span_SPAN_KIND_PRODUCER, 10*time.Second, time.Millisecond)
		consumer := span(41, 3, 0, tracepb.Span_SPAN_KIND_CONSUMER, 11*time.Second, time.Millisecond)
		consumer.Links = []*tracepb.Span_Link{{
			TraceId: producer.TraceId, SpanId: producer.SpanId,
			Attributes: []*commonpb.KeyValue{{Key: "authorization", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "Bearer matrix-link-secret"}}}},
		}}
		publisher, worker := resourceSpans("publisher", "", producer), resourceSpans("worker", "", consumer)
		resources := []*tracepb.ResourceSpans{publisher, worker}
		if reverse {
			resources[0], resources[1] = resources[1], resources[0]
		}
		input := writeOTLPFixture(t, project, "span-links.otlp.jsonl", otlpLine(t, resources...))
		app, stdout, stderr := testApp(project)
		first := ingestFixture(t, app, stdout, stderr, project, input, phase2AMatrixStart, phase2AMatrixStart.Add(time.Minute))
		second := ingestFixture(t, app, stdout, stderr, project, input, phase2AMatrixStart, phase2AMatrixStart.Add(time.Minute))
		if first["data"].(map[string]any)["ingestion_id"] != second["data"].(map[string]any)["ingestion_id"] || second["data"].(map[string]any)["idempotent_replay"] != true {
			t.Fatalf("SpanLink replay first=%#v second=%#v", first, second)
		}
		graph := runGraphJSON(t, app, stdout, stderr, project, "publisher", phase2AMatrixStart.Add(30*time.Second), "low", 100)
		edges := graphEdges(t, graph)
		if len(edges) != 1 || confidenceLevel(edges[0]) != "HIGH" || edges[0]["confidence"].(map[string]any)["basis"] != "producer/consumer SpanLink association" || !graphTargetIs(t, graph, edges[0]["to"], "service", "worker") {
			t.Fatalf("SpanLink graph=%#v", graph)
		}
		db := openCLITestDatabase(t, project)
		defer db.Close()
		var producerClaims, consumerClaims int
		if err := db.QueryRow(`SELECT SUM(claim='outbound producer span'),SUM(claim='linked consumer span') FROM topology_evidence`).Scan(&producerClaims, &consumerClaims); err != nil {
			t.Fatal(err)
		}
		bytes, err := os.ReadFile(filepath.Join(project, ".prodmap", "prodmap.db"))
		if err != nil {
			t.Fatal(err)
		}
		if producerClaims != 1 || consumerClaims != 1 || strings.Contains(string(bytes), "matrix-link-secret") || strings.Contains(mustJSON(t, graph), "matrix-link-secret") {
			t.Fatalf("SpanLink claims=%d/%d or redaction failed", producerClaims, consumerClaims)
		}
		return confidenceLevel(edges[0]) + ":" + edges[0]["confidence"].(map[string]any)["basis"].(string) + ":worker"
	}
	forward, reverse := run(t, false), run(t, true)
	if forward != reverse {
		t.Fatalf("SpanLink semantics changed with resource order: %q != %q", forward, reverse)
	}
}

func testPhase2ATemporalTargetCLI(t *testing.T) {
	type semanticEdge struct {
		TargetType  string
		TargetKey   string
		Confidence  string
		Basis       string
		Limitations string
	}
	run := func(t *testing.T, resolvedFirst bool) semanticEdge {
		project := t.TempDir()
		t1Start, t1End := phase2AMatrixStart, phase2AMatrixStart.Add(time.Minute)
		t2Start, t2End := phase2AMatrixStart.Add(2*time.Minute), phase2AMatrixStart.Add(3*time.Minute)
		t1 := writeOTLPFixture(t, project, "t1-unresolved.otlp.jsonl", otlpLine(t,
			resourceSpans("checkout", "", span(30, 2, 0, tracepb.Span_SPAN_KIND_CLIENT, 10*time.Second, time.Millisecond, stringAttribute("peer.service", "payment"))),
		))
		t2 := writeOTLPFixture(t, project, "t2-resolved.otlp.jsonl", otlpLine(t,
			resourceSpans("checkout", "", span(31, 2, 0, tracepb.Span_SPAN_KIND_CLIENT, 2*time.Minute+10*time.Second, time.Millisecond, stringAttribute("peer.service", "payment"))),
			resourceSpans("payment", "", span(31, 3, 2, tracepb.Span_SPAN_KIND_SERVER, 2*time.Minute+10*time.Second+time.Nanosecond, time.Millisecond)),
		))
		app, stdout, stderr := testApp(project)
		var before map[string]any
		if resolvedFirst {
			ingestFixture(t, app, stdout, stderr, project, t2, t2Start, t2End)
			ingestFixture(t, app, stdout, stderr, project, t1, t1Start, t1End)
		} else {
			ingestFixture(t, app, stdout, stderr, project, t1, t1Start, t1End)
			before = runGraphJSON(t, app, stdout, stderr, project, "checkout", t1Start.Add(30*time.Second), "low", 100)
			ingestFixture(t, app, stdout, stderr, project, t2, t2Start, t2End)
		}
		after := runGraphJSON(t, app, stdout, stderr, project, "checkout", t1Start.Add(30*time.Second), "low", 100)
		if before != nil && !reflect.DeepEqual(before["data"], after["data"]) {
			t.Fatalf("T1 graph changed after T2 ingestion:\nbefore=%#v\nafter=%#v", before, after)
		}
		edges := graphEdges(t, after)
		nodes := after["data"].(map[string]any)["nodes"].([]any)
		if len(edges) != 1 || confidenceLevel(edges[0]) != "MEDIUM" || !strings.Contains(mustJSON(t, edges[0]["limitations"]), "observed target service") {
			t.Fatalf("historical unresolved graph=%#v", after)
		}
		targetID := edges[0]["to"]
		targetType, targetKey := "", ""
		for _, raw := range nodes {
			node := raw.(map[string]any)
			if node["id"] == targetID {
				targetType, targetKey = node["type"].(string), node["logical_key"].(string)
			}
		}
		if targetType != "dependency" || targetKey != "payment" {
			t.Fatalf("T1 target was retroactively resolved: %#v", after)
		}
		t2Graph := runGraphJSON(t, app, stdout, stderr, project, "checkout", t2Start.Add(30*time.Second), "low", 100)
		if edges := graphEdges(t, t2Graph); len(edges) != 1 || confidenceLevel(edges[0]) != "HIGH" || !graphTargetIs(t, t2Graph, edges[0]["to"], "service", "payment") {
			t.Fatalf("T2 resolved graph=%#v", t2Graph)
		}
		return semanticEdge{TargetType: targetType, TargetKey: targetKey, Confidence: confidenceLevel(edges[0]), Basis: edges[0]["confidence"].(map[string]any)["basis"].(string), Limitations: mustJSON(t, edges[0]["limitations"])}
	}
	forward := run(t, false)
	reverse := run(t, true)
	if !reflect.DeepEqual(forward, reverse) {
		t.Fatalf("historical graph depends on ingestion order: forward=%+v reverse=%+v", forward, reverse)
	}
}

func graphTargetIs(t *testing.T, graph map[string]any, targetID any, nodeType, logicalKey string) bool {
	t.Helper()
	for _, raw := range graph["data"].(map[string]any)["nodes"].([]any) {
		node := raw.(map[string]any)
		if node["id"] == targetID {
			return node["type"] == nodeType && node["logical_key"] == logicalKey
		}
	}
	return false
}

func testPhase2AUnobservedPeerCLI(t *testing.T) {
	project := t.TempDir()
	input := writeOTLPFixture(t, project, "peer.otlp.jsonl", otlpLine(t,
		resourceSpans("api", "", span(11, 1, 0, tracepb.Span_SPAN_KIND_CLIENT, time.Second, time.Millisecond,
			stringAttribute("peer.service", "remote-api"))),
	))
	app, stdout, stderr := testApp(project)
	ingestFixture(t, app, stdout, stderr, project, input, phase2AMatrixStart, phase2AMatrixStart.Add(time.Minute))
	graph := runGraphJSON(t, app, stdout, stderr, project, "api", phase2AMatrixStart.Add(2*time.Second), "low", 100)
	nodes := graph["data"].(map[string]any)["nodes"].([]any)
	foundDependency := false
	for _, raw := range nodes {
		node := raw.(map[string]any)
		foundDependency = foundDependency || (node["type"] == "dependency" && node["logical_key"] == "remote-api")
	}
	if len(nodes) != 2 || !foundDependency {
		t.Fatalf("unobserved peer graph=%#v", graph)
	}
	db := openCLITestDatabase(t, project)
	defer db.Close()
	var services, dependencies, targets int
	if err := db.QueryRow("SELECT COUNT(*) FROM services").Scan(&services); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM dependencies").Scan(&dependencies); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(target_service_id) FROM service_dependency_observations").Scan(&targets); err != nil {
		t.Fatal(err)
	}
	if services != 1 || dependencies != 1 || targets != 0 {
		t.Fatalf("services=%d dependencies=%d targets=%d", services, dependencies, targets)
	}
}

func testPhase2AStreamContractCLI(t *testing.T) {
	project := t.TempDir()
	input := writeOTLPFixture(t, project, "outside.otlp.jsonl", otlpLine(t,
		resourceSpans("api", "", span(90, 1, 0, tracepb.Span_SPAN_KIND_INTERNAL, -time.Second, time.Millisecond)),
	))
	app, stdout, stderr := testApp(project)
	args := ingestArgs(project, input, phase2AMatrixStart, phase2AMatrixStart.Add(time.Minute))
	args = args[:len(args)-1]
	if code := app.Run(context.Background(), args); code != 0 {
		t.Fatalf("human ingest exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "Telemetry ingestion") || !strings.Contains(stderr.String(), "warning:") || strings.Contains(stdout.String(), "warning:") {
		t.Fatalf("human streams stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), []string{"graph", "--json=false", "--project-dir", project}); code == 0 {
		t.Fatal("invalid graph unexpectedly succeeded")
	}
	if stdout.Len() != 0 || stderr.Len() == 0 || strings.Contains(stderr.String(), `"error"`) {
		t.Fatalf("--json=false streams stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func testPhase2ADatabaseCLI(t *testing.T) {
	project, input := t.TempDir(), ""
	input = writeOTLPFixture(t, project, "database.otlp.jsonl", otlpLine(t,
		resourceSpans("api", "", span(1, 1, 0, tracepb.Span_SPAN_KIND_CLIENT, time.Second, 10*time.Millisecond,
			stringAttribute("db.system.name", "postgresql"), stringAttribute("db.namespace", "orders"))),
	))
	app, stdout, stderr := testApp(project)
	ingestFixture(t, app, stdout, stderr, project, input, phase2AMatrixStart, phase2AMatrixStart.Add(time.Minute))
	graph := runGraphJSON(t, app, stdout, stderr, project, "api", phase2AMatrixStart.Add(2*time.Second), "low", 100)
	edges := graphEdges(t, graph)
	if len(edges) != 1 || edges[0]["dependency_kind"] != "database" || confidenceLevel(edges[0]) != "MEDIUM" {
		t.Fatalf("database graph=%#v", graph)
	}
	if strings.Contains(mustJSON(t, graph), `database\u0000`) {
		t.Fatalf("public dependency logical key contains internal framing: %#v", graph)
	}
}

func testPhase2AMissingAndSensitiveCLI(t *testing.T) {
	project := t.TempDir()
	input := writeOTLPFixture(t, project, "missing.otlp.jsonl", otlpLine(t,
		resourceSpans("api", "",
			span(2, 1, 0, tracepb.Span_SPAN_KIND_SERVER, time.Second, time.Millisecond,
				stringAttribute("url.path", "/customers/123456789"), stringAttribute("authorization", "Bearer matrix-secret")),
			span(3, 2, 0, tracepb.Span_SPAN_KIND_CLIENT, 2*time.Second, time.Millisecond)),
		resourceSpans("", "", span(4, 3, 0, tracepb.Span_SPAN_KIND_SERVER, 3*time.Second, time.Millisecond)),
	))
	app, stdout, stderr := testApp(project)
	ingest := ingestFixture(t, app, stdout, stderr, project, input, phase2AMatrixStart, phase2AMatrixStart.Add(time.Minute))
	data := ingest["data"].(map[string]any)
	if data["services"] != float64(1) || data["endpoints"] != float64(0) || data["dependencies"] != float64(0) || data["spans_ignored"] != float64(1) {
		t.Fatalf("missing-data ingest=%#v", ingest)
	}
	encoded := mustJSON(t, ingest)
	database, err := os.ReadFile(filepath.Join(project, ".prodmap", "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"matrix-secret", "authorization", "/customers/123456789", "url.path"} {
		if strings.Contains(encoded, forbidden) || strings.Contains(string(database), forbidden) {
			t.Fatalf("sensitive/concrete value %q escaped sanitization", forbidden)
		}
	}
}

func testPhase2AOutOfOrderLinesCLI(t *testing.T) {
	project := t.TempDir()
	remote := otlpLine(t, resourceSpans("payment", "", span(5, 3, 2, tracepb.Span_SPAN_KIND_SERVER, 3*time.Second, time.Millisecond,
		stringAttribute("http.request.method", "GET"), stringAttribute("http.route", "/pay/{id}"))))
	source := otlpLine(t, resourceSpans("checkout", "", span(5, 2, 0, tracepb.Span_SPAN_KIND_CLIENT, 2*time.Second, 2*time.Millisecond,
		stringAttribute("peer.service", "payment"))))
	input := writeOTLPFixture(t, project, "out-of-order.otlp.jsonl", append(append(remote, '\n'), source...))
	app, stdout, stderr := testApp(project)
	ingestFixture(t, app, stdout, stderr, project, input, phase2AMatrixStart, phase2AMatrixStart.Add(time.Minute))
	graph := runGraphJSON(t, app, stdout, stderr, project, "checkout", phase2AMatrixStart.Add(3*time.Second), "low", 100)
	if edges := graphEdges(t, graph); len(edges) != 1 || confidenceLevel(edges[0]) != "HIGH" {
		t.Fatalf("out-of-order graph=%#v", graph)
	}
}

func testPhase2ACycleCLI(t *testing.T) {
	project := t.TempDir()
	input := writeOTLPFixture(t, project, "cycle.otlp.jsonl", otlpLine(t,
		resourceSpans("a", "",
			span(6, 2, 0, tracepb.Span_SPAN_KIND_CLIENT, time.Second, time.Millisecond),
			span(7, 3, 2, tracepb.Span_SPAN_KIND_SERVER, 4*time.Second, time.Millisecond)),
		resourceSpans("b", "",
			span(6, 3, 2, tracepb.Span_SPAN_KIND_SERVER, 2*time.Second, time.Millisecond),
			span(7, 2, 0, tracepb.Span_SPAN_KIND_CLIENT, 3*time.Second, time.Millisecond)),
	))
	app, stdout, stderr := testApp(project)
	ingestFixture(t, app, stdout, stderr, project, input, phase2AMatrixStart, phase2AMatrixStart.Add(time.Minute))
	stdout.Reset()
	stderr.Reset()
	code := app.Run(context.Background(), []string{"graph", "--service", "a", "--depth", "5", "--at", phase2AMatrixStart.Add(5 * time.Second).Format(time.RFC3339Nano), "--project-dir", project, "--json"})
	if code != 0 {
		t.Fatalf("cycle graph exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	graph := decodeEnvelope(t, stdout.Bytes())
	data := graph["data"].(map[string]any)
	if len(data["nodes"].([]any)) != 2 || len(data["edges"].([]any)) != 2 {
		t.Fatalf("cycle graph=%#v", graph)
	}
}

func testPhase2AConfidenceCLI(t *testing.T) {
	project := t.TempDir()
	input := writeOTLPFixture(t, project, "confidence.otlp.jsonl", otlpLine(t,
		resourceSpans("api", "",
			span(8, 2, 0, tracepb.Span_SPAN_KIND_CLIENT, time.Second, time.Millisecond),
			span(9, 4, 0, tracepb.Span_SPAN_KIND_CLIENT, 2*time.Second, time.Millisecond, stringAttribute("db.system.name", "postgresql")),
			span(10, 5, 0, tracepb.Span_SPAN_KIND_CLIENT, 3*time.Second, time.Millisecond, stringAttribute("server.address", "example.invalid"))),
		resourceSpans("remote", "", span(8, 3, 2, tracepb.Span_SPAN_KIND_SERVER, time.Second+time.Nanosecond, time.Millisecond)),
	))
	app, stdout, stderr := testApp(project)
	ingestFixture(t, app, stdout, stderr, project, input, phase2AMatrixStart, phase2AMatrixStart.Add(time.Minute))
	for level, want := range map[string]int{"low": 3, "medium": 2, "high": 1, "exact": 0} {
		graph := runGraphJSON(t, app, stdout, stderr, project, "api", phase2AMatrixStart.Add(4*time.Second), level, 100)
		if got := len(graphEdges(t, graph)); got != want {
			t.Fatalf("confidence %s edges=%d want=%d graph=%#v", level, got, want, graph)
		}
	}
}

func testPhase2APercentilesCLI(t *testing.T) {
	project := t.TempDir()
	spans := make([]*tracepb.Span, 0, 4)
	for index, duration := range []time.Duration{time.Nanosecond, 2 * time.Nanosecond, 3 * time.Nanosecond, 4 * time.Nanosecond} {
		spans = append(spans, span(byte(20+index), byte(10+index), 0, tracepb.Span_SPAN_KIND_SERVER, time.Duration(index+1)*time.Second, duration,
			stringAttribute("http.request.method", "GET"), stringAttribute("http.route", "/items/{id}")))
	}
	input := writeOTLPFixture(t, project, "percentiles.otlp.jsonl", otlpLine(t, resourceSpans("api", "", spans...)))
	app, stdout, stderr := testApp(project)
	ingestFixture(t, app, stdout, stderr, project, input, phase2AMatrixStart, phase2AMatrixStart.Add(time.Minute))
	db := openCLITestDatabase(t, project)
	defer db.Close()
	var p50, p95, p99 int64
	if err := db.QueryRow(`SELECT p50_ns,p95_ns,p99_ns FROM telemetry_windows`).Scan(&p50, &p95, &p99); err != nil {
		t.Fatal(err)
	}
	if p50 != 2 || p95 != 4 || p99 != 4 {
		t.Fatalf("percentiles p50=%d p95=%d p99=%d", p50, p95, p99)
	}
}

func testPhase2ATruncationCLI(t *testing.T) {
	project := t.TempDir()
	spans := make([]*tracepb.Span, 0, 8)
	for index := 0; index < 8; index++ {
		spans = append(spans, span(byte(30+index), byte(30+index), 0, tracepb.Span_SPAN_KIND_CLIENT, time.Duration(index+1)*time.Second, time.Millisecond,
			stringAttribute("server.address", fmt.Sprintf("target-%02d.invalid", index))))
	}
	input := writeOTLPFixture(t, project, "truncated.otlp.jsonl", otlpLine(t, resourceSpans("api", "", spans...)))
	app, stdout, stderr := testApp(project)
	ingestFixture(t, app, stdout, stderr, project, input, phase2AMatrixStart, phase2AMatrixStart.Add(time.Minute))
	graph := runGraphJSON(t, app, stdout, stderr, project, "api", phase2AMatrixStart.Add(10*time.Second), "low", 3)
	data := graph["data"].(map[string]any)
	if data["truncated"] != true || len(data["nodes"].([]any)) != 3 || len(graph["warnings"].([]any)) == 0 {
		t.Fatalf("truncated graph=%#v", graph)
	}
	logicalKeys := func(data map[string]any) []string {
		var keys []string
		for _, raw := range data["nodes"].([]any) {
			keys = append(keys, raw.(map[string]any)["logical_key"].(string))
		}
		return keys
	}
	want := []string{"target-00.invalid", "target-01.invalid", "api"}
	if got := logicalKeys(data); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("semantic truncated subset=%v want=%v", got, want)
	}
	secondProject := t.TempDir()
	secondApp, secondStdout, secondStderr := testApp(secondProject)
	ingestFixture(t, secondApp, secondStdout, secondStderr, secondProject, input, phase2AMatrixStart, phase2AMatrixStart.Add(time.Minute))
	secondGraph := runGraphJSON(t, secondApp, secondStdout, secondStderr, secondProject, "api", phase2AMatrixStart.Add(10*time.Second), "low", 3)
	if got := logicalKeys(secondGraph["data"].(map[string]any)); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("equivalent database selected %v want=%v", got, want)
	}
}

func testPhase2AWindowOrderAndBoundariesCLI(t *testing.T) {
	project := t.TempDir()
	app, stdout, stderr := testApp(project)
	laterStart := phase2AMatrixStart.Add(time.Hour)
	later := writeOTLPFixture(t, project, "later.otlp.jsonl", otlpLine(t, resourceSpans("api", "", span(50, 1, 0, tracepb.Span_SPAN_KIND_CLIENT, time.Hour+time.Second, time.Millisecond, stringAttribute("server.address", "later.invalid")))))
	ingestFixture(t, app, stdout, stderr, project, later, laterStart, laterStart.Add(time.Minute))
	earlier := writeOTLPFixture(t, project, "earlier.otlp.jsonl", otlpLine(t, resourceSpans("api", "", span(51, 1, 0, tracepb.Span_SPAN_KIND_CLIENT, time.Second, time.Millisecond, stringAttribute("server.address", "earlier.invalid")))))
	ingestFixture(t, app, stdout, stderr, project, earlier, phase2AMatrixStart, phase2AMatrixStart.Add(time.Minute))
	for _, test := range []struct {
		name string
		at   time.Time
		want int
	}{{"start", phase2AMatrixStart, 1}, {"inside", phase2AMatrixStart.Add(30 * time.Second), 1}, {"end", phase2AMatrixStart.Add(time.Minute), 0}, {"later", laterStart.Add(30 * time.Second), 1}} {
		t.Run(test.name, func(t *testing.T) {
			graph := runGraphJSON(t, app, stdout, stderr, project, "api", test.at, "low", 100)
			if got := len(graphEdges(t, graph)); got != test.want {
				t.Fatalf("at=%s edges=%d want=%d", test.at, got, test.want)
			}
		})
	}
}

func testPhase2AConflictCLI(t *testing.T) {
	project := t.TempDir()
	path := writeOTLPFixture(t, project, "same-source.otlp.jsonl", otlpLine(t, resourceSpans("api", "", span(60, 1, 0, tracepb.Span_SPAN_KIND_CLIENT, time.Second, time.Millisecond, stringAttribute("server.address", "one.invalid")))))
	app, stdout, stderr := testApp(project)
	first := ingestFixture(t, app, stdout, stderr, project, path, phase2AMatrixStart, phase2AMatrixStart.Add(time.Minute))
	if err := os.WriteFile(path, otlpLine(t, resourceSpans("api", "", span(61, 1, 0, tracepb.Span_SPAN_KIND_CLIENT, 2*time.Second, time.Millisecond, stringAttribute("server.address", "two.invalid")))), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	code := app.Run(context.Background(), ingestArgs(project, path, phase2AMatrixStart, phase2AMatrixStart.Add(time.Minute)))
	if code == 0 || stderr.Len() != 0 {
		t.Fatalf("conflict exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	errorEnvelope := decodeEnvelope(t, stdout.Bytes())
	if errorEnvelope["error"].(map[string]any)["code"] != ErrorCodeConflict {
		t.Fatalf("conflict envelope=%#v first=%#v", errorEnvelope, first)
	}
}

func testPhase2AGraphSelectorsCLI(t *testing.T) {
	project := t.TempDir()
	input := writeOTLPFixture(t, project, "selectors.otlp.jsonl", otlpLine(t,
		resourceSpans("api", "ns1", span(70, 1, 0, tracepb.Span_SPAN_KIND_INTERNAL, time.Second, time.Millisecond)),
		resourceSpans("api", "ns2", span(71, 1, 0, tracepb.Span_SPAN_KIND_INTERNAL, 2*time.Second, time.Millisecond)),
	))
	app, stdout, stderr := testApp(project)
	ingestFixture(t, app, stdout, stderr, project, input, phase2AMatrixStart, phase2AMatrixStart.Add(time.Minute))

	stdout.Reset()
	stderr.Reset()
	code := app.Run(context.Background(), []string{"graph", "--service", "api", "--at", phase2AMatrixStart.Add(3 * time.Second).Format(time.RFC3339Nano), "--project-dir", project, "--json"})
	if code == 0 || stderr.Len() != 0 {
		t.Fatalf("ambiguous exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	ambiguous := decodeEnvelope(t, stdout.Bytes())["error"].(map[string]any)
	if ambiguous["code"] != ErrorCodeConflict || len(ambiguous["details"].(map[string]any)["candidates"].([]any)) != 2 {
		t.Fatalf("ambiguous error=%#v", ambiguous)
	}

	stdout.Reset()
	code = app.Run(context.Background(), []string{"graph", "--service", "missing", "--at", phase2AMatrixStart.Format(time.RFC3339), "--project-dir", project, "--json"})
	if code == 0 || decodeEnvelope(t, stdout.Bytes())["error"].(map[string]any)["code"] != ErrorCodeNotFound {
		t.Fatalf("not found exit=%d stdout=%q", code, stdout.String())
	}

	root := runGraphJSON(t, app, stdout, stderr, project, "ns1.api", phase2AMatrixStart.Add(3*time.Second), "low", 100)
	if len(root["data"].(map[string]any)["nodes"].([]any)) != 1 || len(graphEdges(t, root)) != 0 || len(root["warnings"].([]any)) == 0 {
		t.Fatalf("root-only graph=%#v", root)
	}

	stdout.Reset()
	stderr.Reset()
	code = app.Run(context.Background(), []string{"graph", "--all", "--max-nodes", "1", "--at", phase2AMatrixStart.Add(3 * time.Second).Format(time.RFC3339Nano), "--project-dir", project, "--json"})
	if code != 0 {
		t.Fatalf("all graph exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	all := decodeEnvelope(t, stdout.Bytes())
	if all["data"].(map[string]any)["truncated"] != true || len(all["data"].(map[string]any)["nodes"].([]any)) != 1 {
		t.Fatalf("all graph=%#v", all)
	}
}

func testPhase2AInvalidFilesCLI(t *testing.T) {
	for _, test := range []struct {
		name    string
		content []byte
	}{
		{name: "negative duration", content: otlpLine(t, resourceSpans("api", "", &tracepb.Span{TraceId: bytesOfForCLI(80, 16), SpanId: bytesOfForCLI(1, 8), StartTimeUnixNano: uint64(phase2AMatrixStart.Add(2 * time.Second).UnixNano()), EndTimeUnixNano: uint64(phase2AMatrixStart.Add(time.Second).UnixNano())}))},
		{name: "oversized line", content: append(bytesOfForCLI(' ', (1<<20)+1), '\n')},
	} {
		t.Run(test.name, func(t *testing.T) {
			project := t.TempDir()
			input := writeOTLPFixture(t, project, "invalid.otlp.jsonl", test.content)
			app, stdout, stderr := testApp(project)
			code := app.Run(context.Background(), ingestArgs(project, input, phase2AMatrixStart, phase2AMatrixStart.Add(time.Minute)))
			if code == 0 || stderr.Len() != 0 {
				t.Fatalf("invalid exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if _, err := os.Stat(filepath.Join(project, ".prodmap", "prodmap.db")); !os.IsNotExist(err) {
				t.Fatalf("invalid input created database: %v", err)
			}
		})
	}
}

func resourceSpans(serviceName, namespace string, spans ...*tracepb.Span) *tracepb.ResourceSpans {
	var attributes []*commonpb.KeyValue
	if serviceName != "" {
		attributes = append(attributes, stringAttribute("service.name", serviceName))
	}
	if namespace != "" {
		attributes = append(attributes, stringAttribute("service.namespace", namespace))
	}
	return &tracepb.ResourceSpans{Resource: &resourcepb.Resource{Attributes: attributes}, ScopeSpans: []*tracepb.ScopeSpans{{Spans: spans}}}
}

func span(traceByte, spanByte, parentByte byte, kind tracepb.Span_SpanKind, offset, duration time.Duration, attributes ...*commonpb.KeyValue) *tracepb.Span {
	result := &tracepb.Span{
		TraceId: bytesOfForCLI(traceByte, 16), SpanId: bytesOfForCLI(spanByte, 8), Kind: kind,
		StartTimeUnixNano: uint64(phase2AMatrixStart.Add(offset).UnixNano()), EndTimeUnixNano: uint64(phase2AMatrixStart.Add(offset + duration).UnixNano()),
		Attributes: attributes,
	}
	if parentByte != 0 {
		result.ParentSpanId = bytesOfForCLI(parentByte, 8)
	}
	return result
}

func stringAttribute(key, value string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}}
}

func otlpLine(t *testing.T, resources ...*tracepb.ResourceSpans) []byte {
	t.Helper()
	binary, err := proto.Marshal(&collecttracepb.ExportTraceServiceRequest{ResourceSpans: resources})
	if err != nil {
		t.Fatal(err)
	}
	traces, err := (&ptrace.ProtoUnmarshaler{}).UnmarshalTraces(binary)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := (&ptrace.JSONMarshaler{}).MarshalTraces(traces)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func writeOTLPFixture(t *testing.T, directory, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if len(content) == 0 || content[len(content)-1] != '\n' {
		content = append(content, '\n')
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func ingestArgs(project, input string, start, end time.Time) []string {
	return []string{"telemetry", "ingest", "--file", input, "--window-start", start.Format(time.RFC3339Nano), "--window-end", end.Format(time.RFC3339Nano), "--project-dir", project, "--json"}
}

func ingestFixture(t *testing.T, app *App, stdout, stderr *bytes.Buffer, project, input string, start, end time.Time) map[string]any {
	t.Helper()
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), ingestArgs(project, input, start, end)); code != 0 {
		t.Fatalf("ingest exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("JSON ingest wrote stderr=%q", stderr.String())
	}
	return decodeEnvelope(t, stdout.Bytes())
}

func runGraphJSON(t *testing.T, app *App, stdout, stderr *bytes.Buffer, project, selector string, at time.Time, confidence string, maxNodes int) map[string]any {
	t.Helper()
	stdout.Reset()
	stderr.Reset()
	args := []string{"graph", "--service", selector, "--at", at.Format(time.RFC3339Nano), "--min-confidence", confidence, "--max-nodes", fmt.Sprintf("%d", maxNodes), "--project-dir", project, "--json"}
	if code := app.Run(context.Background(), args); code != 0 {
		t.Fatalf("graph exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("JSON graph wrote stderr=%q", stderr.String())
	}
	return decodeEnvelope(t, stdout.Bytes())
}

func graphEdges(t *testing.T, envelope map[string]any) []map[string]any {
	t.Helper()
	raw := envelope["data"].(map[string]any)["edges"].([]any)
	result := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		result = append(result, item.(map[string]any))
	}
	return result
}

func confidenceLevel(edge map[string]any) string {
	return edge["confidence"].(map[string]any)["level"].(string)
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func bytesOfForCLI(value byte, count int) []byte {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return result
}

func openCLITestDatabase(t *testing.T, project string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(project, ".prodmap", "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	return db
}
