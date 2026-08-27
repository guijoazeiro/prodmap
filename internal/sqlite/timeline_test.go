package sqlite

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/deployment"
	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/timeline"
)

func TestTimelineRollbackConcurrentRuntimeAndPagination(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot, err := deployment.LoadFrozenFile(t.Context(), filepath.Join("..", "..", "testdata", "deployment", "timeline-rollback-concurrent.jsonl"), time.Date(2026, 8, 26, 12, 11, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveDeployment(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	seedArtifactAndCommit(t, store, snapshot.Records[0], time.Date(2026, 8, 26, 12, 0, 30, 0, time.UTC))
	query := timeline.Query{Environment: "reference", Since: time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC), Until: time.Date(2026, 8, 26, 12, 11, 0, 0, time.UTC), Limit: 100}
	result, err := store.Timeline(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 4 || result.Items[0].Kind != "rollback_declared" || !strings.Contains(strings.Join(result.Items[0].Limitations, " "), "does not prove runtime effect") {
		t.Fatalf("timeline=%#v", result.Items)
	}
	concurrent := 0
	for _, event := range result.Items {
		if event.Kind == "deployment_running" {
			concurrent++
			if !event.Concurrency.Detected || event.Concurrency.Count != 2 {
				t.Fatalf("concurrency=%#v", event.Concurrency)
			}
		}
		if event.CausalityClaimed || event.Confidence.Level == "EXACT" {
			t.Fatalf("causal/exact event=%#v", event)
		}
	}
	if concurrent != 2 {
		t.Fatalf("running events=%d", concurrent)
	}
	seen := map[string]bool{}
	page := query
	page.Limit = 1
	for {
		one, err := store.Timeline(t.Context(), page)
		if err != nil {
			t.Fatal(err)
		}
		if len(one.Items) == 0 {
			break
		}
		if seen[one.Items[0].ID] {
			t.Fatalf("duplicate event %s", one.Items[0].ID)
		}
		seen[one.Items[0].ID] = true
		if one.NextCursor == "" {
			break
		}
		page.Cursor = one.NextCursor
	}
	if len(seen) != len(result.Items) {
		t.Fatalf("pages=%d all=%d", len(seen), len(result.Items))
	}
	page.Environment = "other"
	if _, err := store.Timeline(t.Context(), page); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("incompatible cursor error=%v", err)
	}
}

func TestTimelineOrdersSameTimestampByPriorityThenID(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fixturePath := filepath.Join("..", "..", "testdata", "deployment", "timeline-rollback-concurrent.jsonl")
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "same-timestamp.jsonl")
	content := string(raw)
	content = strings.Replace(content, "2026-08-26T12:10:00Z", "2026-08-26T12:00:00Z", 1)
	content = strings.Replace(content, "2026-08-26T12:09:00Z", "2026-08-26T11:59:00Z", 1)
	content = strings.Replace(content, "2026-08-26T12:08:00Z", "2026-08-26T11:58:00Z", 1)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := deployment.LoadFrozenFile(t.Context(), path, time.Date(2026, 8, 26, 12, 11, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveDeployment(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	seedArtifactAndCommit(t, store, snapshot.Records[0], time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC))
	result, err := store.Timeline(t.Context(), timeline.Query{Environment: "reference", Since: time.Date(2026, 8, 26, 11, 59, 0, 0, time.UTC), Until: time.Date(2026, 8, 26, 12, 1, 0, 0, time.UTC), Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 4 {
		t.Fatalf("items=%#v", result.Items)
	}
	if result.Items[0].Kind != "rollback_declared" || result.Items[3].Kind != "runtime_observed" {
		t.Fatalf("priority order=%#v", result.Items)
	}
	if result.Items[1].Kind != "deployment_running" || result.Items[2].Kind != "deployment_running" || result.Items[1].ID >= result.Items[2].ID {
		t.Fatalf("deployment id order=%q,%q", result.Items[1].ID, result.Items[2].ID)
	}
}

func TestTimelineUsesHalfOpenBoundaries(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "prodmap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot, err := deployment.LoadFrozenFile(t.Context(), filepath.Join("..", "..", "testdata", "deployment", "valid-two-services.jsonl"), time.Date(2026, 8, 26, 12, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveDeployment(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	at := snapshot.Records[0].DeployedAt
	inside, err := store.Timeline(t.Context(), timeline.Query{Environment: "reference", Since: at, Until: at.Add(time.Nanosecond), Limit: 100})
	if err != nil || len(inside.Items) != 2 {
		t.Fatalf("since boundary items=%d err=%v", len(inside.Items), err)
	}
	out, err := store.Timeline(t.Context(), timeline.Query{Environment: "reference", Since: at.Add(-time.Nanosecond), Until: at, Limit: 100})
	if err != nil || len(out.Items) != 0 {
		t.Fatalf("until boundary items=%d err=%v", len(out.Items), err)
	}
}
