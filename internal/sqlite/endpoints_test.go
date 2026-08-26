package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/telemetry"
)

func TestEndpointContextUsesActiveHalfOpenWindowsAndPreservesValues(t *testing.T) {
	start := time.Date(2026, 8, 24, 13, 40, 0, 0, time.UTC)
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "endpoints.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	coverage := 0.75
	snapshot := endpointSnapshot(start, start.Add(time.Minute), "a", "reference", []telemetry.Endpoint{
		{Key: "checkout\x00http\x00POST /checkout", ServiceKey: "checkout", Protocol: "http", Operation: "POST /checkout", RouteTemplate: "/checkout"},
		{Key: "checkout\x00http\x00GET /orders", ServiceKey: "checkout", Protocol: "http", Operation: "GET /orders"},
	}, []telemetry.Window{
		{Key: "checkout\x00http\x00POST /checkout", ServiceKey: "checkout", EndpointKey: "checkout\x00http\x00POST /checkout", WindowStart: start, WindowEnd: start.Add(time.Minute), RequestCount: 12, ErrorCount: 1, DurationSumNS: 123, P50NS: 10, P95NS: 20, P99NS: 30, CoverageRatio: &coverage},
		{Key: "checkout\x00http\x00GET /orders", ServiceKey: "checkout", EndpointKey: "checkout\x00http\x00GET /orders", WindowStart: start, WindowEnd: start.Add(time.Minute), RequestCount: 2, ErrorCount: 0, DurationSumNS: 20, P50NS: 5, P95NS: 10, P99NS: 15, IsComplete: true},
		{Key: "checkout\x00", ServiceKey: "checkout", WindowStart: start, WindowEnd: start.Add(time.Minute), RequestCount: 99, DurationSumNS: 99},
	})
	if _, err := store.SaveTelemetry(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		at   time.Time
		want int
	}{
		{name: "start included", at: start, want: 2},
		{name: "inside included", at: start.Add(30 * time.Second), want: 2},
		{name: "end excluded", at: start.Add(time.Minute), want: 0},
		{name: "before excluded", at: start.Add(-time.Nanosecond), want: 0},
		{name: "after excluded", at: start.Add(2 * time.Minute), want: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := store.EndpointContext(context.Background(), telemetry.EndpointQuery{ServiceKey: "checkout", Environment: "reference", At: test.at, Limit: 100})
			if err != nil || len(result.Items) != test.want {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if test.want == 0 && result.TelemetryStatus != "NO_ACTIVE_WINDOW" {
				t.Fatalf("status=%q warnings=%v", result.TelemetryStatus, result.Warnings)
			}
		})
	}
	result, err := store.EndpointContext(context.Background(), telemetry.EndpointQuery{ServiceKey: "checkout", Environment: "reference", At: start, Limit: 100})
	if err != nil || result.TelemetryStatus != "OBSERVED" || result.Items[0].Operation != "GET /orders" || result.Items[0].RouteTemplate != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	post := result.Items[1]
	if post.RouteTemplate == nil || *post.RouteTemplate != "/checkout" || len(post.Windows) != 1 || post.Windows[0].CoverageRatio == nil || *post.Windows[0].CoverageRatio != coverage || post.Windows[0].DurationSumNS != 123 || post.Windows[0].P50NS != 10 || post.Windows[0].P95NS != 20 || post.Windows[0].P99NS != 30 {
		t.Fatalf("POST endpoint=%+v", post)
	}
	if _, err := store.EndpointContext(context.Background(), telemetry.EndpointQuery{ServiceKey: "checkout", Environment: "other", At: start, Limit: 100}); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("wrong environment error=%v", err)
	}
	if _, err := store.EndpointContext(context.Background(), telemetry.EndpointQuery{ServiceKey: "missing", Environment: "reference", At: start, Limit: 100}); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("missing service error=%v", err)
	}
}

func TestEndpointContextKeepsOverlappingWindowsAndPaginatesByEndpoint(t *testing.T) {
	start := time.Date(2026, 8, 24, 13, 40, 0, 0, time.UTC)
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "pagination.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	endpoint := telemetry.Endpoint{Key: "checkout\x00http\x00POST /checkout", ServiceKey: "checkout", Protocol: "http", Operation: "POST /checkout", RouteTemplate: "/checkout"}
	first := endpointSnapshot(start, start.Add(time.Minute), "a", "reference", []telemetry.Endpoint{endpoint}, []telemetry.Window{{Key: endpoint.Key, ServiceKey: "checkout", EndpointKey: endpoint.Key, WindowStart: start, WindowEnd: start.Add(time.Minute), RequestCount: 5, DurationSumNS: 50}})
	second := endpointSnapshot(start.Add(30*time.Second), start.Add(90*time.Second), "b", "reference", []telemetry.Endpoint{endpoint, {Key: "checkout\x00grpc\x00Payment/Authorize", ServiceKey: "checkout", Protocol: "grpc", Operation: "Payment/Authorize"}}, []telemetry.Window{{Key: endpoint.Key, ServiceKey: "checkout", EndpointKey: endpoint.Key, WindowStart: start.Add(30 * time.Second), WindowEnd: start.Add(90 * time.Second), RequestCount: 7, DurationSumNS: 70}, {Key: "checkout\x00grpc\x00Payment/Authorize", ServiceKey: "checkout", EndpointKey: "checkout\x00grpc\x00Payment/Authorize", WindowStart: start.Add(30 * time.Second), WindowEnd: start.Add(90 * time.Second), RequestCount: 3, DurationSumNS: 30}})
	if _, err := store.SaveTelemetry(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveTelemetry(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	query := telemetry.EndpointQuery{ServiceKey: "checkout", Environment: "reference", At: start.Add(45 * time.Second), Limit: 1}
	page1, err := store.EndpointContext(context.Background(), query)
	if err != nil || len(page1.Items) != 1 || page1.NextCursor == "" || page1.Items[0].Protocol != "grpc" || len(page1.Items[0].Windows) != 1 {
		t.Fatalf("page1=%+v err=%v", page1, err)
	}
	query.Cursor = page1.NextCursor
	page2, err := store.EndpointContext(context.Background(), query)
	if err != nil || len(page2.Items) != 1 || page2.NextCursor != "" || page2.Items[0].Operation != "POST /checkout" || len(page2.Items[0].Windows) != 2 || page2.Items[0].Windows[0].RequestCount != 5 || page2.Items[0].Windows[1].RequestCount != 7 {
		t.Fatalf("page2=%+v err=%v", page2, err)
	}
	query.Cursor = "not-a-cursor"
	if _, err := store.EndpointContext(context.Background(), query); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("malformed cursor error=%v", err)
	}
	query.Cursor = page1.NextCursor
	query.Environment = "default"
	if _, err := store.EndpointContext(context.Background(), query); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("cross query cursor error=%v", err)
	}
}

func endpointSnapshot(start, end time.Time, hashByte, environment string, endpoints []telemetry.Endpoint, windows []telemetry.Window) telemetry.Snapshot {
	return telemetry.Snapshot{SourceKey: "file:endpoints-" + hashByte, SourceHash: "sha256:" + strings.Repeat(hashByte, 64), Environment: environment, WindowStart: start, WindowEnd: end, ObservedAt: end, Stats: telemetry.Stats{Services: 1, Endpoints: len(endpoints), TelemetryWindows: len(windows)}, Services: []telemetry.Service{{LogicalKey: "checkout", DisplayName: "checkout"}}, Endpoints: endpoints, Windows: windows}
}
