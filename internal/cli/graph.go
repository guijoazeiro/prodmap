package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/telemetry"
	"github.com/guijoazeiro/prodmap/internal/topology"
)

const defaultGraphMaxNodes = 100

type graphNodeOutput struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	LogicalKey  string `json:"logical_key"`
	DisplayName string `json:"display_name"`
}

type graphConfidenceOutput struct {
	Level            topology.Confidence `json:"level"`
	Basis            string              `json:"basis"`
	AlgorithmVersion string              `json:"algorithm_version"`
}

type graphEdgeOutput struct {
	ID             string                `json:"id"`
	From           string                `json:"from"`
	To             string                `json:"to"`
	RelationType   string                `json:"relation_type"`
	DependencyKind string                `json:"dependency_kind"`
	WindowStart    string                `json:"window_start"`
	WindowEnd      string                `json:"window_end"`
	RequestCount   int64                 `json:"request_count"`
	ErrorCount     int64                 `json:"error_count"`
	DurationSumNS  int64                 `json:"duration_sum_ns"`
	Confidence     graphConfidenceOutput `json:"confidence"`
	EvidenceIDs    []string              `json:"evidence_ids"`
	Limitations    []string              `json:"limitations"`
}

type graphOutput struct {
	At          string            `json:"at"`
	Environment string            `json:"environment"`
	Roots       []string          `json:"roots"`
	Nodes       []graphNodeOutput `json:"nodes"`
	Edges       []graphEdgeOutput `json:"edges"`
	Truncated   bool              `json:"truncated"`
}

func (a *App) runGraph(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("graph", flag.ContinueOnError)
	common := addInventoryFlags(flags)
	service := flags.String("service", "", "service selector")
	all := flags.Bool("all", false, "use all services as graph roots")
	environment := flags.String("environment", "default", "deployment environment")
	atValue := flags.String("at", "", "query timestamp in RFC3339")
	depth := flags.Int("depth", 1, "breadth-first traversal depth (1..5)")
	minimumValue := flags.String("min-confidence", "low", "low, medium, high, or exact")
	maxNodes := flags.Int("max-nodes", defaultGraphMaxNodes, "maximum graph nodes")
	help, err := a.parseCommandFlags(flags, args)
	if help {
		return nil
	}
	if err != nil {
		return fmt.Errorf("parse graph flags: %w: %v", errs.ErrInvalid, err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("graph accepts no positional arguments: %w", errs.ErrInvalid)
	}
	if (*all && strings.TrimSpace(*service) != "") || (!*all && strings.TrimSpace(*service) == "") {
		return fmt.Errorf("exactly one of --service or --all is required: %w", errs.ErrInvalid)
	}
	if *depth < 1 || *depth > 5 {
		return fmt.Errorf("--depth must be between 1 and 5: %w", errs.ErrInvalid)
	}
	if *maxNodes < 1 || *maxNodes > 1000 {
		return fmt.Errorf("--max-nodes must be between 1 and 1000: %w", errs.ErrInvalid)
	}
	env, err := telemetry.ValidEnvironment(*environment)
	if err != nil {
		return err
	}
	minimum, err := topology.ParseConfidence(*minimumValue)
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
	result, err := topology.Build(ctx, store, topology.Query{Service: strings.TrimSpace(*service), All: *all, Environment: env, At: at, Depth: *depth, MinConfidence: minimum, MaxNodes: *maxNodes})
	if err != nil {
		return fmt.Errorf("query graph: %w", err)
	}
	output := graphOutput{At: result.At.Format(time.RFC3339Nano), Environment: result.Environment, Roots: nonNilStrings(result.Roots), Nodes: make([]graphNodeOutput, 0, len(result.Nodes)), Edges: make([]graphEdgeOutput, 0, len(result.Edges)), Truncated: result.Truncated}
	for _, node := range result.Nodes {
		output.Nodes = append(output.Nodes, graphNodeOutput{ID: node.ID, Type: node.Type, LogicalKey: node.LogicalKey, DisplayName: node.DisplayName})
	}
	for _, edge := range result.Edges {
		output.Edges = append(output.Edges, graphEdgeOutput{
			ID: edge.ID, From: edge.From, To: edge.To, RelationType: "OBSERVED", DependencyKind: edge.DependencyKind,
			WindowStart: edge.WindowStart.Format(time.RFC3339Nano), WindowEnd: edge.WindowEnd.Format(time.RFC3339Nano),
			RequestCount: edge.RequestCount, ErrorCount: edge.ErrorCount, DurationSumNS: edge.DurationSumNS,
			Confidence:  graphConfidenceOutput{Level: edge.Confidence, Basis: edge.Basis, AlgorithmVersion: edge.AlgorithmVersion},
			EvidenceIDs: nonNilStrings(edge.EvidenceIDs), Limitations: nonNilStrings(edge.Limitations),
		})
	}
	if *common.jsonOutput {
		return WriteSuccess(a.Stdout, "graph", a.Now(), output, result.Warnings, nil)
	}
	fmt.Fprintf(a.Stdout, "Observed graph at %s environment=%s roots=%d nodes=%d edges=%d truncated=%t\n", output.At, output.Environment, len(output.Roots), len(output.Nodes), len(output.Edges), output.Truncated)
	for _, edge := range output.Edges {
		fmt.Fprintf(a.Stdout, "%s -> %s kind=%s confidence=%s basis=%q limitations=%q requests=%d window=[%s,%s)\n",
			edge.From, edge.To, edge.DependencyKind, edge.Confidence.Level, edge.Confidence.Basis, strings.Join(edge.Limitations, "; "), edge.RequestCount, edge.WindowStart, edge.WindowEnd)
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(a.Stderr, "warning: %s\n", warning)
	}
	return nil
}
