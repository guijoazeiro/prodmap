// Package config loads Prodmap configuration from explicit, ordered layers.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/guijoazeiro/prodmap/internal/errs"
	"go.yaml.in/yaml/v3"
)

const SchemaVersion = "1.0"

type Log struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

type Config struct {
	SchemaVersion string `yaml:"schema_version"`
	ProjectDir    string `yaml:"project_dir"`
	DataDir       string `yaml:"data_dir"`
	Log           Log    `yaml:"log"`
}

// Redacted returns the effective non-secret configuration for diagnostics.
// Phase 0 accepts no secret-bearing configuration fields.
func (c Config) Redacted() map[string]any {
	return map[string]any{
		"schema_version": c.SchemaVersion,
		"project_dir":    c.ProjectDir,
		"data_dir":       c.DataDir,
		"log": map[string]string{
			"level":  c.Log.Level,
			"format": c.Log.Format,
		},
	}
}

// Overrides uses pointers so an absent value differs from an explicitly empty one.
type Overrides struct {
	ProjectDir *string
	DataDir    *string
	LogLevel   *string
	LogFormat  *string
}

type Options struct {
	ProjectDir     string
	UserConfigPath string
	Env            map[string]string
	Overrides      Overrides
}

type document struct {
	SchemaVersion *string `yaml:"schema_version"`
	ProjectDir    *string `yaml:"project_dir"`
	DataDir       *string `yaml:"data_dir"`
	Log           *struct {
		Level  *string `yaml:"level"`
		Format *string `yaml:"format"`
	} `yaml:"log"`
}

func Load(opts Options) (_ Config, err error) {
	defer func() {
		if err == nil {
			return
		}
		var configErr *errs.ConfigError
		if !errors.As(err, &configErr) {
			err = &errs.ConfigError{Err: err}
		}
	}()

	projectDir := opts.ProjectDir
	if projectDir == "" {
		var err error
		projectDir, err = os.Getwd()
		if err != nil {
			return Config{}, fmt.Errorf("determine project directory: %w", errs.ErrInvalid)
		}
	}
	projectDir, err = filepath.Abs(projectDir)
	if err != nil {
		return Config{}, fmt.Errorf("resolve project directory: %w", errs.ErrInvalid)
	}
	cfg := Config{SchemaVersion: SchemaVersion, ProjectDir: projectDir, DataDir: ".prodmap/prodmap.db", Log: Log{Level: "info", Format: "text"}}

	if opts.UserConfigPath != "" {
		if err := applyFile(&cfg, opts.UserConfigPath, true); err != nil {
			return Config{}, err
		}
	}
	// Higher-precedence project directory selectors decide where project config lives.
	if value, ok := opts.Env["PRODMAP_PROJECT_DIR"]; ok {
		cfg.ProjectDir = value
	}
	if opts.Overrides.ProjectDir != nil {
		cfg.ProjectDir = *opts.Overrides.ProjectDir
	}
	if cfg.ProjectDir == "" || !filepath.IsAbs(cfg.ProjectDir) {
		return Config{}, fmt.Errorf("project_dir must be an absolute non-empty path: %w", errs.ErrInvalid)
	}
	selectedProjectDir := filepath.Clean(cfg.ProjectDir)
	if err := applyFile(&cfg, filepath.Join(selectedProjectDir, ".prodmap", "config.yaml"), true); err != nil {
		return Config{}, err
	}
	applyEnv(&cfg, opts.Env)
	applyOverrides(&cfg, opts.Overrides)
	if err := validate(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func applyFile(cfg *Config, path string, optional bool) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if optional && errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read config %q: %w", path, err)
	}
	var doc document
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return fmt.Errorf("decode config %q: %v: %w", path, err, errs.ErrInvalid)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return fmt.Errorf("decode config %q: multiple YAML documents are not allowed: %w", path, errs.ErrInvalid)
	}
	if doc.SchemaVersion == nil || *doc.SchemaVersion != SchemaVersion {
		return fmt.Errorf("config %q schema_version must be %q: %w", path, SchemaVersion, errs.ErrIncompatible)
	}
	applyDocument(cfg, doc)
	return nil
}

