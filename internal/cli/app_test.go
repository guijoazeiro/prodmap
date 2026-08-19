package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
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
