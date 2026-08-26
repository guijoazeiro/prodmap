package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/guijoazeiro/prodmap/internal/deployment"
	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/identity"
)

const defaultDeploymentLimit = 100

func writeDeploymentsUsage(writer io.Writer) {
	fmt.Fprintln(writer, "usage: prodmap deployments ingest --file <path> [--format deployment-ledger-jsonl/v1] [--project-dir <path>] [--data-dir <path>] [--json]")
}
func writeDeploysUsage(writer io.Writer) {
	fmt.Fprintln(writer, "usage: prodmap deploys [--service <logical-key>] [--environment <environment>] [--since <RFC3339>] [--until <RFC3339>] [--status <status>] [--limit <1..1000>] [--cursor <opaque>] [--json]")
}

func (a *App) runDeployments(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("deployments requires subcommand ingest: %w", errs.ErrInvalid)
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		writeDeploymentsUsage(a.Stdout)
		return nil
	}
	if args[0] != "ingest" {
		return fmt.Errorf("unknown deployments subcommand %q: %w", args[0], errs.ErrInvalid)
	}
	flags := flag.NewFlagSet("deployments ingest", flag.ContinueOnError)
	flags.Usage = func() { writeDeploymentsUsage(flags.Output()) }
	common := addInventoryFlags(flags)
	file := flags.String("file", "", "frozen deployment ledger JSONL file")
	format := flags.String("format", deployment.Format, "input format")
	help, err := a.parseCommandFlags(flags, args[1:])
	if help {
		return nil
	}
	if err != nil {
		return fmt.Errorf("parse deployments ingest flags: %w: %v", errs.ErrInvalid, err)
	}
	if flags.NArg() != 0 || strings.TrimSpace(*file) == "" {
		return fmt.Errorf("--file is required and deployments ingest accepts no positional arguments: %w", errs.ErrInvalid)
	}
	if *format != deployment.Format {
		return fmt.Errorf("--format must be %q: %w", deployment.Format, errs.ErrInvalid)
	}
	snapshot, err := deployment.LoadFrozenFile(ctx, *file, a.Now())
	if err != nil {
		return fmt.Errorf("load deployment ledger: %w", err)
	}
	_, store, err := a.openInventory(ctx, common)
	if err != nil {
		return err
	}
	defer store.Close()
	result, err := store.SaveDeployment(ctx, snapshot)
	if err != nil {
		return fmt.Errorf("persist deployment ledger: %w", err)
	}
	data := deploymentIngestOutput{IngestionID: result.IngestionID, SourceHash: result.SourceHash, Format: result.Format, RecordsSeen: result.RecordsSeen, DeploymentsInserted: result.DeploymentsInserted, DeploymentsExisting: result.DeploymentsExisting, IdempotentReplay: result.IdempotentReplay}
	if *common.jsonOutput {
		return WriteSuccess(a.Stdout, "deployments ingest", a.Now(), data, result.Warnings, nil)
	}
	fmt.Fprintf(a.Stdout, "Deployment ingestion %s: records=%d inserted=%d existing=%d replay=%t\n", result.IngestionID, result.RecordsSeen, result.DeploymentsInserted, result.DeploymentsExisting, result.IdempotentReplay)
	for _, warning := range result.Warnings {
		fmt.Fprintf(a.Stderr, "warning: %s\n", warning)
	}
	return nil
}

type deploymentIngestOutput struct {
	IngestionID         string `json:"ingestion_id"`
	SourceHash          string `json:"source_hash"`
	Format              string `json:"format"`
	RecordsSeen         int    `json:"records_seen"`
	DeploymentsInserted int    `json:"deployments_inserted"`
	DeploymentsExisting int    `json:"deployments_existing"`
	IdempotentReplay    bool   `json:"idempotent_replay"`
}
type deploysOutput struct {
	Since       string             `json:"since"`
	Until       string             `json:"until"`
	Environment string             `json:"environment"`
	Items       []deploymentOutput `json:"items"`
}
type deploysPagination struct {
	Limit      int     `json:"limit"`
	NextCursor *string `json:"next_cursor"`
}
type deploymentOutput struct {
	ID               string           `json:"id"`
	ExternalID       string           `json:"external_id"`
	Environment      string           `json:"environment"`
	Service          string           `json:"service"`
	Status           string           `json:"status"`
	Strategy         string           `json:"strategy"`
	StartedAt        string           `json:"started_at"`
	FinishedAt       *string          `json:"finished_at"`
	CausalityClaimed bool             `json:"causality_claimed"`
	Provenance       provenanceOutput `json:"provenance"`
}
type provenanceOutput struct {
	Status      string                `json:"status"`
	Confidence  confidenceOutput      `json:"confidence"`
	Artifact    artifactOutput        `json:"artifact"`
	Commit      commitOutput          `json:"commit"`
	Evidence    []deployment.Evidence `json:"evidence"`
	Limitations []string              `json:"limitations"`
}
type confidenceOutput struct {
	Level string `json:"level"`
	Basis string `json:"basis"`
}
type artifactOutput struct {
	ID         *string `json:"id"`
	RepoDigest *string `json:"repo_digest"`
	ImageID    string  `json:"image_id"`
}
type commitOutput struct {
	ID       *string `json:"id"`
	SHA      *string `json:"sha"`
	Verified bool    `json:"verified"`
}

