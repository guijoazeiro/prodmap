package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/telemetry"
)

const defaultEndpointLimit = 100

type endpointWindowOutput struct {
	ID            string   `json:"id"`
	IngestionID   string   `json:"ingestion_id"`
	WindowStart   string   `json:"window_start"`
	WindowEnd     string   `json:"window_end"`
	RequestCount  int64    `json:"request_count"`
	ErrorCount    int64    `json:"error_count"`
	DurationSumNS int64    `json:"duration_sum_ns"`
	P50NS         int64    `json:"p50_duration_ns"`
	P95NS         int64    `json:"p95_duration_ns"`
	P99NS         int64    `json:"p99_duration_ns"`
	IsComplete    bool     `json:"is_complete"`
	CoverageRatio *float64 `json:"coverage_ratio"`
	Algorithm     string   `json:"algorithm_version"`
}

type endpointOutput struct {
	ID            string                 `json:"id"`
	Protocol      string                 `json:"protocol"`
	Operation     string                 `json:"operation"`
	RouteTemplate *string                `json:"route_template"`
	FirstSeenAt   string                 `json:"first_seen_at"`
	LastSeenAt    string                 `json:"last_seen_at"`
	Windows       []endpointWindowOutput `json:"windows"`
}

type endpointServiceOutput struct {
	ID          string `json:"id"`
	LogicalKey  string `json:"logical_key"`
	DisplayName string `json:"display_name"`
}

type endpointsOutput struct {
	At              string                `json:"at"`
	Environment     string                `json:"environment"`
	Service         endpointServiceOutput `json:"service"`
	TelemetryStatus string                `json:"telemetry_status"`
	Items           []endpointOutput      `json:"items"`
}

type endpointsPagination struct {
	Limit      int     `json:"limit"`
	NextCursor *string `json:"next_cursor"`
}

func writeEndpointsUsage(writer io.Writer) {
	fmt.Fprintln(writer, "usage: prodmap endpoints --service <logical-key> [--environment <environment>] [--at <RFC3339>] [--limit <1..1000>] [--cursor <opaque-cursor>] [--json]")
	fmt.Fprintln(writer, "Lists endpoints with telemetry windows active at --at using [window_start, window_end). No active window does not prove absence of traffic or service activity.")
}

func (a *App) runEndpoints(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("endpoints", flag.ContinueOnError)
	flags.Usage = func() { writeEndpointsUsage(flags.Output()) }
	common := addInventoryFlags(flags)
	service := flags.String("service", "", "logical service key")
	environment := flags.String("environment", "default", "telemetry environment")
	atValue := flags.String("at", "", "query timestamp in RFC3339")
	limit := flags.Int("limit", defaultEndpointLimit, "maximum endpoints")
	cursor := flags.String("cursor", "", "opaque pagination cursor")
	help, err := a.parseCommandFlags(flags, args)
	if help {
		return nil
	}
	if err != nil {
		return fmt.Errorf("parse endpoints flags: %w: %v", errs.ErrInvalid, err)
	}
	if flags.NArg() != 0 || strings.TrimSpace(*service) == "" {
		return fmt.Errorf("--service is required and endpoints accepts no positional arguments: %w", errs.ErrInvalid)
	}
	if *limit < 1 || *limit > 1000 {
		return fmt.Errorf("--limit must be between 1 and 1000: %w", errs.ErrInvalid)
	}
	env, err := telemetry.ValidEnvironment(*environment)
	if err != nil {
		return err
	}
	at := a.Now().UTC()
	if *atValue != "" {
		at, err = parseRFC3339(*atValue, "--at")
		if err != nil {
			return err
		}
	}
	_, store, err := a.openInventory(ctx, common)
	if err != nil {
		return err
	}
	defer store.Close()
	result, err := store.EndpointContext(ctx, telemetry.EndpointQuery{ServiceKey: strings.TrimSpace(*service), Environment: env, At: at, Limit: *limit, Cursor: *cursor})
	if err != nil {
		return fmt.Errorf("query endpoints: %w", err)
	}
	output := endpointsOutput{At: result.At.UTC().Format(time.RFC3339Nano), Environment: result.Environment, Service: endpointServiceOutput{ID: result.Service.ID, LogicalKey: result.Service.LogicalKey, DisplayName: result.Service.DisplayName}, TelemetryStatus: result.TelemetryStatus, Items: make([]endpointOutput, 0, len(result.Items))}
	for _, item := range result.Items {
		entry := endpointOutput{ID: item.ID, Protocol: item.Protocol, Operation: item.Operation, RouteTemplate: item.RouteTemplate, FirstSeenAt: item.FirstSeenAt.UTC().Format(time.RFC3339Nano), LastSeenAt: item.LastSeenAt.UTC().Format(time.RFC3339Nano), Windows: make([]endpointWindowOutput, 0, len(item.Windows))}
		for _, window := range item.Windows {
			entry.Windows = append(entry.Windows, endpointWindowOutput{ID: window.ID, IngestionID: window.IngestionID, WindowStart: window.WindowStart.UTC().Format(time.RFC3339Nano), WindowEnd: window.WindowEnd.UTC().Format(time.RFC3339Nano), RequestCount: window.RequestCount, ErrorCount: window.ErrorCount, DurationSumNS: window.DurationSumNS, P50NS: window.P50NS, P95NS: window.P95NS, P99NS: window.P99NS, IsComplete: window.IsComplete, CoverageRatio: window.CoverageRatio, Algorithm: window.Algorithm})
		}
		output.Items = append(output.Items, entry)
	}
	pagination := endpointsPagination{Limit: *limit, NextCursor: pointer(result.NextCursor)}
	if *common.jsonOutput {
		return WriteSuccess(a.Stdout, "endpoints", a.Now(), output, result.Warnings, pagination)
	}
	fmt.Fprintf(a.Stdout, "Service: %s\nEnvironment: %s\nAt: %s\nTelemetry: %s\n", output.Service.LogicalKey, output.Environment, output.At, output.TelemetryStatus)
	if len(output.Items) == 0 {
		fmt.Fprintln(a.Stdout, "No endpoint telemetry window is active at this instant.")
	}
	for _, item := range output.Items {
		fmt.Fprintf(a.Stdout, "%s\t%s\twindows=%d\n", item.Protocol, item.Operation, len(item.Windows))
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(a.Stderr, "warning: %s\n", warning)
	}
	if pagination.NextCursor != nil {
		fmt.Fprintf(a.Stdout, "next cursor: %s\n", *pagination.NextCursor)
	}
	return nil
}
