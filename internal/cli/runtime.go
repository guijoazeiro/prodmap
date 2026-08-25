package cli

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/guijoazeiro/prodmap/internal/correlation"
	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/identity"
	"github.com/guijoazeiro/prodmap/internal/inventory"
)

type runtimeOutput struct {
	ID               string            `json:"id"`
	ExternalID       string            `json:"external_id"`
	ContainerName    string            `json:"container_name"`
	ServiceID        string            `json:"service_id"`
	Service          string            `json:"service"`
	Environment      string            `json:"environment"`
	RuntimeKind      string            `json:"runtime_kind"`
	State            string            `json:"state"`
	Health           string            `json:"health"`
	RestartCount     int64             `json:"restart_count"`
	StartedAt        *string           `json:"started_at"`
	ObservedAt       string            `json:"observed_at"`
	ImageReference   string            `json:"image_reference"`
	ImageID          *string           `json:"image_id"`
	ImageDigest      *string           `json:"image_digest"`
	ArtifactID       string            `json:"artifact_id"`
	ArtifactIdentity string            `json:"artifact_identity"`
	CommitID         *string           `json:"commit_id"`
	CommitSHA        *string           `json:"commit_sha"`
	CorrelationID    string            `json:"correlation_id"`
	Confidence       correlation.Level `json:"confidence"`
	Score            float64           `json:"score"`
	EvidenceIDs      []string          `json:"evidence_ids"`
}

type runtimeResult struct {
	At      string          `json:"at"`
	Items   []runtimeOutput `json:"items"`
	Refresh *refreshOutput  `json:"refresh"`
}

type refreshOutput struct {
	OperationID      string `json:"operation_id"`
	ObservedAt       string `json:"observed_at"`
	Processed        int    `json:"processed"`
	Rejected         int    `json:"rejected"`
	Services         int    `json:"services"`
	RuntimeInstances int    `json:"runtime_instances"`
}

type runtimePagination struct {
	Limit      int     `json:"limit"`
	NextCursor *string `json:"next_cursor"`
}

