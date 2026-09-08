package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/guijoazeiro/prodmap/internal/mcpserver"
	prodmapsqlite "github.com/guijoazeiro/prodmap/internal/sqlite"
	_ "modernc.org/sqlite"
)

func TestMCPHelpAndInvalidFlagsDoNotOpenInventory(t *testing.T) {
	project := t.TempDir()
	app, stdout, stderr := testApp(project)
	if code := app.Run(t.Context(), []string{"mcp", "serve", "--help"}); code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "usage: prodmap mcp serve") {
		t.Fatalf("help code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(t.Context(), []string{"mcp", "serve", "--json", "--project-dir", project}); code == 0 || stderr.Len() != 0 {
		t.Fatalf("invalid code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(project, ".prodmap")); !os.IsNotExist(err) {
		t.Fatalf("MCP help or invalid flags opened inventory: %v", err)
	}
}

func TestMCPReadOnlyOpenRejectsAbsentAndOutdatedInventoryWithoutMutation(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		project := t.TempDir()
		app, _, stderr := testApp(project)
		if code := app.Run(t.Context(), []string{"mcp", "serve", "--project-dir", project}); code == 0 {
			t.Fatal("MCP opened an absent inventory")
		}
		if _, err := os.Stat(filepath.Join(project, ".prodmap")); !os.IsNotExist(err) {
			t.Fatalf("absent MCP inventory created project state: %v", err)
		}
		if strings.Contains(stderr.String(), project) {
			t.Fatalf("MCP error exposed path: %q", stderr.String())
		}
	})

	t.Run("outdated", func(t *testing.T) {
		project := t.TempDir()
		path := filepath.Join(project, ".prodmap", "prodmap.db")
		store, err := prodmapsqlite.Open(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("DELETE FROM schema_migrations WHERE version = 5"); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		before := mcpDatabaseState(t, path)
		app, _, _ := testApp(project)
		if code := app.Run(t.Context(), []string{"mcp", "serve", "--project-dir", project}); code == 0 {
			t.Fatal("MCP opened an outdated inventory")
		}
		if after := mcpDatabaseState(t, path); before != after {
			t.Fatalf("MCP migrated or changed outdated inventory\nbefore=%+v\nafter=%+v", before, after)
		}
	})
}

