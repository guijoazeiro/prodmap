package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/guijoazeiro/prodmap/internal/errs"
	prodmapotel "github.com/guijoazeiro/prodmap/internal/otel"
	"github.com/guijoazeiro/prodmap/internal/telemetry"
)

type telemetryIngestOutput struct {
	IngestionID      string `json:"ingestion_id"`
	SourceHash       string `json:"source_hash"`
	Environment      string `json:"environment"`
	WindowStart      string `json:"window_start"`
	WindowEnd        string `json:"window_end"`
	Lines            int    `json:"lines"`
	ResourceSpans    int    `json:"resource_spans"`
	SpansSeen        int    `json:"spans_seen"`
	SpansAccepted    int    `json:"spans_accepted"`
	SpansIgnored     int    `json:"spans_ignored"`
	Services         int    `json:"services"`
	Endpoints        int    `json:"endpoints"`
	Dependencies     int    `json:"dependencies"`
	Observations     int    `json:"observations"`
	TelemetryWindows int    `json:"telemetry_windows"`
	IdempotentReplay bool   `json:"idempotent_replay"`
}

func (a *App) runTelemetry(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("telemetry requires subcommand ingest: %w", errs.ErrInvalid)
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(a.Stdout, "usage: prodmap telemetry ingest [flags]")
		return nil
	}
	if args[0] != "ingest" {
		return fmt.Errorf("unknown telemetry subcommand %q: %w", args[0], errs.ErrInvalid)
	}
	return a.runTelemetryIngest(ctx, args[1:])
}

func (a *App) runTelemetryIngest(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("telemetry ingest", flag.ContinueOnError)
	common := addInventoryFlags(flags)
	file := flags.String("file", "", "immutable OTLP JSONL file")
	format := flags.String("format", telemetry.FormatOTLPJSONL, "input format (otlp-jsonl)")
	environment := flags.String("environment", "default", "deployment environment")
	windowStart := flags.String("window-start", "", "inclusive RFC3339 window start")
	windowEnd := flags.String("window-end", "", "exclusive RFC3339 window end")
	help, err := a.parseCommandFlags(flags, args)
	if help {
		return nil
	}
	if err != nil {
		return fmt.Errorf("parse telemetry ingest flags: %w: %v", errs.ErrInvalid, err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("telemetry ingest accepts no positional arguments: %w", errs.ErrInvalid)
	}
	if strings.TrimSpace(*file) == "" || *windowStart == "" || *windowEnd == "" {
		return fmt.Errorf("--file, --window-start, and --window-end are required: %w", errs.ErrInvalid)
	}
	if *format != telemetry.FormatOTLPJSONL {
		return fmt.Errorf("--format must be %q: %w", telemetry.FormatOTLPJSONL, errs.ErrInvalid)
	}
	env, err := telemetry.ValidEnvironment(*environment)
	if err != nil {
		return err
	}
	start, err := parseRFC3339(*windowStart, "--window-start")
	if err != nil {
		return err
	}
	end, err := parseRFC3339(*windowEnd, "--window-end")
	if err != nil {
		return err
	}
	if !end.After(start) {
		return fmt.Errorf("--window-end must be after --window-start: %w", errs.ErrInvalid)
	}

	// Parse and sanitize the complete frozen file before opening SQLite so an
	// invalid line can never produce a partial ingestion.
	snapshot, err := telemetry.LoadFrozenFile(ctx, *file, env, start, end, a.Now(), prodmapotel.NewDecoder())
	if err != nil {
		return fmt.Errorf("decode telemetry snapshot: %w", err)
	}
	_, store, err := a.openInventory(ctx, common)
	if err != nil {
		return err
	}
	defer store.Close()
	result, err := store.SaveTelemetry(ctx, snapshot)
	if err != nil {
		return fmt.Errorf("persist telemetry snapshot: %w", err)
	}
	output := telemetryIngestOutput{
		IngestionID: result.IngestionID, SourceHash: result.SourceHash, Environment: result.Environment,
		WindowStart: result.WindowStart.UTC().Format(time.RFC3339Nano), WindowEnd: result.WindowEnd.UTC().Format(time.RFC3339Nano),
		Lines: result.Stats.Lines, ResourceSpans: result.Stats.ResourceSpans, SpansSeen: result.Stats.SpansSeen,
		SpansAccepted: result.Stats.SpansAccepted, SpansIgnored: result.Stats.SpansIgnored,
		Services: result.Stats.Services, Endpoints: result.Stats.Endpoints, Dependencies: result.Stats.Dependencies,
		Observations: result.Stats.Observations, TelemetryWindows: result.Stats.TelemetryWindows,
		IdempotentReplay: result.IdempotentReplay,
	}
	if *common.jsonOutput {
		return WriteSuccess(a.Stdout, "telemetry ingest", a.Now(), output, result.Warnings, nil)
	}
	fmt.Fprintf(a.Stdout, "Telemetry ingestion %s: hash=%s environment=%s window=[%s,%s) spans=%d/%d services=%d endpoints=%d dependencies=%d replay=%t\n",
		output.IngestionID, output.SourceHash, output.Environment, output.WindowStart, output.WindowEnd,
		output.SpansAccepted, output.SpansSeen, output.Services, output.Endpoints, output.Dependencies, output.IdempotentReplay)
	for _, warning := range result.Warnings {
		fmt.Fprintf(a.Stderr, "warning: %s\n", warning)
	}
	return nil
}