func (a *App) runRuntime(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("runtime", flag.ContinueOnError)
	common := addInventoryFlags(flags)
	service := flags.String("service", "", "filter by service logical key")
	environment := flags.String("environment", "", "filter by environment")
	atValue := flags.String("at", "", "query timestamp in RFC3339")
	refresh := flags.Bool("refresh", false, "inspect Docker before querying")
	limit := flags.Int("limit", 100, "maximum number of results")
	cursor := flags.String("cursor", "", "opaque pagination cursor")
	help, parseErr := a.parseCommandFlags(flags, args)
	if help {
		return nil
	}
	if parseErr != nil {
		return fmt.Errorf("parse runtime flags: %w: %v", errs.ErrInvalid, parseErr)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("runtime accepts no positional arguments: %w", errs.ErrInvalid)
	}
	if *limit < 1 || *limit > 1000 {
		return fmt.Errorf("--limit must be between 1 and 1000: %w", errs.ErrInvalid)
	}
	var err error
	environmentSpecified := false
	flags.Visit(func(item *flag.Flag) {
		environmentSpecified = environmentSpecified || item.Name == "environment"
	})
	queryEnvironment := ""
	if environmentSpecified {
		queryEnvironment, err = identity.ValidEnvironment(*environment)
		if err != nil {
			return err
		}
	}
	var at time.Time
	if *atValue != "" {
		at, err = parseRFC3339(*atValue, "--at")
		if err != nil {
			return err
		}
	}
	cfg, store, err := a.openInventory(ctx, common)
	if err != nil {
		return err
	}
	defer store.Close()
	warnings := []string{}
	var refreshed *refreshOutput
	if *refresh {
		if a.RuntimeSource == nil || a.CommitSource == nil {
			return fmt.Errorf("Phase 1 source factories are unavailable: %w", errs.ErrUnavailable)
		}
		refreshOperationID := operationID()
		refreshEnvironment := "default"
		if environmentSpecified {
			refreshEnvironment = queryEnvironment
		}
		result, refreshErr := (inventory.Refresher{OperationID: refreshOperationID, Environment: refreshEnvironment, Runtime: a.RuntimeSource(), Commits: a.CommitSource(cfg.ProjectDir), Store: store}).Refresh(ctx)
		if refreshErr != nil {
			return fmt.Errorf("refresh runtime: %w", refreshErr)
		}
		warnings = append(warnings, result.Warnings...)
		refreshed = &refreshOutput{
			OperationID: result.OperationID,
			ObservedAt:  result.ObservedAt.UTC().Format(time.RFC3339Nano), Processed: result.Processed,
			Rejected: result.Rejected, Services: result.Services, RuntimeInstances: result.RuntimeInstances,
		}
		queryEnvironment = refreshEnvironment
	}
	if at.IsZero() {
		at = a.Now().UTC()
	}
	items, nextCursor, err := store.Runtime(ctx, inventory.RuntimeQuery{
		Service: *service, Environment: queryEnvironment, At: at, Limit: *limit, Cursor: *cursor,
	})
	if err != nil {
		return fmt.Errorf("query runtime: %w", err)
	}
	status, err := store.Status(ctx)
	if err != nil {
		return fmt.Errorf("read runtime freshness: %w", err)
	}
	warnings = append(warnings, freshnessWarnings(a.Now(), status)...)
	result := runtimeResult{At: at.Format(time.RFC3339Nano), Items: make([]runtimeOutput, 0, len(items)), Refresh: refreshed}
	for _, item := range items {
		output := runtimeOutput{
			ID: item.ID, ExternalID: item.ExternalID, ContainerName: item.ContainerName,
			ServiceID: item.ServiceID, Service: item.ServiceKey, Environment: item.Environment,
			RuntimeKind: item.RuntimeKind, State: item.State, Health: normalizeUnknown(item.Health),
			RestartCount: item.RestartCount, ObservedAt: item.ObservedAt.UTC().Format(time.RFC3339Nano),
			ImageReference: item.ImageReference, ImageID: pointer(item.ImageID), ImageDigest: pointer(item.ImageDigest),
			ArtifactID: item.ArtifactID, ArtifactIdentity: item.ArtifactIdentity,
			CommitID: pointer(item.CommitID), CommitSHA: pointer(item.CommitSHA), CorrelationID: item.CorrelationID,
			Confidence: item.Confidence, Score: item.Score, EvidenceIDs: nonNilStrings(item.EvidenceIDs),
		}
		if item.StartedAt != nil {
			value := item.StartedAt.UTC().Format(time.RFC3339Nano)
			output.StartedAt = &value
		}
		result.Items = append(result.Items, output)
	}
	pagination := runtimePagination{Limit: *limit, NextCursor: pointer(nextCursor)}
	if *common.jsonOutput {
		return WriteSuccess(a.Stdout, "runtime", a.Now(), result, warnings, pagination)
	}
	if refreshed != nil {
		fmt.Fprintf(a.Stdout, "Runtime refresh %s completed at %s: processed=%d rejected=%d services=%d\n", refreshed.OperationID, refreshed.ObservedAt, refreshed.Processed, refreshed.Rejected, refreshed.Services)
	}
	if len(result.Items) == 0 {
		fmt.Fprintln(a.Stdout, "No runtime instances found for the requested snapshot.")
	} else {
		for _, item := range result.Items {
			fmt.Fprintf(a.Stdout, "%s service=%s environment=%s state=%s health=%s image=%s artifact=%s commit=%s confidence=%s observed=%s\n",
				item.ID, item.Service, item.Environment, item.State, item.Health, item.ImageReference,
				item.ArtifactIdentity, displayOptional(item.CommitSHA), item.Confidence, item.ObservedAt)
		}
	}
	for _, warning := range warnings {
		fmt.Fprintf(a.Stderr, "warning: %s\n", warning)
	}
	if pagination.NextCursor != nil {
		fmt.Fprintf(a.Stdout, "next cursor: %s\n", *pagination.NextCursor)
	}
	return nil
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
