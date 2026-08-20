package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/guijoazeiro/prodmap/internal/errs"
	prodmapsqlite "github.com/guijoazeiro/prodmap/internal/sqlite"
)

type doctorCheck struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Message    string `json:"message"`
	DurationMS int64  `json:"duration_ms"`
}

type doctorResult struct {
	Status string        `json:"status"`
	Checks []doctorCheck `json:"checks"`
}

func (a *App) runDoctor(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	projectDir := flags.String("project-dir", "", "project directory")
	dataDir := flags.String("data-dir", "", "database path")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	strict := flags.Bool("strict", false, "fail when any check is not pass")
	help, err := a.parseCommandFlags(flags, args)
	if help {
		return nil
	}
	if err != nil {
		return fmt.Errorf("parse doctor flags: %w: %v", errs.ErrInvalid, err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("doctor accepts no positional arguments: %w", errs.ErrInvalid)
	}
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
	checks := []doctorCheck{{ID: "config.schema", Status: "pass", Message: "Configuration schema 1.0 is supported"}}
	checks = append(checks, a.directoryCheck("project.directory", cfg.ProjectDir, false))
	checks = append(checks, a.directoryCheck("data.directory", filepath.Dir(cfg.DataDir), true))

	store, openErr := prodmapsqlite.Open(ctx, cfg.DataDir)
	if openErr != nil {
		checks = append(checks,
			doctorCheck{ID: "sqlite.open", Status: "fail", Message: openErr.Error()},
			doctorCheck{ID: "sqlite.migrations", Status: "fail", Message: "Migration state unavailable because SQLite could not be opened"},
		)
	} else {
		checks = append(checks, doctorCheck{ID: "sqlite.open", Status: "pass", Message: "SQLite database opened"})
		status, statusErr := store.MigrationStatus(ctx)
		if statusErr != nil {
			checks = append(checks, doctorCheck{ID: "sqlite.migrations", Status: "fail", Message: statusErr.Error()})
		} else if !status.Current {
			checks = append(checks, doctorCheck{ID: "sqlite.migrations", Status: "fail", Message: "Database migrations are not current"})
		} else {
			checks = append(checks, doctorCheck{ID: "sqlite.migrations", Status: "pass", Message: fmt.Sprintf("Migrations current at version %d", status.AppliedVersion)})
		}
		_ = store.Close()
	}
	checks = append(checks, a.externalCheck(ctx, "git.available", "git", "--version"))
	checks = append(checks, a.externalCheck(ctx, "docker.available", "docker", "version", "--format", "{{.Server.Version}}"))
	checks = append(checks, hotReloadCheck(cfg.ProjectDir))

	overall := "pass"
	warnings := []string{}
	for _, check := range checks {
		if check.Status != "pass" {
			overall = "warning"
			warnings = append(warnings, check.ID+": "+check.Message)
		}
	}
	result := doctorResult{Status: overall, Checks: checks}
	logger.Debug("doctor completed", "duration", a.Now().Sub(started), "status", overall)
	if *jsonOutput {
		if err := WriteSuccess(a.Stdout, "doctor", a.Now(), result, warnings, nil); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(a.Stdout, "Prodmap doctor: %s\n", overall)
		for _, check := range checks {
			fmt.Fprintf(a.Stdout, "[%s] %s — %s\n", check.Status, check.ID, check.Message)
		}
	}
	if *strict && overall != "pass" {
		return &renderedError{err: fmt.Errorf("doctor checks reported warnings: %w", errs.ErrUnavailable)}
	}
	return nil
}

func (a *App) directoryCheck(id, path string, optional bool) doctorCheck {
	started := time.Now()
	status := "pass"
	message := "Directory is accessible"
	info, err := os.Stat(path)
	if err != nil {
		status = "fail"
		if optional && os.IsNotExist(err) {
			status = "warning"
			message = "Directory does not exist; run `prodmap init`"
		} else {
			message = err.Error()
		}
	} else if !info.IsDir() {
		status = "fail"
		message = "Path is not a directory"
	} else {
		probe, probeErr := os.CreateTemp(path, ".prodmap-doctor-*")
		if probeErr != nil {
			status = "fail"
			message = probeErr.Error()
		} else {
			name := probe.Name()
			_ = probe.Close()
			_ = os.Remove(name)
		}
	}
	return doctorCheck{ID: id, Status: status, Message: message, DurationMS: time.Since(started).Milliseconds()}
}

func (a *App) externalCheck(parent context.Context, id, command string, args ...string) doctorCheck {
	started := time.Now()
	if _, err := a.LookPath(command); err != nil {
		return doctorCheck{ID: id, Status: "warning", Message: command + " is not installed", DurationMS: time.Since(started).Milliseconds()}
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	if err := a.RunExternal(ctx, command, args...); err != nil {
		return doctorCheck{ID: id, Status: "warning", Message: command + " is installed but unavailable", DurationMS: time.Since(started).Milliseconds()}
	}
	return doctorCheck{ID: id, Status: "pass", Message: command + " is available", DurationMS: time.Since(started).Milliseconds()}
}

func hotReloadCheck(projectDir string) doctorCheck {
	started := time.Now()
	for _, name := range []string{".air.toml", "Makefile", "go.mod"} {
		if _, err := os.Stat(filepath.Join(projectDir, name)); err != nil {
			return doctorCheck{ID: "development.hot_reload", Status: "warning", Message: "Hot reload configuration is incomplete", DurationMS: time.Since(started).Milliseconds()}
		}
	}
	return doctorCheck{ID: "development.hot_reload", Status: "pass", Message: "Hot reload configuration is present", DurationMS: time.Since(started).Milliseconds()}
}