func applyDocument(cfg *Config, doc document) {
	cfg.SchemaVersion = *doc.SchemaVersion
	if doc.ProjectDir != nil {
		cfg.ProjectDir = *doc.ProjectDir
	}
	if doc.DataDir != nil {
		cfg.DataDir = *doc.DataDir
	}
	if doc.Log != nil {
		if doc.Log.Level != nil {
			cfg.Log.Level = *doc.Log.Level
		}
		if doc.Log.Format != nil {
			cfg.Log.Format = *doc.Log.Format
		}
	}
}

func applyEnv(cfg *Config, env map[string]string) {
	if v, ok := env["PRODMAP_PROJECT_DIR"]; ok {
		cfg.ProjectDir = v
	}
	if v, ok := env["PRODMAP_DATA_DIR"]; ok {
		cfg.DataDir = v
	}
	if v, ok := env["PRODMAP_LOG_LEVEL"]; ok {
		cfg.Log.Level = v
	}
	if v, ok := env["PRODMAP_LOG_FORMAT"]; ok {
		cfg.Log.Format = v
	}
}

func applyOverrides(cfg *Config, o Overrides) {
	if o.ProjectDir != nil {
		cfg.ProjectDir = *o.ProjectDir
	}
	if o.DataDir != nil {
		cfg.DataDir = *o.DataDir
	}
	if o.LogLevel != nil {
		cfg.Log.Level = *o.LogLevel
	}
	if o.LogFormat != nil {
		cfg.Log.Format = *o.LogFormat
	}
}

func validate(cfg *Config) error {
	if cfg.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schema version %q: %w", cfg.SchemaVersion, errs.ErrIncompatible)
	}
	if cfg.ProjectDir == "" || strings.ContainsRune(cfg.ProjectDir, 0) || !filepath.IsAbs(cfg.ProjectDir) {
		return fmt.Errorf("project_dir must be an absolute non-empty path: %w", errs.ErrInvalid)
	}
	cfg.ProjectDir = filepath.Clean(cfg.ProjectDir)
	if strings.TrimSpace(cfg.DataDir) == "" || strings.ContainsRune(cfg.DataDir, 0) {
		return fmt.Errorf("data_dir must not be empty: %w", errs.ErrInvalid)
	}
	if !filepath.IsAbs(cfg.DataDir) {
		cfg.DataDir = filepath.Join(cfg.ProjectDir, cfg.DataDir)
	}
	cfg.DataDir = filepath.Clean(cfg.DataDir)
	if cfg.Log.Level != "debug" && cfg.Log.Level != "info" && cfg.Log.Level != "warn" && cfg.Log.Level != "error" {
		return fmt.Errorf("invalid log level %q: %w", cfg.Log.Level, errs.ErrInvalid)
	}
	if cfg.Log.Format != "text" && cfg.Log.Format != "json" {
		return fmt.Errorf("invalid log format %q: %w", cfg.Log.Format, errs.ErrInvalid)
	}
	return nil
}

// EnsureProjectConfig atomically creates .prodmap/config.yaml if absent.
// It returns true only when it creates the file and never overwrites an existing file.
func EnsureProjectConfig(cfg Config) (bool, error) {
	if err := validate(&cfg); err != nil {
		return false, err
	}
	dir := filepath.Join(cfg.ProjectDir, ".prodmap")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return false, fmt.Errorf("create config directory: %w", err)
	}
	target := filepath.Join(dir, "config.yaml")
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return false, fmt.Errorf("encode project config: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return false, fmt.Errorf("create temporary config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return false, fmt.Errorf("set config permissions: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return false, fmt.Errorf("write temporary config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return false, fmt.Errorf("sync temporary config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("close temporary config: %w", err)
	}
	// Link provides atomic create-if-absent semantics; rename would overwrite.
	if err := os.Link(tmpName, target); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, fmt.Errorf("install project config: %w", err)
	}
	return true, nil
}
