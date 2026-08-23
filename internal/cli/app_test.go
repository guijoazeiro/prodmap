package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/inventory"
)

func TestAppInitIsIdempotentAndDoctorEmitsValidJSON(t *testing.T) {
	projectDir := t.TempDir()
	app, stdout, stderr := testApp(projectDir)

	if code := app.Run(context.Background(), []string{"init", "--json", "--project-dir", projectDir}); code != 0 {
		t.Fatalf("first init exit = %d, stderr = %q", code, stderr.String())
	}
	first := decodeEnvelope(t, stdout.Bytes())
	data := first["data"].(map[string]any)
	if created, _ := data["config_created"].(bool); !created {
		t.Fatal("first init did not create configuration")
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".prodmap", "prodmap.db")); err != nil {
		t.Fatalf("database not created: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), []string{"init", "--json", "--project-dir", projectDir}); code != 0 {
		t.Fatalf("second init exit = %d, stderr = %q", code, stderr.String())
	}
	second := decodeEnvelope(t, stdout.Bytes())
	data = second["data"].(map[string]any)
	if created, _ := data["config_created"].(bool); created {
		t.Fatal("second init overwrote configuration")
	}

	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), []string{"doctor", "--json", "--project-dir", projectDir}); code != 0 {
		t.Fatalf("doctor exit = %d, stderr = %q", code, stderr.String())
	}
	doctor := decodeEnvelope(t, stdout.Bytes())
	if doctor["command"] != "doctor" {
		t.Fatalf("doctor command = %v", doctor["command"])
	}
	if stderr.Len() != 0 {
		t.Fatalf("normal JSON doctor wrote stderr: %q", stderr.String())
	}
}

