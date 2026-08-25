package sqlite

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/telemetry"
	"github.com/guijoazeiro/prodmap/internal/topology"
)

func TestTelemetryTargetResolutionIsTemporalAndOrderIndependent(t *testing.T) {
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	unresolved := telemetrySnapshot(start, "a")
	unresolved.Services = unresolved.Services[:1]
	unresolved.Endpoints = nil
	unresolved.Evidence = unresolved.Evidence[:1]
	unresolved.Observations[0].OriginEndpointKey = ""
	unresolved.Observations[0].TargetServiceKey = ""
	unresolved.Observations[0].Confidence = topology.Medium
	unresolved.Observations[0].Basis = "peer.service semantic convention"
	unresolved.Observations[0].EvidenceFingerprints = unresolved.Observations[0].EvidenceFingerprints[:1]
	unresolved.Observations[0].Limitations = []string{"No linked remote span or observed target service was present."}
	unresolved.Windows[0].Key = "checkout\x00"
	unresolved.Windows[0].EndpointKey = ""
	unresolved.Stats.Services, unresolved.Stats.Endpoints = 1, 0
	resolved := telemetrySnapshot(start.Add(10*time.Minute), "b")

	type semantic struct {
		targetType, targetKey string
		confidence            topology.Confidence
		limitations           string
	}
	run := func(t *testing.T, resolvedFirst bool) semantic {
		store, err := Open(context.Background(), filepath.Join(t.TempDir(), "temporal-target.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		var beforeNodes []topology.Node
		var beforeEdges []topology.Edge
		if resolvedFirst {
			if _, err := store.SaveTelemetry(context.Background(), resolved); err != nil {
				t.Fatal(err)
			}
			if _, err := store.SaveTelemetry(context.Background(), unresolved); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, err := store.SaveTelemetry(context.Background(), unresolved); err != nil {
				t.Fatal(err)
			}
			from := []string{telemetryServiceID(t, store, "checkout")}
			beforeNodes, beforeEdges, _, err = store.ObservedGraph(context.Background(), "default", start.Add(time.Minute), topology.Low, from, 100)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.SaveTelemetry(context.Background(), resolved); err != nil {
				t.Fatal(err)
			}
		}
		from := []string{telemetryServiceID(t, store, "checkout")}
		nodes, edges, _, err := store.ObservedGraph(context.Background(), "default", start.Add(time.Minute), topology.Low, from, 100)
		if err != nil {
			t.Fatal(err)
		}
		if beforeNodes != nil && (!reflect.DeepEqual(beforeNodes, nodes) || !reflect.DeepEqual(beforeEdges, edges)) {
			t.Fatalf("historical graph changed after future resolution:\nbefore=%+v/%+v\nafter=%+v/%+v", beforeNodes, beforeEdges, nodes, edges)
		}
		if len(edges) != 1 {
			t.Fatalf("historical edges=%+v", edges)
		}
		result := semantic{confidence: edges[0].Confidence, limitations: strings.Join(edges[0].Limitations, "|")}
		for _, node := range nodes {
			if node.ID == edges[0].To {
				result.targetType, result.targetKey = node.Type, node.LogicalKey
			}
		}
		_, futureEdges, _, err := store.ObservedGraph(context.Background(), "default", resolved.WindowStart.Add(time.Minute), topology.Low, from, 100)
		if err != nil || len(futureEdges) != 1 {
			t.Fatalf("future edges=%+v err=%v", futureEdges, err)
		}
		futureTargetType := ""
		futureNodes, _, _, _ := store.ObservedGraph(context.Background(), "default", resolved.WindowStart.Add(time.Minute), topology.Low, from, 100)
		for _, node := range futureNodes {
			if node.ID == futureEdges[0].To {
				futureTargetType = node.Type
			}
		}
		if futureTargetType != "service" {
			t.Fatalf("future target type=%q nodes=%+v", futureTargetType, futureNodes)
		}
		return result
	}
	forward, reverse := run(t, false), run(t, true)
	if !reflect.DeepEqual(forward, reverse) || forward.targetType != "dependency" || forward.targetKey != "payment" || forward.confidence != topology.Medium {
		t.Fatalf("temporal result forward=%+v reverse=%+v", forward, reverse)
	}
}

func TestTelemetrySaveIsIdempotentAndConflictsOnDifferentHash(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "telemetry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot := telemetrySnapshot(time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC), "a")
	first, err := store.SaveTelemetry(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	firstIDs := telemetryEntityIDs(t, store)
	second, err := store.SaveTelemetry(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if first.IngestionID != second.IngestionID || !second.IdempotentReplay {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	if secondIDs := telemetryEntityIDs(t, store); strings.Join(firstIDs, "\n") != strings.Join(secondIDs, "\n") {
		t.Fatalf("replay changed entity IDs:\nfirst=%v\nsecond=%v", firstIDs, secondIDs)
	}
	assertTelemetryCounts(t, store, map[string]int{"telemetry_ingestions": 1, "services": 2, "endpoints": 1, "dependencies": 1, "service_dependency_observations": 1, "topology_evidence": 2, "telemetry_windows": 1})
	conflict := snapshot
	conflict.SourceHash = "sha256:" + strings.Repeat("b", 64)
	if _, err := store.SaveTelemetry(context.Background(), conflict); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("different hash error = %v, want conflict", err)
	}
}

func TestTelemetryWindowsCanArriveOutOfOrderAndGraphIsTemporal(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "temporal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	later := telemetrySnapshot(start.Add(time.Hour), "b")
	earlier := telemetrySnapshot(start, "a")
	if _, err := store.SaveTelemetry(context.Background(), later); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveTelemetry(context.Background(), earlier); err != nil {
		t.Fatal(err)
	}
	from := []string{telemetryServiceID(t, store, "checkout")}
	outsideNodes, outsideEdges, _, err := store.ObservedGraph(context.Background(), "default", start.Add(-time.Second), topology.Low, from, 100)
	if err != nil || len(outsideNodes) != 0 || len(outsideEdges) != 0 {
		t.Fatalf("outside graph nodes=%d edges=%d err=%v", len(outsideNodes), len(outsideEdges), err)
	}
	_, atStart, _, err := store.ObservedGraph(context.Background(), "default", start, topology.Low, from, 100)
	if err != nil || len(atStart) != 1 {
		t.Fatalf("graph at window start edges=%d err=%v", len(atStart), err)
	}
	_, atEnd, _, err := store.ObservedGraph(context.Background(), "default", earlier.WindowEnd, topology.Low, from, 100)
	if err != nil || len(atEnd) != 0 {
		t.Fatalf("graph at window end edges=%d err=%v", len(atEnd), err)
	}
	nodes, edges, _, err := store.ObservedGraph(context.Background(), "default", start.Add(time.Minute), topology.Low, from, 100)
	if err != nil || len(nodes) != 2 || len(edges) != 1 || edges[0].Confidence != topology.High {
		t.Fatalf("inside graph nodes=%+v edges=%+v err=%v", nodes, edges, err)
	}
	_, exactEdges, _, err := store.ObservedGraph(context.Background(), "default", start.Add(time.Minute), topology.Exact, from, 100)
	if err != nil || len(exactEdges) != 0 {
		t.Fatalf("EXACT filter edges=%+v err=%v", exactEdges, err)
	}
}

func TestOutOfOrderTelemetryPreservesNewestDependencyMetadata(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	newer := telemetrySnapshot(start.Add(time.Hour), "b")
	newer.Dependencies[0].DisplayName = "payment-new"
	older := telemetrySnapshot(start, "a")
	older.Dependencies[0].DisplayName = "payment-old"
	// Import time is intentionally identical: metadata recency follows the
	// represented telemetry window, not the order in which frozen files arrive.
	newer.ObservedAt = start.Add(2 * time.Hour)
	older.ObservedAt = newer.ObservedAt
	for index := range newer.Evidence {
		newer.Evidence[index].ObservedAt = newer.ObservedAt
		older.Evidence[index].ObservedAt = older.ObservedAt
	}
	if _, err := store.SaveTelemetry(context.Background(), newer); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveTelemetry(context.Background(), older); err != nil {
		t.Fatal(err)
	}
	var display, firstSeen, lastSeen string
	if err := store.db.QueryRow(`SELECT display_name,first_seen_at,last_seen_at FROM dependencies WHERE logical_key='payment'`).Scan(&display, &firstSeen, &lastSeen); err != nil {
		t.Fatal(err)
	}
	if display != "payment-new" || firstSeen != formatTime(older.WindowEnd) || lastSeen != formatTime(newer.WindowEnd) {
		t.Fatalf("dependency metadata display=%q first=%q last=%q", display, firstSeen, lastSeen)
	}
}

func TestEqualWindowDependencyMetadataIsArrivalOrderIndependent(t *testing.T) {
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	for _, names := range [][2]string{{"zeta", "alpha"}, {"alpha", "zeta"}} {
		t.Run(names[0]+" then "+names[1], func(t *testing.T) {
			store, err := Open(context.Background(), filepath.Join(t.TempDir(), "metadata-tie.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			for index, name := range names {
				snapshot := telemetrySnapshot(start, string(rune('a'+index)))
				snapshot.SourceKey = fmt.Sprintf("file:fixture-%d", index)
				snapshot.Dependencies[0].DisplayName = name
				if _, err := store.SaveTelemetry(context.Background(), snapshot); err != nil {
					t.Fatal(err)
				}
			}
			var display string
			if err := store.db.QueryRow(`SELECT display_name FROM dependencies WHERE logical_key='payment'`).Scan(&display); err != nil {
				t.Fatal(err)
			}
			if display != "alpha" {
				t.Fatalf("display=%q", display)
			}
		})
	}
}

func TestConcurrentStoresIdempotentlySaveSameTelemetrySnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent.db")
	first, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	snapshot := telemetrySnapshot(time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC), "a")
	start := make(chan struct{})
	results := make(chan telemetry.IngestResult, 2)
	errorsChannel := make(chan error, 2)
	var wait sync.WaitGroup
	for _, store := range []*Store{first, second} {
		wait.Add(1)
		go func(store *Store) {
			defer wait.Done()
			<-start
			result, err := store.SaveTelemetry(context.Background(), snapshot)
			results <- result
			errorsChannel <- err
		}(store)
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("concurrent save: %v", err)
		}
	}
	var ingestionID string
	for result := range results {
		if ingestionID == "" {
			ingestionID = result.IngestionID
		} else if result.IngestionID != ingestionID {
			t.Fatalf("concurrent IDs differ: %s != %s", ingestionID, result.IngestionID)
		}
	}
	assertTelemetryCounts(t, first, map[string]int{"telemetry_ingestions": 1, "services": 2, "endpoints": 1, "dependencies": 1, "service_dependency_observations": 1, "topology_evidence": 2, "telemetry_windows": 1})
}

func TestConcurrentStoresConflictOnDifferentTelemetryHashes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent-conflict.db")
	first, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	startTime := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	snapshots := []telemetry.Snapshot{telemetrySnapshot(startTime, "a"), telemetrySnapshot(startTime, "b")}
	stores := []*Store{first, second}
	barrier := make(chan struct{})
	errorsChannel := make(chan error, 2)
	for index := range stores {
		go func(index int) {
			<-barrier
			_, saveErr := stores[index].SaveTelemetry(context.Background(), snapshots[index])
			errorsChannel <- saveErr
		}(index)
	}
	close(barrier)
	var successes, conflicts int
	for range stores {
		saveErr := <-errorsChannel
		switch {
		case saveErr == nil:
			successes++
		case errors.Is(saveErr, errs.ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent error: %v", saveErr)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	assertTelemetryCounts(t, first, map[string]int{"telemetry_ingestions": 1, "services": 2, "endpoints": 1, "dependencies": 1, "service_dependency_observations": 1, "topology_evidence": 2, "telemetry_windows": 1})
}

func TestInvalidChildWindowIsRejectedBeforeAnyTelemetryPersistence(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "invalid-child.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot := telemetrySnapshot(time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC), "a")
	snapshot.Observations[0].WindowEnd = snapshot.WindowEnd.Add(time.Second)
	if _, err := store.SaveTelemetry(context.Background(), snapshot); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("invalid child window error=%v", err)
	}
	assertTelemetryCounts(t, store, map[string]int{"telemetry_ingestions": 0, "service_dependency_observations": 0, "topology_evidence": 0, "telemetry_windows": 0})
}

func TestTelemetrySnapshotRejectsNULInPublicLogicalKey(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "nul-key.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot := telemetrySnapshot(time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC), "a")
	snapshot.Dependencies[0].LogicalKey = "payment\x00internal"
	if _, err := store.SaveTelemetry(context.Background(), snapshot); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("NUL logical key error=%v", err)
	}
	assertTelemetryCounts(t, store, map[string]int{"telemetry_ingestions": 0, "dependencies": 0})
}

func TestTelemetrySnapshotRejectsCrossServiceEndpointsDuplicateIdentitiesAndInvalidTargetsAtomically(t *testing.T) {
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		mutate func(*telemetry.Snapshot)
	}{
		{name: "observation endpoint ownership", mutate: func(snapshot *telemetry.Snapshot) {
			snapshot.Endpoints[0].ServiceKey = "payment"
		}},
		{name: "window endpoint ownership", mutate: func(snapshot *telemetry.Snapshot) {
			snapshot.Observations[0].OriginEndpointKey = ""
			snapshot.Endpoints[0].ServiceKey = "payment"
		}},
		{name: "duplicate observation identity", mutate: func(snapshot *telemetry.Snapshot) {
			snapshot.Observations = append(snapshot.Observations, snapshot.Observations[0])
			snapshot.Stats.Observations++
		}},
		{name: "duplicate endpoint persistence identity", mutate: func(snapshot *telemetry.Snapshot) {
			duplicate := snapshot.Endpoints[0]
			duplicate.Key += "\x00alias"
			snapshot.Endpoints = append(snapshot.Endpoints, duplicate)
			snapshot.Stats.Endpoints++
		}},
		{name: "duplicate dependency persistence identity", mutate: func(snapshot *telemetry.Snapshot) {
			duplicate := snapshot.Dependencies[0]
			duplicate.Key += "\x00alias"
			snapshot.Dependencies = append(snapshot.Dependencies, duplicate)
			snapshot.Stats.Dependencies++
		}},
		{name: "duplicate window identity", mutate: func(snapshot *telemetry.Snapshot) {
			snapshot.Windows = append(snapshot.Windows, snapshot.Windows[0])
			snapshot.Stats.TelemetryWindows++
		}},
		{name: "non-service target", mutate: func(snapshot *telemetry.Snapshot) {
			snapshot.Dependencies[0].Kind = "database"
		}},
		{name: "mismatched service target", mutate: func(snapshot *telemetry.Snapshot) {
			snapshot.Dependencies[0].LogicalKey = "checkout"
		}},
		{name: "noncanonical environment", mutate: func(snapshot *telemetry.Snapshot) {
			snapshot.Environment = " default "
		}},
		{name: "non-finite coverage", mutate: func(snapshot *telemetry.Snapshot) {
			value := math.NaN()
			snapshot.Windows[0].CoverageRatio = &value
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := Open(context.Background(), filepath.Join(t.TempDir(), "invalid-telemetry.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			snapshot := telemetrySnapshot(start, "a")
			test.mutate(&snapshot)
			if _, err := store.SaveTelemetry(context.Background(), snapshot); !errors.Is(err, errs.ErrInvalid) {
				t.Fatalf("SaveTelemetry error=%v, want invalid", err)
			}
			assertTelemetryCounts(t, store, map[string]int{"telemetry_ingestions": 0, "endpoints": 0, "dependencies": 0, "topology_evidence": 0, "service_dependency_observations": 0, "telemetry_windows": 0})
		})
	}
}

func TestObservedGraphHasExplicitEdgeLimit(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "edge-limit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	snapshot := telemetrySnapshot(start, "a")
	snapshot.Services = snapshot.Services[:1]
	snapshot.Dependencies = nil
	snapshot.Observations = nil
	snapshot.Evidence = nil
	for index := 0; index < 12; index++ {
		key := fmt.Sprintf("db-%02d", index)
		fingerprint := telemetry.EvidenceFingerprintV1 + ":" + fmt.Sprintf("%064x", index+1)
		snapshot.Dependencies = append(snapshot.Dependencies, telemetry.Dependency{Key: "database\x00" + key, LogicalKey: key, Kind: "database", DisplayName: key})
		snapshot.Evidence = append(snapshot.Evidence, telemetry.Evidence{Fingerprint: fingerprint, Claim: "outbound dependency observation", ObservedAt: snapshot.ObservedAt})
		snapshot.Observations = append(snapshot.Observations, telemetry.DependencyObservation{
			Key: "checkout\x00\x00database\x00" + key, FromServiceKey: "checkout", DependencyKey: "database\x00" + key,
			WindowStart: snapshot.WindowStart, WindowEnd: snapshot.WindowEnd, RequestCount: 1, DurationSumNS: 1,
			Confidence: topology.Medium, Basis: "database semantic convention", EvidenceFingerprints: []string{fingerprint},
		})
	}
	snapshot.Stats.Services = len(snapshot.Services)
	snapshot.Stats.Dependencies = len(snapshot.Dependencies)
	snapshot.Stats.Observations = len(snapshot.Observations)
	if _, err := store.SaveTelemetry(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	from := []string{telemetryServiceID(t, store, "checkout")}
	_, edges, truncated, err := store.ObservedGraph(context.Background(), "default", start.Add(time.Minute), topology.Low, from, 5)
	if err != nil || !truncated || len(edges) != 5 {
		t.Fatalf("edges=%d truncated=%t err=%v", len(edges), truncated, err)
	}
}

func TestGraphEdgeBudgetIsAppliedToTheSelectedFrontier(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "frontier-limit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	irrelevant := telemetrySnapshot(start, "a")
	irrelevant.Services = []telemetry.Service{{LogicalKey: "a", DisplayName: "a"}}
	irrelevant.Endpoints, irrelevant.Dependencies, irrelevant.Observations, irrelevant.Evidence = nil, nil, nil, nil
	irrelevant.Windows = []telemetry.Window{{Key: "a\x00", ServiceKey: "a", WindowStart: irrelevant.WindowStart, WindowEnd: irrelevant.WindowEnd}}
	for index := 0; index < 25; index++ {
		key := fmt.Sprintf("irrelevant-%02d.invalid", index)
		fingerprint := telemetry.EvidenceFingerprintV1 + ":" + fmt.Sprintf("%064x", index+1)
		irrelevant.Dependencies = append(irrelevant.Dependencies, telemetry.Dependency{Key: "external_api\x00" + key, LogicalKey: key, Kind: "external_api", DisplayName: key})
		irrelevant.Evidence = append(irrelevant.Evidence, telemetry.Evidence{Fingerprint: fingerprint, Claim: "outbound dependency observation", ObservedAt: irrelevant.ObservedAt})
		irrelevant.Observations = append(irrelevant.Observations, telemetry.DependencyObservation{
			Key: "a\x00\x00external_api\x00" + key, FromServiceKey: "a", DependencyKey: "external_api\x00" + key,
			WindowStart: irrelevant.WindowStart, WindowEnd: irrelevant.WindowEnd, RequestCount: 1, DurationSumNS: 1,
			Confidence: topology.Low, Basis: "observed server.address", EvidenceFingerprints: []string{fingerprint},
		})
	}
	irrelevant.Stats.Services, irrelevant.Stats.Endpoints = 1, 0
	irrelevant.Stats.Dependencies, irrelevant.Stats.Observations, irrelevant.Stats.TelemetryWindows = 25, 25, 1
	if _, err := store.SaveTelemetry(context.Background(), irrelevant); err != nil {
		t.Fatal(err)
	}
	target := telemetrySnapshot(start, "b")
	target.SourceKey = "file:target"
	target.Services = []telemetry.Service{{LogicalKey: "z", DisplayName: "z"}}
	target.Endpoints = nil
	target.Dependencies = []telemetry.Dependency{{Key: "external_api\x00z-target.invalid", LogicalKey: "z-target.invalid", Kind: "external_api", DisplayName: "z-target.invalid"}}
	target.Evidence = []telemetry.Evidence{{Fingerprint: telemetry.EvidenceFingerprintV1 + ":" + strings.Repeat("f", 64), Claim: "outbound dependency observation", ObservedAt: target.ObservedAt}}
	target.Observations = []telemetry.DependencyObservation{{
		Key: "z\x00\x00external_api\x00z-target.invalid", FromServiceKey: "z", DependencyKey: "external_api\x00z-target.invalid",
		WindowStart: target.WindowStart, WindowEnd: target.WindowEnd, RequestCount: 1, DurationSumNS: 1,
		Confidence: topology.Low, Basis: "observed server.address", EvidenceFingerprints: []string{target.Evidence[0].Fingerprint},
	}}
	target.Windows = []telemetry.Window{{Key: "z\x00", ServiceKey: "z", WindowStart: target.WindowStart, WindowEnd: target.WindowEnd}}
	target.Stats.Services, target.Stats.Endpoints, target.Stats.Dependencies, target.Stats.Observations, target.Stats.TelemetryWindows = 1, 0, 1, 1, 1
	if _, err := store.SaveTelemetry(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	result, err := topology.Build(context.Background(), store, topology.Query{Service: "z", Environment: "default", At: start.Add(time.Minute), Depth: 1, MinConfidence: topology.Low, MaxNodes: 2})
	if err != nil || result.Truncated || len(result.Nodes) != 2 || len(result.Edges) != 1 {
		t.Fatalf("selected frontier graph=%+v err=%v", result, err)
	}
}

func TestObservedGraphPreLimitMatchesSemanticTargetTypeOrdering(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "semantic-edge-limit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	snapshot := telemetrySnapshot(start, "a")
	snapshot.Services = []telemetry.Service{{LogicalKey: "api", DisplayName: "api"}, {LogicalKey: "aaa", DisplayName: "aaa"}}
	snapshot.Endpoints = nil
	snapshot.Dependencies = []telemetry.Dependency{
		{Key: "service\x00aaa", LogicalKey: "aaa", Kind: "service", DisplayName: "aaa"},
		{Key: "service\x00zzz", LogicalKey: "zzz", Kind: "service", DisplayName: "zzz"},
	}
	snapshot.Evidence = []telemetry.Evidence{
		{Fingerprint: telemetry.EvidenceFingerprintV1 + ":" + strings.Repeat("a", 64), Claim: "outbound dependency observation", ObservedAt: snapshot.ObservedAt},
		{Fingerprint: telemetry.EvidenceFingerprintV1 + ":" + strings.Repeat("b", 64), Claim: "outbound dependency observation", ObservedAt: snapshot.ObservedAt},
	}
	snapshot.Observations = []telemetry.DependencyObservation{
		{Key: "api\x00\x00service\x00aaa", FromServiceKey: "api", DependencyKey: "service\x00aaa", TargetServiceKey: "aaa", WindowStart: snapshot.WindowStart, WindowEnd: snapshot.WindowEnd, RequestCount: 1, DurationSumNS: 1, Confidence: topology.Medium, Basis: "peer.service semantic convention", EvidenceFingerprints: []string{snapshot.Evidence[0].Fingerprint}},
		{Key: "api\x00\x00service\x00zzz", FromServiceKey: "api", DependencyKey: "service\x00zzz", WindowStart: snapshot.WindowStart, WindowEnd: snapshot.WindowEnd, RequestCount: 1, DurationSumNS: 1, Confidence: topology.Medium, Basis: "peer.service semantic convention", EvidenceFingerprints: []string{snapshot.Evidence[1].Fingerprint}},
	}
	snapshot.Windows = []telemetry.Window{{Key: "api\x00", ServiceKey: "api", WindowStart: snapshot.WindowStart, WindowEnd: snapshot.WindowEnd}}
	snapshot.Stats.Services, snapshot.Stats.Endpoints, snapshot.Stats.Dependencies, snapshot.Stats.Observations, snapshot.Stats.TelemetryWindows = 2, 0, 2, 2, 1
	if _, err := store.SaveTelemetry(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	from := []string{telemetryServiceID(t, store, "api")}
	nodes, edges, truncated, err := store.ObservedGraph(context.Background(), "default", start.Add(time.Minute), topology.Low, from, 1)
	if err != nil || !truncated || len(edges) != 1 {
		t.Fatalf("nodes=%+v edges=%+v truncated=%t err=%v", nodes, edges, truncated, err)
	}
	foundDependency := false
	for _, node := range nodes {
		if node.Type == "dependency" && node.LogicalKey == "zzz" {
			foundDependency = true
		}
	}
	if !foundDependency {
		t.Fatalf("pre-limit selected a different semantic target: nodes=%+v edges=%+v", nodes, edges)
	}
}

func TestTelemetrySchemaRejectsMalformedUUIDTimestampAndJSONArray(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "constraints.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot := telemetrySnapshot(time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC), "a")
	if _, err := store.SaveTelemetry(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	_, err = store.db.Exec(`INSERT INTO telemetry_ingestions(
		id,source_id,source_hash,environment,window_start,window_end,observed_at,ingested_at,
		lines,resource_spans,spans_seen,spans_accepted,spans_ignored,services_count,endpoints_count,
		dependencies_count,observations_count,telemetry_windows_count,warnings_json,created_at)
		SELECT 'xxxxxxxx-xxxx-7xxx-8xxx-xxxxxxxxxxxx',source_id,source_hash,environment,
		'2026-08-20T12:00:00Z','2026-08-20T12:05:00Z',observed_at,ingested_at,
		lines,resource_spans,spans_seen,spans_accepted,spans_ignored,services_count,endpoints_count,
		dependencies_count,observations_count,telemetry_windows_count,warnings_json,created_at
		FROM telemetry_ingestions LIMIT 1`)
	if err == nil {
		t.Fatal("schema accepted a non-hex UUIDv7")
	}
	if _, err := store.db.Exec(`INSERT INTO endpoints(id,service_id,protocol,operation,route_template,first_seen_at,last_seen_at,created_at,updated_at)
		SELECT NULL,id,'http','GET /null',NULL,last_seen_at,last_seen_at,created_at,updated_at FROM services LIMIT 1`); err == nil {
		t.Fatal("schema accepted a NULL UUIDv7 primary key")
	}
	if _, err := store.db.Exec(`UPDATE telemetry_ingestions SET observed_at='2026-99-99T99:99:99Z'`); err == nil {
		t.Fatal("schema accepted an invalid UTC timestamp")
	}
	for _, value := range []string{
		"2026-08-19 12:00:00Z",
		"2026-08-19T12:00Z",
		"2026-08-19T12:00:00.1Z",
		"2026-02-30T12:00:00.000000000Z",
		"2026-99-01T12:00:00.000000000Z",
		"2026-08-19T99:00:00.000000000Z",
		"2026-08-19T24:00:00.000000000Z",
	} {
		if _, err := store.db.Exec(`UPDATE telemetry_ingestions SET observed_at=?`, value); err == nil {
			t.Fatalf("schema accepted noncanonical timestamp %q", value)
		}
	}
	if _, err := store.db.Exec(`UPDATE telemetry_ingestions SET warnings_json='{}'`); err == nil {
		t.Fatal("schema accepted non-array warnings JSON")
	}
}

func telemetrySnapshot(start time.Time, hashByte string) telemetry.Snapshot {
	end := start.Add(5 * time.Minute)
	fingerprint := telemetry.EvidenceFingerprintV1 + ":" + strings.Repeat("c", 64)
	linkedFingerprint := telemetry.EvidenceFingerprintV1 + ":" + strings.Repeat("d", 64)
	endpointKey := "checkout\x00http\x00GET /checkout/{id}"
	stats := telemetry.Stats{Lines: 1, ResourceSpans: 2, SpansSeen: 3, SpansAccepted: 3, Services: 2, Endpoints: 1, Dependencies: 1, Observations: 1, TelemetryWindows: 1}
	return telemetry.Snapshot{
		SourceKey: "file:fixture", SourceHash: "sha256:" + strings.Repeat(hashByte, 64), Environment: "default",
		WindowStart: start, WindowEnd: end, ObservedAt: end, Stats: stats,
		Services:     []telemetry.Service{{LogicalKey: "checkout", DisplayName: "checkout"}, {LogicalKey: "payment", DisplayName: "payment"}},
		Endpoints:    []telemetry.Endpoint{{Key: endpointKey, ServiceKey: "checkout", Protocol: "http", Operation: "GET /checkout/{id}", RouteTemplate: "/checkout/{id}"}},
		Dependencies: []telemetry.Dependency{{Key: "service\x00payment", LogicalKey: "payment", Kind: "service", DisplayName: "payment"}},
		Evidence: []telemetry.Evidence{
			{Fingerprint: fingerprint, Claim: "outbound client span", ObservedAt: end},
			{Fingerprint: linkedFingerprint, Claim: "linked remote server span", ObservedAt: end},
		},
		Observations: []telemetry.DependencyObservation{{
			Key: "checkout\x00" + endpointKey + "\x00service\x00payment", FromServiceKey: "checkout", OriginEndpointKey: endpointKey, DependencyKey: "service\x00payment", TargetServiceKey: "payment",
			WindowStart: start, WindowEnd: end, RequestCount: 2, ErrorCount: 1, DurationSumNS: 30,
			Confidence: topology.High, Basis: "parent/child client/server propagation", EvidenceFingerprints: []string{fingerprint, linkedFingerprint},
		}},
		Windows: []telemetry.Window{{Key: endpointKey, ServiceKey: "checkout", EndpointKey: endpointKey, WindowStart: start, WindowEnd: end, RequestCount: 2, ErrorCount: 1, DurationSumNS: 30, P50NS: 10, P95NS: 20, P99NS: 20}},
	}
}

func assertTelemetryCounts(t *testing.T, store *Store, expected map[string]int) {
	t.Helper()
	for table, want := range expected {
		var got int
		if err := store.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if got != want {
			t.Fatalf("%s count=%d want=%d", table, got, want)
		}
	}
}

func telemetryEntityIDs(t *testing.T, store *Store) []string {
	t.Helper()
	var result []string
	for _, table := range []string{"telemetry_ingestions", "services", "endpoints", "dependencies", "topology_evidence", "service_dependency_observations", "telemetry_windows"} {
		rows, err := store.db.Query("SELECT id FROM " + table + " ORDER BY id")
		if err != nil {
			t.Fatalf("query IDs from %s: %v", table, err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			result = append(result, table+":"+id)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(result)
	return result
}

func telemetryServiceID(t *testing.T, store *Store, logicalKey string) string {
	t.Helper()
	var id string
	if err := store.db.QueryRow("SELECT id FROM services WHERE logical_key=?", logicalKey).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