func (a *App) runDeploys(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("deploys", flag.ContinueOnError)
	flags.Usage = func() { writeDeploysUsage(flags.Output()) }
	common := addInventoryFlags(flags)
	service := flags.String("service", "", "logical service key")
	environment := flags.String("environment", "default", "deployment environment")
	sinceValue := flags.String("since", "", "inclusive RFC3339 timestamp")
	untilValue := flags.String("until", "", "exclusive RFC3339 timestamp")
	status := flags.String("status", "", "deployment status")
	limit := flags.Int("limit", defaultDeploymentLimit, "maximum deployments")
	cursor := flags.String("cursor", "", "opaque pagination cursor")
	help, err := a.parseCommandFlags(flags, args)
	if help {
		return nil
	}
	if err != nil {
		return fmt.Errorf("parse deploys flags: %w: %v", errs.ErrInvalid, err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("deploys accepts no positional arguments: %w", errs.ErrInvalid)
	}
	if *limit < 1 || *limit > 1000 {
		return fmt.Errorf("--limit must be between 1 and 1000: %w", errs.ErrInvalid)
	}
	env, err := identity.ValidEnvironment(*environment)
	if err != nil {
		return err
	}
	until := a.Now().UTC()
	if *untilValue != "" {
		until, err = parseRFC3339(*untilValue, "--until")
		if err != nil {
			return err
		}
	}
	since := until.Add(-24 * time.Hour)
	if *sinceValue != "" {
		since, err = parseRFC3339(*sinceValue, "--since")
		if err != nil {
			return err
		}
	}
	if !until.After(since) {
		return fmt.Errorf("--until must be after --since: %w", errs.ErrInvalid)
	}
	_, store, err := a.openInventory(ctx, common)
	if err != nil {
		return err
	}
	defer store.Close()
	result, err := store.Deployments(ctx, deployment.Query{Environment: env, Service: strings.TrimSpace(*service), Status: *status, Since: since, Until: until, Limit: *limit, Cursor: *cursor})
	if err != nil {
		return fmt.Errorf("query deployments: %w", err)
	}
	output := deploysOutput{Since: result.Since.Format(time.RFC3339Nano), Until: result.Until.Format(time.RFC3339Nano), Environment: result.Environment, Items: make([]deploymentOutput, 0, len(result.Items))}
	for _, item := range result.Items {
		entry := deploymentOutput{ID: item.ID, ExternalID: item.ExternalID, Environment: item.Environment, Service: item.Service, Status: item.Status, Strategy: item.Strategy, StartedAt: item.StartedAt.Format(time.RFC3339Nano), CausalityClaimed: false, Provenance: provenanceOutput{Status: item.Provenance.Status, Confidence: confidenceOutput{Level: item.Provenance.Confidence, Basis: item.Provenance.Basis}, Artifact: artifactOutput{ID: item.Provenance.ArtifactID, RepoDigest: item.Provenance.RepoDigest, ImageID: item.Provenance.ImageID}, Commit: commitOutput{ID: item.Provenance.CommitID, SHA: item.Provenance.CommitSHA, Verified: item.Provenance.Verified}, Evidence: item.Provenance.Evidence, Limitations: item.Provenance.Limitations}}
		if item.FinishedAt != nil {
			value := item.FinishedAt.Format(time.RFC3339Nano)
			entry.FinishedAt = &value
		}
		output.Items = append(output.Items, entry)
	}
	pagination := deploysPagination{Limit: *limit, NextCursor: pointer(result.NextCursor)}
	if *common.jsonOutput {
		return WriteSuccess(a.Stdout, "deploys", a.Now(), output, []string{}, pagination)
	}
	fmt.Fprintf(a.Stdout, "Deployments: %s [%s,%s)\n", output.Environment, output.Since, output.Until)
	for _, item := range output.Items {
		fmt.Fprintf(a.Stdout, "%s  %s  %s  %s  %s\n", item.StartedAt, item.Service, item.Status, item.Provenance.Status, item.Provenance.Confidence.Level)
	}
	return nil
}
