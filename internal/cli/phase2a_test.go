package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContradictoryLinkedTargetCLIJSONHumanSQLiteAndReplay(t *testing.T) {
	project := t.TempDir()
	fixture, err := filepath.Abs(filepath.Join("..", "..", "testdata", "otel", "conflicting-linked-target.otlp.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	app, stdout, stderr := testApp(project)
	args := []string{"telemetry", "ingest", "--file", fixture, "--window-start", "2026-08-19T12:00:00Z", "--window-end", "2026-08-19T12:01:00Z", "--project-dir", project, "--json"}
	if code := app.Run(context.Background(), args); code != 0 {
		t.Fatalf("ingest exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	firstID := decodeEnvelope(t, stdout.Bytes())["data"].(map[string]any)["ingestion_id"]
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), args); code != 0 {
		t.Fatalf("replay exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	replay := decodeEnvelope(t, stdout.Bytes())["data"].(map[string]any)
	if replay["idempotent_replay"] != true || replay["ingestion_id"] != firstID {
		t.Fatalf("replay=%#v", replay)
	}

	graphArgs := []string{"graph", "--service", "checkout", "--at", "2026-08-19T12:00:30Z", "--project-dir", project, "--json"}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), graphArgs); code != 0 {
		t.Fatalf("graph JSON exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	graph := decodeEnvelope(t, stdout.Bytes())
	edges := graph["data"].(map[string]any)["edges"].([]any)
	if len(edges) != 1 {
		t.Fatalf("graph=%#v", graph)
	}
	edge := edges[0].(map[string]any)
	if confidenceLevel(edge) != "MEDIUM" || edge["confidence"].(map[string]any)["basis"] != "linked spans with conflicting semantic target" || !graphTargetIs(t, graph, edge["to"], "service", "payment") || strings.Contains(mustJSON(t, graph), "wrong-fallback") {
		t.Fatalf("contradictory graph=%#v", graph)
	}
	if !strings.Contains(mustJSON(t, edge["limitations"]), "contradicted") {
		t.Fatalf("contradiction limitation missing: %#v", edge)
	}

	graphArgs = graphArgs[:len(graphArgs)-1]
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), graphArgs); code != 0 || !strings.Contains(stdout.String(), "confidence=MEDIUM") || !strings.Contains(stdout.String(), "contradicted") || stderr.Len() != 0 {
		t.Fatalf("human graph exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	db := openCLITestDatabase(t, project)
	defer db.Close()
	var confidence, basis, limitations string
	if err := db.QueryRow(`SELECT confidence_level,confidence_basis,limitations_json FROM service_dependency_observations`).Scan(&confidence, &basis, &limitations); err != nil {
		t.Fatal(err)
	}
	var wrongCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM dependencies WHERE logical_key='wrong-fallback'`).Scan(&wrongCount); err != nil {
		t.Fatal(err)
	}
	var clientClaims, serverClaims int
	if err := db.QueryRow(`SELECT SUM(claim='outbound client span'),SUM(claim='linked remote server span') FROM topology_evidence`).Scan(&clientClaims, &serverClaims); err != nil {
		t.Fatal(err)
	}
	if confidence != "MEDIUM" || basis != "linked spans with conflicting semantic target" || !strings.Contains(limitations, "contradicted") || wrongCount != 0 || clientClaims != 1 || serverClaims != 1 {
		t.Fatalf("stored contradiction confidence=%q basis=%q limitations=%q wrong=%d claims=%d/%d", confidence, basis, limitations, wrongCount, clientClaims, serverClaims)
	}
}

func TestPhase2ACLIIngestReplayGraphTemporalConfidenceAndRedaction(t *testing.T) {
	projectDir := t.TempDir()
	fixture, err := filepath.Abs(filepath.Join("..", "..", "testdata", "otel", "linked-services.otlp.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	app, stdout, stderr := testApp(projectDir)
	args := []string{"telemetry", "ingest", "--file", fixture, "--window-start", "2026-08-19T12:00:00Z", "--window-end", "2026-08-19T12:01:00Z", "--project-dir", projectDir, "--json"}
	if code := app.Run(context.Background(), args); code != 0 {
		t.Fatalf("ingest exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	first := decodeEnvelope(t, stdout.Bytes())
	if first["command"] != "telemetry ingest" || stderr.Len() != 0 {
		t.Fatalf("ingest envelope=%#v stderr=%q", first, stderr.String())
	}
	firstData := first["data"].(map[string]any)
	if firstData["spans_accepted"] != float64(3) || firstData["dependencies"] != float64(1) || firstData["idempotent_replay"] != false {
		t.Fatalf("ingest data=%#v", firstData)
	}
	ingestionID := firstData["ingestion_id"]
	stdout.Reset()
	stderr.Reset()
	replayArgs := append(append([]string(nil), args...), "--format", "otlp-jsonl")
	if code := app.Run(context.Background(), replayArgs); code != 0 {
		t.Fatalf("replay exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	replayData := decodeEnvelope(t, stdout.Bytes())["data"].(map[string]any)
	if replayData["ingestion_id"] != ingestionID || replayData["idempotent_replay"] != true {
		t.Fatalf("replay=%#v first=%#v", replayData, firstData)
	}

	stdout.Reset()
	stderr.Reset()
	graphArgs := []string{"graph", "--service", "checkout", "--environment", "default", "--at", "2026-08-19T12:00:02.5Z", "--project-dir", projectDir, "--json"}
	if code := app.Run(context.Background(), graphArgs); code != 0 {
		t.Fatalf("graph exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	graph := decodeEnvelope(t, stdout.Bytes())
	edges := graph["data"].(map[string]any)["edges"].([]any)
	if len(edges) != 1 {
		t.Fatalf("graph=%#v", graph)
	}
	edge := edges[0].(map[string]any)
	if edge["relation_type"] != "OBSERVED" || edge["confidence"].(map[string]any)["level"] != "HIGH" {
		t.Fatalf("edge=%#v", edge)
	}

	stdout.Reset()
	stderr.Reset()
	graphArgs[6] = "2026-08-19T11:59:00Z"
	if code := app.Run(context.Background(), graphArgs); code != 0 {
		t.Fatalf("outside graph exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	outside := decodeEnvelope(t, stdout.Bytes())
	if got := len(outside["data"].(map[string]any)["edges"].([]any)); got != 0 {
		t.Fatalf("outside graph edges=%d", got)
	}

	stdout.Reset()
	stderr.Reset()
	exactArgs := []string{"graph", "--service", "checkout", "--at", "2026-08-19T12:00:02.5Z", "--min-confidence", "exact", "--project-dir", projectDir, "--json"}
	if code := app.Run(context.Background(), exactArgs); code != 0 {
		t.Fatalf("exact graph exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if got := len(decodeEnvelope(t, stdout.Bytes())["data"].(map[string]any)["edges"].([]any)); got != 0 {
		t.Fatalf("exact graph returned %d OTel edges", got)
	}
	db := openCLITestDatabase(t, projectDir)
	var protocol, operation, route string
	if err := db.QueryRow(`SELECT protocol,operation,route_template FROM endpoints WHERE route_template IS NOT NULL ORDER BY operation LIMIT 1`).Scan(&protocol, &operation, &route); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if protocol != "http" || operation != "GET /checkout/{id}" || route != "/checkout/{id}" {
		t.Fatalf("persisted endpoint protocol=%q operation=%q route=%q", protocol, operation, route)
	}

	database, err := os.ReadFile(filepath.Join(projectDir, ".prodmap", "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"fixture-secret-never-persist", "authorization", "url.full", "01010101010101010101010101010101", "0202020202020202"} {
		if strings.Contains(string(database), forbidden) || strings.Contains(stdout.String(), forbidden) || strings.Contains(stderr.String(), forbidden) {
			t.Fatalf("sensitive marker %q escaped sanitization", forbidden)
		}
	}
	assertJSONGolden(t, filepath.Join("testdata", "phase2a.golden.json"), map[string]any{
		"telemetry_ingest": normalizeUUIDs(first),
		"graph":            normalizeUUIDs(graph),
	})
}

func TestTelemetryIngestRejectsUnsupportedFormatBeforeOpeningDatabase(t *testing.T) {
	projectDir := t.TempDir()
	fixture, err := filepath.Abs(filepath.Join("..", "..", "testdata", "otel", "linked-services.otlp.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	app, stdout, stderr := testApp(projectDir)
	code := app.Run(context.Background(), []string{"telemetry", "ingest", "--file", fixture, "--format", "json", "--window-start", "2026-08-19T12:00:00Z", "--window-end", "2026-08-19T12:01:00Z", "--project-dir", projectDir, "--json"})
	if code != 2 || stderr.Len() != 0 {
		t.Fatalf("invalid format exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	decodeEnvelope(t, stdout.Bytes())
	if _, err := os.Stat(filepath.Join(projectDir, ".prodmap", "prodmap.db")); !os.IsNotExist(err) {
		t.Fatalf("invalid format created database: %v", err)
	}
}

func TestPhase2AMalformedFileDoesNotOpenDatabase(t *testing.T) {
	projectDir := t.TempDir()
	input := filepath.Join(projectDir, "malformed.jsonl")
	if err := os.WriteFile(input, []byte(`{"unknown":"secret-value"`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	app, stdout, stderr := testApp(projectDir)
	code := app.Run(context.Background(), []string{"telemetry", "ingest", "--file", input, "--window-start", "2026-08-19T12:00:00Z", "--window-end", "2026-08-19T12:01:00Z", "--project-dir", projectDir, "--json"})
	if code != 2 || stderr.Len() != 0 || strings.Contains(stdout.String(), "secret-value") {
		t.Fatalf("malformed exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".prodmap", "prodmap.db")); !os.IsNotExist(err) {
		t.Fatalf("malformed ingest created database: %v", err)
	}
}
