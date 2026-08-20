package cli

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/guijoazeiro/prodmap/internal/correlation"
	"github.com/guijoazeiro/prodmap/internal/errs"
)

type serviceOutput struct {
	ID               string            `json:"id"`
	LogicalKey       string            `json:"logical_key"`
	Environment      string            `json:"environment"`
	DisplayName      string            `json:"display_name"`
	RuntimeInstances int               `json:"runtime_instances"`
	State            string            `json:"state"`
	Health           string            `json:"health"`
	Artifact         *string           `json:"artifact"`
	CommitConfidence correlation.Level `json:"commit_confidence"`
	Freshness        string            `json:"freshness"`
}

type servicesResult struct {
	Items []serviceOutput `json:"items"`
}

func (a *App) runServices(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("services", flag.ContinueOnError)
	common := addInventoryFlags(flags)
	help, err := a.parseCommandFlags(flags, args)
	if help {
		return nil
	}
	if err != nil {
		return fmt.Errorf("parse services flags: %w: %v", errs.ErrInvalid, err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("services accepts no positional arguments: %w", errs.ErrInvalid)
	}
	_, store, err := a.openInventory(ctx, common)
	if err != nil {
		return err
	}
	defer store.Close()
	items, err := store.Services(ctx)
	if err != nil {
		return fmt.Errorf("list services: %w", err)
	}
	result := servicesResult{Items: make([]serviceOutput, 0, len(items))}
	for _, item := range items {
		result.Items = append(result.Items, serviceOutput{
			ID: item.ID, LogicalKey: item.LogicalKey, Environment: item.Environment, DisplayName: item.DisplayName,
			RuntimeInstances: item.RuntimeInstances, State: item.State, Health: normalizeUnknown(item.Health),
			Artifact: pointer(item.ArtifactIdentity), CommitConfidence: item.CommitConfidence,
			Freshness: item.Freshness.UTC().Format(time.RFC3339Nano),
		})
	}
	if *common.jsonOutput {
		return WriteSuccess(a.Stdout, "services", a.Now(), result, nil, nil)
	}
	if len(result.Items) == 0 {
		fmt.Fprintln(a.Stdout, "No services found. Run `prodmap runtime --refresh` to inspect Docker.")
		return nil
	}
	for _, item := range result.Items {
		fmt.Fprintf(a.Stdout, "%s (%s) environment=%s instances=%d state=%s health=%s artifact=%s commit=%s freshness=%s\n",
			item.DisplayName, item.ID, item.Environment, item.RuntimeInstances, item.State, item.Health,
			displayOptional(item.Artifact), item.CommitConfidence, item.Freshness)
	}
	return nil
}

func normalizeUnknown(value string) string {
	if value == "" || value == "unknown" {
		return "UNKNOWN"
	}
	return value
}
