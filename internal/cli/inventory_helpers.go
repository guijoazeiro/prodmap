package cli

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/guijoazeiro/prodmap/internal/config"
	"github.com/guijoazeiro/prodmap/internal/errs"
	prodmapinventory "github.com/guijoazeiro/prodmap/internal/inventory"
	prodmapsqlite "github.com/guijoazeiro/prodmap/internal/sqlite"
)

const phase1FreshnessThreshold = 5 * time.Minute

type inventoryFlags struct {
	projectDir *string
	dataDir    *string
	jsonOutput *bool
}

func addInventoryFlags(flags *flag.FlagSet) inventoryFlags {
	return inventoryFlags{
		projectDir: flags.String("project-dir", "", "project directory"),
		dataDir:    flags.String("data-dir", "", "database path"),
		jsonOutput: flags.Bool("json", false, "emit JSON"),
	}
}

func (a *App) openInventory(ctx context.Context, common inventoryFlags) (config.Config, *prodmapsqlite.Store, error) {
	return a.openInventoryWith(ctx, common, prodmapsqlite.Open)
}

func (a *App) openInventoryReadOnly(ctx context.Context, common inventoryFlags) (config.Config, *prodmapsqlite.Store, error) {
	return a.openInventoryWith(ctx, common, prodmapsqlite.OpenReadOnly)
}

func (a *App) openInventoryWith(ctx context.Context, common inventoryFlags, open func(context.Context, string) (*prodmapsqlite.Store, error)) (config.Config, *prodmapsqlite.Store, error) {
	if *common.projectDir == "" {
		cwd, err := a.currentWorkingDir()
		if err != nil {
			return config.Config{}, nil, err
		}
		*common.projectDir = cwd
	}
	cfg, err := a.loadConfig(*common.projectDir, *common.dataDir)
	if err != nil {
		return config.Config{}, nil, fmt.Errorf("load configuration: %w", err)
	}
	store, err := open(ctx, cfg.DataDir)
	if err != nil {
		return config.Config{}, nil, fmt.Errorf("open sqlite: %w", err)
	}
	return cfg, store, nil
}

func parseRFC3339(value, flagName string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be RFC3339: %w", flagName, errs.ErrInvalid)
	}
	return parsed.UTC(), nil
}

func freshnessWarnings(now time.Time, status prodmapinventory.Status) []string {
	if status.LastSnapshot == nil {
		return []string{"No Docker runtime snapshot has been collected; run `prodmap runtime --refresh`."}
	}
	age := now.UTC().Sub(status.LastSnapshot.UTC())
	if age < 0 {
		return []string{"The latest runtime snapshot is in the future relative to the local clock."}
	}
	if age > phase1FreshnessThreshold {
		return []string{fmt.Sprintf("The latest runtime snapshot is stale (%s old; Phase 1 threshold is %s).", age.Round(time.Second), phase1FreshnessThreshold)}
	}
	return nil
}

func pointer(value string) *string {
	if value == "" {
		return nil
	}
	copy := value
	return &copy
}
