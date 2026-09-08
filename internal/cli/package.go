package cli

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/guijoazeiro/prodmap/internal/baseline"
	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/investigation"
	investigationpackage "github.com/guijoazeiro/prodmap/internal/investigationpackage"
	"github.com/guijoazeiro/prodmap/internal/regression"
)

func writePackageUsage(writer interface{ Write([]byte) (int, error) }) {
	fmt.Fprintln(writer, "usage: prodmap package <create|verify> [flags]")
	fmt.Fprintln(writer, "       prodmap package create --deployment <UUID> --metric <request_count|error_rate|latency_p50|latency_p95|latency_p99> --output <file.zip> [--before <5m..24h>] [--after <5m..24h>] [--min-samples <1..1000000>] [--min-coverage <0..1>] [--project-dir <path>] [--data-dir <path>] [--json]")
	fmt.Fprintln(writer, "       prodmap package verify --file <file.zip> [--json]")
}

func (a *App) runPackage(ctx context.Context, args []string) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		writePackageUsage(a.Stdout)
		return nil
	}
	if len(args) == 0 {
		return fmt.Errorf("package subcommand is required: %w", errs.ErrInvalid)
	}
	switch args[0] {
	case "create":
		return a.runPackageCreate(ctx, args[1:])
	case "verify":
		return a.runPackageVerify(ctx, args[1:])
	default:
		return fmt.Errorf("unknown package subcommand: %w", errs.ErrInvalid)
	}
}

func (a *App) runPackageCreate(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("package create", flag.ContinueOnError)
	flags.Usage = func() { writePackageUsage(flags.Output()) }
	common := addInventoryFlags(flags)
	deploymentID := flags.String("deployment", "", "internal deployment UUID")
	metric := flags.String("metric", "", "comparison metric")
	before := flags.Duration("before", baseline.DefaultWindow, "exact baseline duration")
	after := flags.Duration("after", baseline.DefaultWindow, "exact observation duration")
	minSamples := flags.Int64("min-samples", baseline.DefaultMinSamples, "minimum samples")
	minCoverage := flags.Float64("min-coverage", baseline.DefaultMinCoverage, "minimum known coverage")
	output := flags.String("output", "", "new package ZIP path")
	help, err := a.parseCommandFlags(flags, args)
	if help {
		return nil
	}
	if err != nil {
		return fmt.Errorf("parse package create flags: %w: %v", errs.ErrInvalid, err)
	}
	query := regression.Query{DeploymentID: strings.TrimSpace(*deploymentID), Metric: baseline.Metric(*metric), Before: *before, After: *after, MinSamples: *minSamples, MinCoverage: *minCoverage}
	if flags.NArg() != 0 || strings.TrimSpace(*output) == "" || regression.ValidateQuery(query) != nil {
		return fmt.Errorf("invalid package create flags: %w", errs.ErrInvalid)
	}
	_, store, err := a.openInventoryReadOnly(ctx, common)
	if err != nil {
		return err
	}
	defer store.Close()
	createdAt := a.Now().UTC()
	result, err := investigation.Compose(ctx, store, investigation.Query{Comparison: query, GeneratedAt: createdAt})
	if err != nil {
		return fmt.Errorf("compose package investigation: %w", err)
	}
	created, err := investigationpackage.Create(ctx, investigationpackage.CreateRequest{OutputPath: *output, CreatedAt: createdAt, Investigation: result})
	if err != nil {
		return fmt.Errorf("create investigation package: %w", err)
	}
	data := packageCreateOutput{FileName: created.FileName, PackageSHA256: created.PackageSHA256, InvestigationKey: created.InvestigationKey, PackageSize: created.PackageSize}
	if *common.jsonOutput {
		return WriteSuccess(a.Stdout, "package create", createdAt, data, []string{}, nil)
	}
	fmt.Fprintf(a.Stdout, "Investigation package created: %s\nSHA-256: %s\nInvestigation key: %s\nSize: %d bytes\n", data.FileName, data.PackageSHA256, data.InvestigationKey, data.PackageSize)
	return nil
}

func (a *App) runPackageVerify(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("package verify", flag.ContinueOnError)
	flags.Usage = func() { writePackageUsage(flags.Output()) }
	file := flags.String("file", "", "investigation package ZIP path")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	help, err := a.parseCommandFlags(flags, args)
	if help {
		return nil
	}
	if err != nil {
		return fmt.Errorf("parse package verify flags: %w: %v", errs.ErrInvalid, err)
	}
	if flags.NArg() != 0 || strings.TrimSpace(*file) == "" {
		return fmt.Errorf("invalid package verify flags: %w", errs.ErrInvalid)
	}
	verified, err := investigationpackage.Verify(ctx, *file)
	if err != nil {
		return fmt.Errorf("verify investigation package: %w", err)
	}
	generatedAt := a.Now().UTC()
	data := packageVerifyOutput{FileName: filepath.Base(*file), PackageSHA256: verified.PackageSHA256, FormatVersion: verified.FormatVersion, CreatedAt: verified.CreatedAt, InvestigationVersion: verified.InvestigationVersion, InvestigationKey: verified.InvestigationKey, RedactionProfile: verified.RedactionProfile, CausalityClaimed: false}
	if *jsonOutput {
		return WriteSuccess(a.Stdout, "package verify", generatedAt, data, []string{}, nil)
	}
	fmt.Fprintf(a.Stdout, "Investigation package verified: %s\nInvestigation key: %s\n", data.FileName, data.InvestigationKey)
	return nil
}

type packageCreateOutput struct {
	FileName         string `json:"file_name"`
	PackageSHA256    string `json:"package_sha256"`
	InvestigationKey string `json:"investigation_key"`
	PackageSize      int64  `json:"package_size"`
}

type packageVerifyOutput struct {
	FileName             string `json:"file_name"`
	PackageSHA256        string `json:"package_sha256"`
	FormatVersion        string `json:"format_version"`
	CreatedAt            string `json:"created_at"`
	InvestigationVersion string `json:"investigation_version"`
	InvestigationKey     string `json:"investigation_key"`
	RedactionProfile     string `json:"redaction_profile"`
	CausalityClaimed     bool   `json:"causality_claimed"`
}
