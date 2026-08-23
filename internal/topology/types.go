// Package topology owns observed dependency graph contracts and traversal.
package topology

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/inventory"
)

const AlgorithmVersion = "otel-topology/v1"

type Confidence string

const (
	Unknown Confidence = "UNKNOWN"
	Low     Confidence = "LOW"
	Medium  Confidence = "MEDIUM"
	High    Confidence = "HIGH"
	Exact   Confidence = "EXACT"
)

func ParseConfidence(value string) (Confidence, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "low":
		return Low, nil
	case "medium":
		return Medium, nil
	case "high":
		return High, nil
	case "exact":
		return Exact, nil
	default:
		return Unknown, fmt.Errorf("%w: min-confidence must be low, medium, high, or exact", errs.ErrInvalid)
	}
}

func Rank(value Confidence) int {
	switch value {
	case Exact:
		return 4
	case High:
		return 3
	case Medium:
		return 2
	case Low:
		return 1
	default:
		return 0
	}
}

type Node struct {
	ID          string
	Type        string
	LogicalKey  string
	DisplayName string
}

type Edge struct {
	ID               string
	From             string
	To               string
	DependencyKind   string
	WindowStart      time.Time
	WindowEnd        time.Time
	RequestCount     int64
	ErrorCount       int64
	DurationSumNS    int64
	Confidence       Confidence
	Basis            string
	AlgorithmVersion string
	EvidenceIDs      []string
	Limitations      []string
}

type Query struct {
	Service       string
	All           bool
	Environment   string
	At            time.Time
	Depth         int
	MinConfidence Confidence
	MaxNodes      int
}

type Result struct {
	At          time.Time
	Environment string
	Roots       []string
	Nodes       []Node
	Edges       []Edge
	Truncated   bool
	Warnings    []string
}

type Reader interface {
	ResolveGraphRoots(context.Context, string, string, bool) ([]Node, error)
	ObservedGraph(context.Context, string, time.Time, Confidence, []string, int) ([]Node, []Edge, bool, error)
}

// Build performs deterministic breadth-first traversal over one temporal graph.
func Build(ctx context.Context, reader Reader, query Query) (Result, error) {
	roots, err := reader.ResolveGraphRoots(ctx, query.Environment, query.Service, query.All)
	if err != nil {
		return Result{}, err
	}
	if len(roots) == 0 {
		return Result{}, fmt.Errorf("%w: service selector matched no service", errs.ErrNotFound)
	}
	nodeByID := make(map[string]Node, len(roots))
	for _, root := range roots {
		nodeByID[root.ID] = root
	}
	sort.Slice(roots, func(i, j int) bool { return nodeSemanticLess(roots[i], roots[j]) })
	result := Result{At: query.At.UTC(), Environment: query.Environment}
	seen := make(map[string]int)
	frontier := make([]string, 0, len(roots))
	for _, root := range roots {
		if len(seen) >= query.MaxNodes {
			result.Truncated = true
			break
		}
		if _, exists := seen[root.ID]; exists {
			continue
		}
		seen[root.ID] = 0
		frontier = append(frontier, root.ID)
		result.Roots = append(result.Roots, root.ID)
	}
	selectedEdges := make(map[string]Edge)
	remainingEdges := query.MaxNodes * 10
	for depth := 0; depth < query.Depth && len(frontier) > 0; depth++ {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		if remainingEdges == 0 {
			result.Truncated = true
			break
		}
		observedNodes, observedEdges, edgeTruncated, err := reader.ObservedGraph(ctx, query.Environment, query.At, query.MinConfidence, frontier, remainingEdges)
		if err != nil {
			return Result{}, err
		}
		if edgeTruncated {
			result.Truncated = true
		}
		remainingEdges -= len(observedEdges)
		for _, node := range observedNodes {
			nodeByID[node.ID] = node
		}
		sort.Slice(observedEdges, func(i, j int) bool { return edgeSemanticLess(observedEdges[i], observedEdges[j], nodeByID) })
		nextByID := make(map[string]Node)
		for _, edge := range observedEdges {
			if _, ok := seen[edge.To]; !ok {
				if len(seen) >= query.MaxNodes {
					result.Truncated = true
					continue
				}
				seen[edge.To] = depth + 1
				if node := nodeByID[edge.To]; node.Type == "service" {
					nextByID[node.ID] = node
				}
			}
			if _, ok := seen[edge.To]; ok {
				selectedEdges[edge.ID] = edge
			}
		}
		next := make([]Node, 0, len(nextByID))
		for _, node := range nextByID {
			next = append(next, node)
		}
		sort.Slice(next, func(i, j int) bool { return nodeSemanticLess(next[i], next[j]) })
		frontier = frontier[:0]
		for _, node := range next {
			frontier = append(frontier, node.ID)
		}
	}
	for id := range seen {
		if node, ok := nodeByID[id]; ok {
			result.Nodes = append(result.Nodes, node)
		}
	}
	for _, edge := range selectedEdges {
		result.Edges = append(result.Edges, edge)
	}
	sort.Slice(result.Nodes, func(i, j int) bool { return nodeSemanticLess(result.Nodes[i], result.Nodes[j]) })
	sort.Slice(result.Edges, func(i, j int) bool { return edgeSemanticLess(result.Edges[i], result.Edges[j], nodeByID) })
	if len(result.Edges) == 0 && !result.Truncated {
		result.Warnings = append(result.Warnings, "No observed telemetry relations matched the selected service and time.")
	}
	if result.Truncated {
		result.Warnings = append(result.Warnings, "Graph was truncated by node or edge limits.")
	}
	return result, nil
}

