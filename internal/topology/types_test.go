package topology

import (
	"context"
	"strings"
	"testing"
	"time"
)

type graphReader struct {
	roots     []Node
	nodes     []Node
	edges     []Edge
	truncated bool
}

func (r graphReader) ResolveGraphRoots(context.Context, string, string, bool) ([]Node, error) {
	return append([]Node(nil), r.roots...), nil
}

func (r graphReader) ObservedGraph(_ context.Context, _ string, _ time.Time, _ Confidence, from []string, limit int) ([]Node, []Edge, bool, error) {
	allowed := make(map[string]struct{}, len(from))
	for _, id := range from {
		allowed[id] = struct{}{}
	}
	edges := make([]Edge, 0, len(r.edges))
	for _, edge := range r.edges {
		if _, ok := allowed[edge.From]; ok {
			edges = append(edges, edge)
		}
	}
	truncated := r.truncated || len(edges) > limit
	if len(edges) > limit {
		edges = edges[:limit]
	}
	return append([]Node(nil), r.nodes...), edges, truncated, nil
}

func TestBuildChoosesSameSemanticSubsetWhenUUIDsDiffer(t *testing.T) {
	build := func(aID, bID, cID, abID, acID string) Result {
		a := Node{ID: aID, Type: "service", LogicalKey: "a", DisplayName: "a"}
		b := Node{ID: bID, Type: "service", LogicalKey: "b", DisplayName: "b"}
		c := Node{ID: cID, Type: "service", LogicalKey: "c", DisplayName: "c"}
		result, err := Build(context.Background(), graphReader{
			roots: []Node{a}, nodes: []Node{c, a, b},
			edges: []Edge{{ID: acID, From: aID, To: cID, DependencyKind: "service"}, {ID: abID, From: aID, To: bID, DependencyKind: "service"}},
		}, Query{Environment: "default", At: time.Unix(0, 0), Depth: 1, MinConfidence: Low, MaxNodes: 2})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := build("z-root", "z-target", "a-target", "z-edge", "a-edge")
	second := build("a-root", "a-target", "z-target", "a-edge", "z-edge")
	for _, result := range []Result{first, second} {
		if !result.Truncated || len(result.Nodes) != 2 || result.Nodes[0].LogicalKey != "a" || result.Nodes[1].LogicalKey != "b" || len(result.Edges) != 1 {
			t.Fatalf("semantic subset=%+v", result)
		}
	}
}

func TestBuildPropagatesEdgeTruncation(t *testing.T) {
	a := Node{ID: "a", Type: "service", LogicalKey: "a", DisplayName: "a"}
	result, err := Build(context.Background(), graphReader{roots: []Node{a}, nodes: []Node{a}, truncated: true}, Query{
		Environment: "default", At: time.Unix(0, 0), Depth: 1, MinConfidence: Low, MaxNodes: 2,
	})
	if err != nil || !result.Truncated || len(result.Warnings) == 0 {
		t.Fatalf("edge-truncated result=%+v err=%v", result, err)
	}
}

func TestBuildDoesNotClaimAbsenceWhenTheNodeLimitSuppressesRelations(t *testing.T) {
	a := Node{ID: "a", Type: "service", LogicalKey: "a", DisplayName: "a"}
	b := Node{ID: "b", Type: "service", LogicalKey: "b", DisplayName: "b"}
	result, err := Build(context.Background(), graphReader{
		roots: []Node{a}, nodes: []Node{a, b}, edges: []Edge{{ID: "ab", From: "a", To: "b", DependencyKind: "service"}},
	}, Query{Environment: "default", At: time.Unix(0, 0), Depth: 1, MinConfidence: Low, MaxNodes: 1})
	if err != nil || !result.Truncated || len(result.Edges) != 0 {
		t.Fatalf("truncated result=%+v err=%v", result, err)
	}
	if strings.Contains(strings.Join(result.Warnings, " "), "No observed telemetry relations") {
		t.Fatalf("truncated result falsely claimed absence: %v", result.Warnings)
	}
}

func TestBuildHandlesCyclesWithoutDuplicatesAndTruncatesDeterministically(t *testing.T) {
	a := Node{ID: "a", Type: "service", LogicalKey: "a", DisplayName: "a"}
	b := Node{ID: "b", Type: "service", LogicalKey: "b", DisplayName: "b"}
	c := Node{ID: "c", Type: "service", LogicalKey: "c", DisplayName: "c"}
	reader := graphReader{roots: []Node{a}, nodes: []Node{c, b, a}, edges: []Edge{{ID: "ab", From: "a", To: "b"}, {ID: "ba", From: "b", To: "a"}, {ID: "bc", From: "b", To: "c"}}}
	result, err := Build(context.Background(), reader, Query{Environment: "default", At: time.Now(), Depth: 5, MinConfidence: Low, MaxNodes: 3})
	if err != nil || result.Truncated || len(result.Nodes) != 3 || len(result.Edges) != 3 {
		t.Fatalf("cycle result=%+v err=%v", result, err)
	}
	result, err = Build(context.Background(), reader, Query{Environment: "default", At: time.Now(), Depth: 5, MinConfidence: Low, MaxNodes: 2})
	if err != nil || !result.Truncated || len(result.Nodes) != 2 {
		t.Fatalf("truncated result=%+v err=%v", result, err)
	}
}

func TestEdgeSemanticOrderUsesEveryPublicFieldBeforeUUID(t *testing.T) {
	nodes := map[string]Node{
		"from": {ID: "from", Type: "service", LogicalKey: "from", DisplayName: "from"},
		"to":   {ID: "to", Type: "dependency", LogicalKey: "to", DisplayName: "to"},
	}
	base := Edge{
		ID: "z", From: "from", To: "to", DependencyKind: "database",
		WindowStart: time.Unix(1, 0), WindowEnd: time.Unix(2, 0), Confidence: High,
		Basis: "alpha", RequestCount: 1, ErrorCount: 1, DurationSumNS: 1,
		AlgorithmVersion: "alpha", Limitations: []string{"alpha"}, EvidenceIDs: []string{"alpha"},
	}
	for _, test := range []struct {
		name   string
		mutate func(*Edge)
	}{
		{name: "confidence", mutate: func(edge *Edge) { edge.Confidence = Low }},
		{name: "basis", mutate: func(edge *Edge) { edge.Basis = "beta" }},
		{name: "request count", mutate: func(edge *Edge) { edge.RequestCount = 2 }},
		{name: "error count", mutate: func(edge *Edge) { edge.ErrorCount = 2 }},
		{name: "duration", mutate: func(edge *Edge) { edge.DurationSumNS = 2 }},
		{name: "algorithm", mutate: func(edge *Edge) { edge.AlgorithmVersion = "beta" }},
		{name: "limitations", mutate: func(edge *Edge) { edge.Limitations = []string{"beta"} }},
		{name: "evidence", mutate: func(edge *Edge) { edge.EvidenceIDs = []string{"beta"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			later := base
			later.ID = "a"
			test.mutate(&later)
			if !edgeSemanticLess(base, later, nodes) || edgeSemanticLess(later, base, nodes) {
				t.Fatalf("semantic field %q did not take precedence over UUID", test.name)
			}
		})
	}
}

func TestEdgeSemanticOrderUsesDependencyKindBeforeNodeUUIDForEquivalentTargets(t *testing.T) {
	nodes := map[string]Node{
		"from": {ID: "from", Type: "service", LogicalKey: "from", DisplayName: "from"},
		"z-to": {ID: "z-to", Type: "dependency", LogicalKey: "same", DisplayName: "same"},
		"a-to": {ID: "a-to", Type: "dependency", LogicalKey: "same", DisplayName: "same"},
	}
	database := Edge{ID: "z-edge", From: "from", To: "z-to", DependencyKind: "database", WindowStart: time.Unix(1, 0), WindowEnd: time.Unix(2, 0)}
	queue := Edge{ID: "a-edge", From: "from", To: "a-to", DependencyKind: "queue", WindowStart: database.WindowStart, WindowEnd: database.WindowEnd}
	if !edgeSemanticLess(database, queue, nodes) || edgeSemanticLess(queue, database, nodes) {
		t.Fatalf("dependency kind did not precede target UUID in edge ordering")
	}
}
