package telemetry

import "testing"

func TestNearestRank(t *testing.T) {
	values := []int64{40, 10, 30, 20}
	if got := NearestRank(values, .50); got != 20 {
		t.Fatalf("p50 = %d, want 20", got)
	}
	if got := NearestRank(values, .95); got != 40 {
		t.Fatalf("p95 = %d, want 40", got)
	}
	if got := NearestRank([]int64{7}, .99); got != 7 {
		t.Fatalf("single p99 = %d, want 7", got)
	}
}