func nodeSemanticLess(left, right Node) bool {
	if comparison := compareNodeSemantics(left, right); comparison != 0 {
		return comparison < 0
	}
	return left.ID < right.ID
}

func edgeSemanticLess(left, right Edge, nodes map[string]Node) bool {
	leftFrom, rightFrom := nodes[left.From], nodes[right.From]
	if comparison := compareNodeSemantics(leftFrom, rightFrom); comparison != 0 {
		return comparison < 0
	}
	leftTo, rightTo := nodes[left.To], nodes[right.To]
	if comparison := compareNodeSemantics(leftTo, rightTo); comparison != 0 {
		return comparison < 0
	}
	if left.DependencyKind != right.DependencyKind {
		return left.DependencyKind < right.DependencyKind
	}
	if !left.WindowStart.Equal(right.WindowStart) {
		return left.WindowStart.Before(right.WindowStart)
	}
	if !left.WindowEnd.Equal(right.WindowEnd) {
		return left.WindowEnd.Before(right.WindowEnd)
	}
	if Rank(left.Confidence) != Rank(right.Confidence) {
		return Rank(left.Confidence) > Rank(right.Confidence)
	}
	if left.Basis != right.Basis {
		return left.Basis < right.Basis
	}
	if left.RequestCount != right.RequestCount {
		return left.RequestCount < right.RequestCount
	}
	if left.ErrorCount != right.ErrorCount {
		return left.ErrorCount < right.ErrorCount
	}
	if left.DurationSumNS != right.DurationSumNS {
		return left.DurationSumNS < right.DurationSumNS
	}
	if left.AlgorithmVersion != right.AlgorithmVersion {
		return left.AlgorithmVersion < right.AlgorithmVersion
	}
	if comparison := compareStrings(left.Limitations, right.Limitations); comparison != 0 {
		return comparison < 0
	}
	if comparison := compareStrings(left.EvidenceIDs, right.EvidenceIDs); comparison != 0 {
		return comparison < 0
	}
	if left.From != right.From {
		return left.From < right.From
	}
	if left.To != right.To {
		return left.To < right.To
	}
	return left.ID < right.ID
}

func compareNodeSemantics(left, right Node) int {
	if left.Type < right.Type {
		return -1
	}
	if left.Type > right.Type {
		return 1
	}
	if left.LogicalKey < right.LogicalKey {
		return -1
	}
	if left.LogicalKey > right.LogicalKey {
		return 1
	}
	if left.DisplayName < right.DisplayName {
		return -1
	}
	if left.DisplayName > right.DisplayName {
		return 1
	}
	return 0
}

func compareStrings(left, right []string) int {
	leftJSON, _ := json.Marshal(nonNilStrings(left))
	rightJSON, _ := json.Marshal(nonNilStrings(right))
	return strings.Compare(string(leftJSON), string(rightJSON))
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func Ambiguous(selector string, candidates []Node) error {
	values := make([]inventory.SelectorCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		values = append(values, inventory.SelectorCandidate{ID: candidate.ID, Type: candidate.Type, DisplayLabel: candidate.DisplayName})
	}
	return &inventory.AmbiguousSelectorError{Selector: selector, Candidates: values}
}
