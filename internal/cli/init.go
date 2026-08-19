package cli

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"

	"github.com/guijoazeiro/prodmap/internal/config"
	"github.com/guijoazeiro/prodmap/internal/errs"
	prodmapsqlite "github.com/guijoazeiro/prodmap/internal/sqlite"
)

type initResult struct {
	ProjectDir       string   `json:"project_dir"`
	ConfigPath       string   `json:"config_path"`
	DatabasePath     string   `json:"database_path"`
	ConfigCreated    bool     `json:"config_created"`
	MigrationVersion int64    `json:"migration_version"`
	NextSteps        []string `json:"next_steps"`
}

func (a *App) runInit(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(a.Stderr)
	projectDir := flags.String("project-dir", "", "project directory")
	dataDir := flags.String("data-dir", "", "database path")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	interactive := flags.Bool("interactive", false, "allow interactive mode")
	force := flags.Bool("force", false, "preserve existing files and re-run initialization")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parse init flags: %w: %v", errs.ErrInvalid, err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("init accepts no positional arguments: %w", errs.ErrInvalid)
	}
	_ = interactive // The Foundation has no prompts; the flag only makes future intent explicit.
	_ = force       // Re-running is always safe and never overwrites configuration or the database.

	if *projectDir == "" {
		cwd, err := a.currentWorkingDir()
		if err != nil {
			return err
		}
		*projectDir = cwd
	}
	cfg, err := a.loadConfig(*projectDir, *dataDir)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	logger, err := a.commandLogger(cfg, operationID())
	if err != nil {
		return err
	}
	started := a.Now()

	created, err := config.EnsureProjectConfig(cfg)
	if err != nil {
		return fmt.Errorf("initialize project configuration: %w", err)
	}
	store, err := prodmapsqlite.Open(ctx, cfg.DataDir)
	if err != nil {
		return fmt.Errorf("initialize sqlite: %w", err)
	}
	defer store.Close()
	status, err := store.MigrationStatus(ctx)
	if err != nil {
		return fmt.Errorf("read migration status: %w", err)
	}

	result := initResult{
		ProjectDir:       cfg.ProjectDir,
		ConfigPath:       filepath.Join(cfg.ProjectDir, ".prodmap", "config.yaml"),
		DatabasePath:     cfg.DataDir,
		ConfigCreated:    created,
		MigrationVersion: status.AppliedVersion,
		NextSteps:        []string{"run `prodmap doctor` to verify the local Foundation"},
	}
	logger.Debug("project initialized", "duration", a.Now().Sub(started), "config_created", created)
	if *jsonOutput {
		return WriteSuccess(a.Stdout, "init", a.Now(), result, nil, nil)
	}
	_, err = fmt.Fprintf(a.Stdout, "Prodmap initialized in %s\nconfig: %s\ndatabase: %s\nmigration: %d\nnext: %s\n",
		result.ProjectDir, result.ConfigPath, result.DatabasePath, result.MigrationVersion, result.NextSteps[0])
	return err
}
