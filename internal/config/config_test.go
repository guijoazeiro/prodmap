package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/guijoazeiro/prodmap/internal/errs"
)

func ptr(s string) *string { return &s }

func writeConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDefaults(t *testing.T) {
	project := t.TempDir()
	cfg, err := Load(Options{ProjectDir: project})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SchemaVersion != SchemaVersion || cfg.ProjectDir != project || cfg.DataDir != filepath.Join(project, ".prodmap", "prodmap.db") || cfg.Log.Level != "info" || cfg.Log.Format != "text" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestRedactedContainsOnlySupportedNonSecretFields(t *testing.T) {
	cfg := Config{SchemaVersion: SchemaVersion, ProjectDir: "/project", DataDir: "/project/data.db", Log: Log{Level: "info", Format: "json"}}
	got := cfg.Redacted()
	if len(got) != 4 || got["schema_version"] != SchemaVersion {
		t.Fatalf("Redacted() = %#v", got)
	}
}

func TestLoadPrecedence(t *testing.T) {
	base := t.TempDir()
	userProject := filepath.Join(base, "user-project")
	envProject := filepath.Join(base, "env-project")
	flagProject := filepath.Join(base, "flag-project")
	user := filepath.Join(base, "explicit-user.yaml")
	writeConfig(t, user, "schema_version: '1.0'\nproject_dir: "+userProject+"\ndata_dir: user.db\nlog:\n  level: debug\n  format: json\n")
	writeConfig(t, filepath.Join(flagProject, ".prodmap", "config.yaml"), "schema_version: '1.0'\ndata_dir: project.db\nlog:\n  level: warn\n  format: text\n")
	cfg, err := Load(Options{
		ProjectDir: base, UserConfigPath: user,
		Env:       map[string]string{"PRODMAP_PROJECT_DIR": envProject, "PRODMAP_DATA_DIR": "env.db", "PRODMAP_LOG_LEVEL": "error", "PRODMAP_LOG_FORMAT": "json"},
		Overrides: Overrides{ProjectDir: &flagProject, DataDir: ptr("flag.db"), LogLevel: ptr("info")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProjectDir != flagProject || cfg.DataDir != filepath.Join(flagProject, "flag.db") || cfg.Log.Level != "info" || cfg.Log.Format != "json" {
		t.Fatalf("precedence mismatch: %+v", cfg)
	}
}

func TestLoadRejectsUnknownFieldAndBadSchema(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		category   error
	}{
		{"unknown", "schema_version: '1.0'\nunknown: true\n", errs.ErrInvalid},
		{"secret field", "schema_version: '1.0'\ntoken: must-not-be-accepted\n", errs.ErrInvalid},
		{"missing schema", "log:\n  level: info\n", errs.ErrIncompatible},
		{"bad schema", "schema_version: '2.0'\n", errs.ErrIncompatible},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := t.TempDir()
			writeConfig(t, filepath.Join(project, ".prodmap", "config.yaml"), tc.body)
			_, err := Load(Options{ProjectDir: project})
			if !errors.Is(err, tc.category) {
				t.Fatalf("error %v does not wrap %v", err, tc.category)
			}
		})
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	project := t.TempDir()
	for name, override := range map[string]Overrides{
		"empty data":       {DataDir: ptr("")},
		"nul data":         {DataDir: ptr("bad\x00path")},
		"relative project": {ProjectDir: ptr("relative")},
		"bad level":        {LogLevel: ptr("verbose")},
		"bad format":       {LogFormat: ptr("xml")},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(Options{ProjectDir: project, Overrides: override})
			if !errors.Is(err, errs.ErrInvalid) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestExplicitUserPathDoesNotUseHome(t *testing.T) {
	project := t.TempDir()
	user := filepath.Join(t.TempDir(), "chosen.yaml")
	writeConfig(t, user, "schema_version: '1.0'\nlog:\n  level: debug\n")
	t.Setenv("HOME", filepath.Join(t.TempDir(), "must-not-be-read"))
	cfg, err := Load(Options{ProjectDir: project, UserConfigPath: user})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Log.Level != "debug" {
		t.Fatalf("explicit user config ignored: %+v", cfg)
	}
}

func TestEnsureProjectConfigIsIdempotentAndDoesNotOverwrite(t *testing.T) {
	project := t.TempDir()
	cfg, err := Load(Options{ProjectDir: project})
	if err != nil {
		t.Fatal(err)
	}
	created, err := EnsureProjectConfig(cfg)
	if err != nil || !created {
		t.Fatalf("first create: created=%v err=%v", created, err)
	}
	path := filepath.Join(project, ".prodmap", "config.yaml")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	created, err = EnsureProjectConfig(Config{SchemaVersion: SchemaVersion, ProjectDir: project, DataDir: "different.db", Log: Log{Level: "debug", Format: "json"}})
	if err != nil || created {
		t.Fatalf("second create: created=%v err=%v", created, err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(original) {
		t.Fatal("existing config was overwritten")
	}
}