func TestMCPToolUsesSQLiteFixtureWithoutWrites(t *testing.T) {
	project := t.TempDir()
	deploymentID, deployedAt := seedRegressionCLIDataWithMetrics(t, project, regressionCLIMetrics{requests: 20, errors: 1, p50NS: 250_000_000, p95NS: 250_000_000, p99NS: 250_000_000}, regressionCLIMetrics{requests: 30, errors: 1, p50NS: 300_000_000, p95NS: 300_000_000, p99NS: 300_000_000})
	databasePath := filepath.Join(project, ".prodmap", "prodmap.db")
	before := mcpDatabaseState(t, databasePath)
	store, err := prodmapsqlite.OpenReadOnly(t.Context(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := deployedAt.Add(time.Hour)
	server, err := mcpserver.New(mcpserver.Config{
		OpenReader: func(ctx context.Context) (mcpserver.Reader, func() error, error) {
			snapshot, err := store.BeginReadSnapshot(ctx)
			if err != nil {
				return nil, nil, err
			}
			return snapshot, snapshot.Close, nil
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "prodmap-cli-test", Version: "v1"}, nil)
	session, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	arguments := map[string]any{"deployment": deploymentID, "metric": "latency_p95"}
	first, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: mcpserver.ToolName, Arguments: arguments})
	if err != nil || first.IsError {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: mcpserver.ToolName, Arguments: arguments})
	if err != nil || second.IsError {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	firstJSON, err := json.Marshal(first.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var firstOutput, secondOutput map[string]any
	if err := json.Unmarshal(firstJSON, &firstOutput); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(secondJSON, &secondOutput); err != nil {
		t.Fatal(err)
	}
	if firstOutput["investigation_key"] != secondOutput["investigation_key"] || firstOutput["regression"].(map[string]any)["comparison_key"] != secondOutput["regression"].(map[string]any)["comparison_key"] {
		t.Fatalf("non-deterministic replay first=%#v second=%#v", firstOutput, secondOutput)
	}
	classification := firstOutput["regression"].(map[string]any)["classification"].(map[string]any)
	if firstOutput["status"] != "AVAILABLE" || classification["result"] != "CANDIDATE" || classification["confidence"].(map[string]any)["level"] != "LOW" || firstOutput["causality_claimed"] != false {
		t.Fatalf("output=%#v", firstOutput)
	}
	app, stdout, stderr := testApp(project)
	app.Now = func() time.Time { return now }
	if code := app.Run(t.Context(), []string{"investigate", "--deployment", deploymentID, "--metric", "latency_p95", "--project-dir", project, "--json"}); code != 0 || stderr.Len() != 0 {
		t.Fatalf("investigate code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	cliOutput := decodeEnvelope(t, stdout.Bytes())["data"]
	cliJSON, _ := json.Marshal(cliOutput)
	mcpJSON, _ := json.Marshal(firstOutput)
	var cliSemantic, mcpSemantic any
	if err := json.Unmarshal(cliJSON, &cliSemantic); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(mcpJSON, &mcpSemantic); err != nil {
		t.Fatal(err)
	}
	cliCanonical, _ := json.Marshal(cliSemantic)
	mcpCanonical, _ := json.Marshal(mcpSemantic)
	if string(cliCanonical) != string(mcpCanonical) {
		t.Fatalf("MCP output differs from investigate projection\nMCP: %s\nCLI: %s", mcpCanonical, cliCanonical)
	}
	if strings.Contains(string(firstJSON), project) || strings.Contains(string(firstJSON), "external_id") || strings.Contains(string(firstJSON), "image_reference") || strings.Contains(string(firstJSON), "git_head") {
		t.Fatalf("MCP output exposed prohibited data: %s", firstJSON)
	}
	if after := mcpDatabaseState(t, databasePath); before != after {
		t.Fatalf("MCP tool changed SQLite logical state\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestMCPRealStdio(t *testing.T) {
	project := t.TempDir()
	deploymentID, _ := seedRegressionCLIDataWithMetrics(t, project, regressionCLIMetrics{requests: 20, errors: 1, p50NS: 250_000_000, p95NS: 250_000_000, p99NS: 250_000_000}, regressionCLIMetrics{requests: 30, errors: 1, p50NS: 300_000_000, p95NS: 300_000_000, p99NS: 300_000_000})
	databasePath := filepath.Join(project, ".prodmap", "prodmap.db")
	before := mcpDatabaseState(t, databasePath)

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	command := exec.Command(os.Args[0], "-test.run=^TestMCPStdioHelperProcess$", "--")
	command.Env = append(os.Environ(), "PRODMAP_MCP_STDIO_HELPER=1", "PRODMAP_MCP_PROJECT_DIR="+project)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	transport := &mcp.CommandTransport{Command: command, TerminateDuration: 3 * time.Second}
	client := mcp.NewClient(&mcp.Implementation{Name: "prodmap-stdio-test", Version: "v1"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("MCP handshake failed; stdout must contain only protocol messages: %v; stderr=%q", err, stderr.String())
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != mcpserver.ToolName {
		t.Fatalf("tools=%+v", tools.Tools)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: mcpserver.ToolName, Arguments: map[string]any{"deployment": deploymentID, "metric": "latency_p95"}})
	if err != nil || result.IsError || result.StructuredContent == nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatal(err)
	}
	if output["status"] != "AVAILABLE" || output["causality_claimed"] != false || output["regression"].(map[string]any)["classification"].(map[string]any)["result"] != "CANDIDATE" {
		t.Fatalf("output=%#v", output)
	}

	// A successful stdio handshake, list, and call prove the child emitted no
	// banner or log line on stdout: any non-protocol line would break framing.
	if err := session.Close(); err != nil {
		t.Fatalf("clean MCP shutdown failed: %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected helper stderr: %q", stderr.String())
	}
	if command.ProcessState == nil || !command.ProcessState.Exited() || !command.ProcessState.Success() {
		t.Fatalf("helper did not exit cleanly: %v", command.ProcessState)
	}
	if after := mcpDatabaseState(t, databasePath); before != after {
		t.Fatalf("stdio MCP server changed SQLite logical state\nbefore=%+v\nafter=%+v", before, after)
	}
}

type mcpState struct {
	MainHash      [sha256.Size]byte
	SchemaVersion int64
	UserVersion   int64
	Migrations    string
	Deployments   int64
	Windows       int64
}

func mcpDatabaseState(t *testing.T, path string) mcpState {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro&_query_only=1")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	state := mcpState{MainHash: sha256.Sum256(contents)}
	if err := db.QueryRow("PRAGMA schema_version").Scan(&state.SchemaVersion); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("PRAGMA user_version").Scan(&state.UserVersion); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query("SELECT version || ':' || name || ':' || checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var migrations []string
	for rows.Next() {
		var migration string
		if err := rows.Scan(&migration); err != nil {
			t.Fatal(err)
		}
		migrations = append(migrations, migration)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	state.Migrations = strings.Join(migrations, ",")
	if err := db.QueryRow("SELECT COUNT(*) FROM deployments").Scan(&state.Deployments); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM telemetry_windows").Scan(&state.Windows); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestMCPStdioHelperProcess(t *testing.T) {
	if os.Getenv("PRODMAP_MCP_STDIO_HELPER") != "1" {
		return
	}
	project := os.Getenv("PRODMAP_MCP_PROJECT_DIR")
	if project == "" {
		os.Exit(2)
	}
	app := NewApp(os.Stdout, os.Stderr, DefaultBuildInfo())
	os.Exit(app.Run(context.Background(), []string{"mcp", "serve", "--project-dir", project}))
}
