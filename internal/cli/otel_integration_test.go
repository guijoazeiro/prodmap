package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const collectorDiagnosticLimit = 4096

var (
	diagnosticHexID   = regexp.MustCompile(`(?i)\b[0-9a-f]{16,64}\b`)
	diagnosticBase64  = regexp.MustCompile(`\b[A-Za-z0-9+/]{16,}={0,2}\b`)
	diagnosticUserURL = regexp.MustCompile(`(?i)(https?://)[^/@\s]+@`)
	diagnosticQuery   = regexp.MustCompile(`(?i)(https?://[^?\s]+)\?[^\s]*`)
)

func TestRealOTelCollector(t *testing.T) {
	if os.Getenv("PRODMAP_TEST_REAL_OTEL") != "1" {
		t.Skip("set PRODMAP_TEST_REAL_OTEL=1 to run the pinned Collector integration")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("Docker CLI is required for the requested real Collector smoke: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
		t.Fatalf("Docker daemon is required for the requested real Collector smoke: %v", err)
	}
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	compose := filepath.Join(repositoryRoot, "deploy", "otel-collector", "compose.yaml")
	outputDir := t.TempDir()
	project := fmt.Sprintf("prodmap-otel-%d", time.Now().UnixNano())
	composeCommand := func(arguments ...string) *exec.Cmd {
		args := append([]string{"compose", "-p", project, "-f", compose}, arguments...)
		command := exec.CommandContext(ctx, "docker", args...)
		command.Env = append(os.Environ(), "OTEL_OUTPUT_DIR="+outputDir, fmt.Sprintf("OTEL_UID=%d", os.Getuid()), fmt.Sprintf("OTEL_GID=%d", os.Getgid()))
		return command
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		command := exec.CommandContext(cleanupCtx, "docker", "compose", "-p", project, "-f", compose, "down", "--volumes", "--remove-orphans")
		command.Env = append(os.Environ(), "OTEL_OUTPUT_DIR="+outputDir, fmt.Sprintf("OTEL_UID=%d", os.Getuid()), fmt.Sprintf("OTEL_GID=%d", os.Getgid()))
		if output, err := command.CombinedOutput(); err != nil {
			t.Errorf("clean up Collector Compose project: %v: %s", err, sanitizeCollectorDiagnostic(output))
			return
		}
		for _, resource := range [][]string{
			{"ps", "-aq", "--filter", "label=com.docker.compose.project=" + project},
			{"network", "ls", "-q", "--filter", "label=com.docker.compose.project=" + project},
			{"volume", "ls", "-q", "--filter", "label=com.docker.compose.project=" + project},
		} {
			output, err := exec.CommandContext(cleanupCtx, "docker", resource...).Output()
			if err != nil || strings.TrimSpace(string(output)) != "" {
				t.Errorf("Collector Compose project resources remain after cleanup")
			}
		}
	})
	if output, err := composeCommand("up", "-d", "--wait").CombinedOutput(); err != nil {
		t.Fatalf("start Collector: %v\nstartup: %s\n%s", err, sanitizeCollectorDiagnostic(output), collectorComposeDiagnostics(project, compose, outputDir))
	}
	healthDeadline := time.Now().Add(20 * time.Second)
	for {
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:13133/", nil)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		response, requestErr := http.DefaultClient.Do(request)
		if requestErr == nil {
			response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				break
			}
		}
		if time.Now().After(healthDeadline) {
			t.Fatalf("Collector health endpoint was not ready: %v\n%s", requestErr, collectorComposeDiagnostics(project, compose, outputDir))
		}
		time.Sleep(100 * time.Millisecond)
	}
	payload, err := os.ReadFile(filepath.Join(repositoryRoot, "testdata", "otel", "linked-services.otlp.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:4318/v1/traces", bytes.NewReader(bytes.TrimSpace(payload)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, bodyErr := io.ReadAll(io.LimitReader(response.Body, collectorDiagnosticLimit+1))
	response.Body.Close()
	if bodyErr != nil {
		t.Fatalf("read bounded OTLP HTTP response: %v", bodyErr)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		t.Fatalf("OTLP HTTP status = %s; sanitized response=%s\n%s", response.Status, sanitizeCollectorDiagnostic(body), collectorComposeDiagnostics(project, compose, outputDir))
	}
	outputFile := filepath.Join(outputDir, "traces.otlp.jsonl")
	deadline := time.Now().Add(20 * time.Second)
	for {
		if info, err := os.Stat(outputFile); err == nil && info.Size() > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Collector did not flush the trace snapshot\n%s", collectorComposeDiagnostics(project, compose, outputDir))
		}
		time.Sleep(100 * time.Millisecond)
	}
	if output, err := composeCommand("stop").CombinedOutput(); err != nil {
		t.Fatalf("stop Collector: %v: %s", err, sanitizeCollectorDiagnostic(output))
	}
	projectDir := t.TempDir()
	app, stdout, stderr := testApp(projectDir)
	if code := app.Run(ctx, []string{"telemetry", "ingest", "--file", outputFile, "--window-start", "2026-08-19T12:00:00Z", "--window-end", "2026-08-19T12:01:00Z", "--project-dir", projectDir, "--json"}); code != 0 {
		t.Fatalf("ingest frozen Collector output failed with exit=%d", code)
	}
	database := openCLITestDatabase(t, projectDir)
	defer database.Close()
	for table, want := range map[string]int{"services": 2, "endpoints": 2, "dependencies": 1, "service_dependency_observations": 1} {
		var got int
		if err := database.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil || got != want {
			t.Fatalf("%s count=%d want=%d err=%v", table, got, want, err)
		}
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(ctx, []string{"graph", "--service", "checkout", "--at", "2026-08-19T12:00:02.5Z", "--project-dir", projectDir, "--json"}); code != 0 {
		t.Fatalf("query graph from frozen Collector output failed with exit=%d", code)
	}
	if !strings.Contains(stdout.String(), `"relation_type":"OBSERVED"`) || !strings.Contains(stdout.String(), `"logical_key":"payment"`) {
		t.Fatalf("unexpected Collector graph: %s", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(ctx, []string{"graph", "--service", "checkout", "--at", "2026-08-19T12:01:00Z", "--project-dir", projectDir, "--json"}); code != 0 {
		t.Fatalf("query boundary graph from frozen Collector output failed with exit=%d", code)
	}
	if strings.Contains(stdout.String(), `"relation_type":"OBSERVED"`) {
		t.Fatalf("outside-window graph retained an edge: %s", stdout.String())
	}
}

func collectorComposeDiagnostics(project, compose, outputDir string) string {
	diagnosticCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	environment := append(os.Environ(), "OTEL_OUTPUT_DIR="+outputDir, fmt.Sprintf("OTEL_UID=%d", os.Getuid()), fmt.Sprintf("OTEL_GID=%d", os.Getgid()))
	commands := [][]string{
		{"compose", "-p", project, "-f", compose, "ps", "-a"},
		{"compose", "-p", project, "-f", compose, "logs", "--no-color", "collector", "health-probe"},
	}
	labels := []string{"docker compose ps -a", "docker compose logs collector health-probe"}
	parts := make([]string, 0, len(commands))
	for index, arguments := range commands {
		buffer := &limitedDiagnosticBuffer{remaining: collectorDiagnosticLimit}
		command := exec.CommandContext(diagnosticCtx, "docker", arguments...)
		command.Env = environment
		command.Stdout = buffer
		command.Stderr = buffer
		err := command.Run()
		parts = append(parts, fmt.Sprintf("%s (error=%v): %s", labels[index], err, sanitizeCollectorDiagnostic(buffer.Bytes())))
	}
	return strings.Join(parts, "\n")
}

type limitedDiagnosticBuffer struct {
	data      bytes.Buffer
	remaining int
}

func (buffer *limitedDiagnosticBuffer) Write(content []byte) (int, error) {
	written := len(content)
	if buffer.remaining > 0 {
		kept := content
		if len(kept) > buffer.remaining {
			kept = kept[:buffer.remaining]
		}
		_, _ = buffer.data.Write(kept)
		buffer.remaining -= len(kept)
	}
	return written, nil
}

func (buffer *limitedDiagnosticBuffer) Bytes() []byte { return buffer.data.Bytes() }

func sanitizeCollectorDiagnostic(raw []byte) string {
	if len(raw) > collectorDiagnosticLimit {
		raw = raw[:collectorDiagnosticLimit]
	}
	text := strings.ToValidUTF8(string(raw), "?")
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		lower := strings.ToLower(line)
		sensitive := false
		for _, marker := range []string{"authorization", "token", "secret", "traceid", "trace_id", "spanid", "span_id", "resourcespans", "scopespans", "payload", "attribute"} {
			if strings.Contains(lower, marker) {
				sensitive = true
				break
			}
		}
		if sensitive {
			lines[index] = "[redacted sensitive diagnostic line]"
			continue
		}
		line = diagnosticUserURL.ReplaceAllString(line, `${1}[redacted]@`)
		line = diagnosticQuery.ReplaceAllString(line, `${1}?[redacted]`)
		line = diagnosticHexID.ReplaceAllString(line, "[redacted-id]")
		lines[index] = diagnosticBase64.ReplaceAllString(line, "[redacted-value]")
	}
	result := strings.TrimSpace(strings.Join(lines, "\n"))
	if result == "" {
		return "[no diagnostic body]"
	}
	return result
}
