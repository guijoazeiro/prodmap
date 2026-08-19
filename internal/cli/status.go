package cli

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/guijoazeiro/prodmap/internal/errs"
)

type statusDependency struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

type statusResult struct {
	Config           statusDependency `json:"config"`
	Database         statusDependency `json:"database"`
	Migrations       statusDependency `json:"migrations"`
	Git              statusDependency `json:"git"`
	Docker           statusDependency `json:"docker"`
	LastSnapshot     *string          `json:"last_runtime_snapshot"`
	Services         int              `json:"services"`
	RuntimeInstances int              `json:"runtime_instances"`
}

func (a *App) runStatus(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(a.Stderr)
	common := addInventoryFlags(flags)
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parse status flags: %w: %v", errs.ErrInvalid, err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("status accepts no positional arguments: %w", errs.ErrInvalid)
	}
	_, store, err := a.openInventory(ctx, common)
	if err != nil {
		return err
	}
	defer store.Close()
	migrations, err := store.MigrationStatus(ctx)
	if err != nil {
		return fmt.Errorf("read migration status: %w", err)
	}
	stored, err := store.Status(ctx)
	if err != nil {
		return fmt.Errorf("read inventory status: %w", err)
	}
	gitCheck := a.externalCheck(ctx, "git.available", "git", "--version")
	dockerCheck := a.externalCheck(ctx, "docker.available", "docker", "version", "--format", "{{.Server.Version}}")
	warnings := freshnessWarnings(a.Now(), stored)
	for _, check := range []doctorCheck{gitCheck, dockerCheck} {
		if check.Status != "pass" {
			warnings = append(warnings, check.Message)
		}
	}
	result := statusResult{
		Config:     statusDependency{Status: "pass", Message: "Configuration schema 1.0 is supported"},
		Database:   statusDependency{Status: "pass", Message: "SQLite database opened"},
		Migrations: statusDependency{Status: map[bool]string{true: "pass", false: "warning"}[migrations.Current], Message: fmt.Sprintf("Migration version %d of %d", migrations.AppliedVersion, migrations.Available)},
		Git:        statusDependency{Status: gitCheck.Status, Message: gitCheck.Message},
		Docker:     statusDependency{Status: dockerCheck.Status, Message: dockerCheck.Message},
		Services:   stored.Services, RuntimeInstances: stored.RuntimeInstances,
	}
	if stored.LastSnapshot != nil {
		value := stored.LastSnapshot.UTC().Format(time.RFC3339Nano)
		result.LastSnapshot = &value
	}
	if *common.jsonOutput {
		return WriteSuccess(a.Stdout, "status", a.Now(), result, warnings, nil)
	}
	fmt.Fprintf(a.Stdout, "Prodmap status\nconfig: %s — %s\ndatabase: %s — %s\nmigrations: %s — %s\ngit: %s — %s\ndocker: %s — %s\nlast runtime snapshot: %s\nservices: %d\nruntime instances: %d\n",
		result.Config.Status, result.Config.Message, result.Database.Status, result.Database.Message,
		result.Migrations.Status, result.Migrations.Message, result.Git.Status, result.Git.Message,
		result.Docker.Status, result.Docker.Message, displayOptional(result.LastSnapshot), result.Services, result.RuntimeInstances)
	for _, warning := range warnings {
		fmt.Fprintf(a.Stdout, "warning: %s\n", warning)
	}
	return nil
}

func displayOptional(value *string) string {
	if value == nil {
		return "UNKNOWN"
	}
	return *value
}