func TestAppInvalidConfigurationUsesExitThreeAndJSONError(t *testing.T) {
	projectDir := t.TempDir()
	configDir := filepath.Join(projectDir, ".prodmap")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("schema_version: \"1.0\"\nunknown: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	app, stdout, _ := testApp(projectDir)

	if code := app.Run(context.Background(), []string{"init", "--json", "--project-dir", projectDir}); code != 3 {
		t.Fatalf("exit = %d, want 3", code)
	}
	envelope := decodeEnvelope(t, stdout.Bytes())
	payload := envelope["error"].(map[string]any)
	if payload["code"] != ErrorCodeInvalidConfig {
		t.Fatalf("error code = %v", payload["code"])
	}
	if _, err := os.Stat(filepath.Join(configDir, "prodmap.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid config created database, stat err = %v", err)
	}
}

func TestAppVersionJSONUsesInjectedBuildInfo(t *testing.T) {
	app, stdout, stderr := testApp(t.TempDir())
	app.BuildInfo = BuildInfo{Version: "1.2.3", Commit: "abc", BuildDate: "2026-08-19", GoVersion: "go-test", SchemaVersion: SchemaVersion}
	if code := app.Run(context.Background(), []string{"version", "--json"}); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	envelope := decodeEnvelope(t, stdout.Bytes())
	data := envelope["data"].(map[string]any)
	if data["version"] != "1.2.3" || data["commit"] != "abc" {
		t.Fatalf("version data = %#v", data)
	}
}

func TestAllCommandsHelpIsSuccessfulAndSideEffectFree(t *testing.T) {
	commands := []string{"init", "doctor", "status", "services", "runtime", "explain", "version", "graph"}
	for _, command := range commands {
		for _, args := range [][]string{{command, "--help"}, {command, "--json", "--help"}} {
			name := command + " help"
			if len(args) == 3 {
				name += " with json flag"
			}
			t.Run(name, func(t *testing.T) {
				projectDir := t.TempDir()
				app, stdout, stderr := testApp(projectDir)
				app.WorkingDir = func() (string, error) {
					t.Fatal("help executed working-directory discovery")
					return "", errors.New("unreachable")
				}
				app.LookPath = func(string) (string, error) {
					t.Fatal("help executed dependency discovery")
					return "", errors.New("unreachable")
				}
				app.RuntimeSource = func() inventory.RuntimeSource {
					t.Fatal("help constructed the Docker runtime source")
					return nil
				}

				if code := app.Run(context.Background(), args); code != 0 {
					t.Fatalf("%v exit = %d, stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
				}
				if !strings.Contains(stdout.String(), "Usage of "+command+":") {
					t.Fatalf("%v stdout does not contain command help: %q", args, stdout.String())
				}
				if strings.Contains(stdout.String(), "flag: help requested") || strings.Contains(stdout.String(), `"error"`) {
					t.Fatalf("%v rendered help as an error: %q", args, stdout.String())
				}
				if stderr.Len() != 0 {
					t.Fatalf("%v stderr = %q, want empty", args, stderr.String())
				}
				if _, err := os.Stat(filepath.Join(projectDir, ".prodmap")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("%v changed project state, stat error = %v", args, err)
				}
			})
		}
	}
}

func TestTelemetryIngestHelpIsSuccessfulAndSideEffectFree(t *testing.T) {
	projectDir := t.TempDir()
	app, stdout, stderr := testApp(projectDir)
	app.WorkingDir = func() (string, error) {
		t.Fatal("telemetry help executed working-directory discovery")
		return "", errors.New("unreachable")
	}
	if code := app.Run(context.Background(), []string{"telemetry", "ingest", "--json", "--help"}); code != 0 {
		t.Fatalf("help exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "Usage of telemetry ingest:") || stderr.Len() != 0 {
		t.Fatalf("help streams stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".prodmap")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("help changed project state: %v", err)
	}
}

func TestRootAndTelemetryHelpAreSuccessfulAndSideEffectFree(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"telemetry", "--help"}, {"telemetry", "-h"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			projectDir := t.TempDir()
			app, stdout, stderr := testApp(projectDir)
			app.WorkingDir = func() (string, error) {
				t.Fatal("help executed working-directory discovery")
				return "", errors.New("unreachable")
			}
			if code := app.Run(context.Background(), args); code != 0 {
				t.Fatalf("help exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String(), "usage: prodmap") || stderr.Len() != 0 {
				t.Fatalf("help streams stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
			if _, err := os.Stat(filepath.Join(projectDir, ".prodmap")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("help changed project state: %v", err)
			}
		})
	}
}

func TestContainsJSONFlagMatchesFlagParsingOrderAndTerminator(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want bool
	}{
		{name: "last bare true", args: []string{"--json=false", "--json"}, want: true},
		{name: "last explicit false", args: []string{"--json", "--json=false"}, want: false},
		{name: "single dash last wins", args: []string{"-json=false", "-json=true"}, want: true},
		{name: "terminator", args: []string{"--", "--json"}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := containsJSONFlag(test.args); got != test.want {
				t.Fatalf("containsJSONFlag(%v)=%t want=%t", test.args, got, test.want)
			}
		})
	}
}

func TestRepeatedJSONFlagsAndTerminatorSelectTheCorrectErrorStream(t *testing.T) {
	for _, test := range []struct {
		name     string
		args     []string
		wantJSON bool
	}{
		{name: "last bare true", args: []string{"graph", "--json=false", "--json", "--unknown"}, wantJSON: true},
		{name: "last explicit false", args: []string{"graph", "--json", "--json=false", "--unknown"}, wantJSON: false},
		{name: "terminator", args: []string{"graph", "--", "--json"}, wantJSON: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, stdout, stderr := testApp(t.TempDir())
			if code := app.Run(context.Background(), test.args); code != 2 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if test.wantJSON {
				decodeEnvelope(t, stdout.Bytes())
				if stderr.Len() != 0 {
					t.Fatalf("JSON error wrote stderr=%q", stderr.String())
				}
			} else if stdout.Len() != 0 || stderr.Len() == 0 {
				t.Fatalf("human error streams stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestInvalidFlagWithJSONWritesOnlyErrorEnvelopeToStdout(t *testing.T) {
	projectDir := t.TempDir()
	app, stdout, stderr := testApp(projectDir)
	if code := app.Run(context.Background(), []string{"graph", "--json", "--unknown", "--project-dir", projectDir}); code != 2 {
		t.Fatalf("invalid flag exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("JSON flag error wrote stderr=%q", stderr.String())
	}
	envelope := decodeEnvelope(t, stdout.Bytes())
	if envelope["command"] != "graph" || envelope["error"].(map[string]any)["code"] != ErrorCodeInvalidArgument {
		t.Fatalf("invalid flag envelope=%#v", envelope)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".prodmap")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid flag changed project state: %v", err)
	}
}

func testApp(projectDir string) (*App, *bytes.Buffer, *bytes.Buffer) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	app := NewApp(stdout, stderr, DefaultBuildInfo())
	app.Now = func() time.Time { return time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC) }
	app.WorkingDir = func() (string, error) { return projectDir, nil }
	app.Environment = map[string]string{}
	app.UserConfigPath = filepath.Join(projectDir, "absent-user-config.yaml")
	app.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
	app.RunExternal = func(context.Context, string, ...string) error { return nil }
	return app, stdout, stderr
}

func decodeEnvelope(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("invalid JSON %q: %v", raw, err)
	}
	return value
}
