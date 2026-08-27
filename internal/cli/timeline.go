package cli

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/identity"
	"github.com/guijoazeiro/prodmap/internal/timeline"
)

func writeTimelineUsage(writer interface{ Write([]byte) (int, error) }) {
	fmt.Fprintln(writer, "usage: prodmap timeline [--service <logical-key>] [--environment <environment>] [--since <RFC3339>] [--until <RFC3339>] [--limit <1..1000>] [--cursor <opaque>] [--project-dir <path>] [--data-dir <path>] [--json]")
}

func (a *App) runTimeline(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("timeline", flag.ContinueOnError)
	flags.Usage = func() { writeTimelineUsage(flags.Output()) }
	common := addInventoryFlags(flags)
	service := flags.String("service", "", "logical service key")
	environment := flags.String("environment", "default", "timeline environment")
	sinceValue := flags.String("since", "", "inclusive RFC3339 timestamp")
	untilValue := flags.String("until", "", "exclusive RFC3339 timestamp")
	limit := flags.Int("limit", timeline.DefaultLimit, "maximum events")
	cursor := flags.String("cursor", "", "opaque pagination cursor")
	help, err := a.parseCommandFlags(flags, args)
	if help {
		return nil
	}
	if err != nil {
		return fmt.Errorf("parse timeline flags: %w: %v", errs.ErrInvalid, err)
	}
	if flags.NArg() != 0 || *limit < 1 || *limit > 1000 {
		return fmt.Errorf("invalid timeline arguments: %w", errs.ErrInvalid)
	}
	env, err := identity.ValidEnvironment(*environment)
	if err != nil {
		return err
	}
	var until time.Time
	if *untilValue != "" {
		until, err = parseRFC3339(*untilValue, "--until")
		if err != nil {
			return err
		}
	} else {
		until = a.Now().UTC()
	}
	since := until.Add(-2 * time.Hour)
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
	result, err := store.Timeline(ctx, timeline.Query{Environment: env, Service: *service, Since: since, Until: until, Limit: *limit, Cursor: *cursor})
	if err != nil {
		return fmt.Errorf("query timeline: %w", err)
	}
	generatedAt := a.Now().UTC()
	items := make([]timelineOutput, 0, len(result.Items))
	for _, event := range result.Items {
		items = append(items, timelineOutputFrom(event, generatedAt))
	}
	data := timelineData{Since: result.Since.Format(time.RFC3339Nano), Until: result.Until.Format(time.RFC3339Nano), Environment: result.Environment, Items: items}
	pagination := deploysPagination{Limit: *limit, NextCursor: pointer(result.NextCursor)}
	if *common.jsonOutput {
		return WriteSuccess(a.Stdout, "timeline", generatedAt, data, []string{}, pagination)
	}
	fmt.Fprintf(a.Stdout, "Timeline: %s [%s,%s)\n", data.Environment, data.Since, data.Until)
	for _, item := range data.Items {
		fmt.Fprintf(a.Stdout, "%s  %s  %s  %s\n", item.Time, item.Kind, item.Service, item.Confidence.Level)
	}
	return nil
}

type timelineData struct {
	Since       string           `json:"since"`
	Until       string           `json:"until"`
	Environment string           `json:"environment"`
	Items       []timelineOutput `json:"items"`
}
type timelineOutput struct {
	ID               string               `json:"id"`
	Time             string               `json:"time"`
	Kind             string               `json:"kind"`
	Environment      string               `json:"environment"`
	Service          string               `json:"service"`
	RelationType     string               `json:"relation_type"`
	Subject          timeline.Subject     `json:"subject"`
	Source           timelineSourceOutput `json:"source"`
	Confidence       confidenceOutput     `json:"confidence"`
	Deployment       *timeline.Deployment `json:"deployment"`
	Runtime          *timeline.Runtime    `json:"runtime"`
	Concurrency      timeline.Concurrency `json:"concurrency"`
	Limitations      []string             `json:"limitations"`
	CausalityClaimed bool                 `json:"causality_claimed"`
}
type timelineSourceOutput struct {
	Kind             string `json:"kind"`
	ObservedAt       string `json:"observed_at"`
	FreshnessSeconds *int64 `json:"freshness_seconds"`
}

func timelineOutputFrom(event timeline.Event, generatedAt time.Time) timelineOutput {
	generatedAt = generatedAt.UTC()
	limitations := make([]string, 0, len(event.Limitations)+1)
	for _, limitation := range event.Limitations {
		limitations = appendUniqueTimelineLimitation(limitations, limitation)
	}
	confidence := confidenceOutput{Level: event.Confidence.Level, Basis: event.Confidence.Basis}
	eventFuture := event.Time.After(generatedAt)
	sourceFuture := event.Source.ObservedAt.After(generatedAt)
	if eventFuture || sourceFuture {
		basis, limitation := futureTimelineBasis(eventFuture, sourceFuture)
		confidence = confidenceOutput{Level: "UNKNOWN", Basis: basis}
		limitations = appendUniqueTimelineLimitation(limitations, limitation)
	}
	var freshness *int64
	if !sourceFuture {
		age := generatedAt.Sub(event.Source.ObservedAt.UTC())
		value := int64(age.Seconds())
		freshness = new(value)
	}
	return timelineOutput{ID: event.ID, Time: event.Time.Format(time.RFC3339Nano), Kind: event.Kind, Environment: event.Environment, Service: event.Service, RelationType: event.RelationType, Subject: event.Subject, Source: timelineSourceOutput{Kind: event.Source.Kind, ObservedAt: event.Source.ObservedAt.Format(time.RFC3339Nano), FreshnessSeconds: freshness}, Confidence: confidence, Deployment: event.Deployment, Runtime: event.Runtime, Concurrency: event.Concurrency, Limitations: limitations, CausalityClaimed: false}
}

func futureTimelineBasis(eventFuture, sourceFuture bool) (string, string) {
	switch {
	case eventFuture && sourceFuture:
		return "event and source timestamps are in the future relative to generated_at", "event and source timestamps are in the future relative to generated_at"
	case eventFuture:
		return "event timestamp is in the future relative to generated_at", "event timestamp is in the future relative to generated_at"
	default:
		return "source timestamp is in the future relative to generated_at", "source timestamp is in the future relative to generated_at"
	}
}

func appendUniqueTimelineLimitation(limitations []string, limitation string) []string {
	for _, existing := range limitations {
		if existing == limitation {
			return limitations
		}
	}
	return append(limitations, limitation)
}
