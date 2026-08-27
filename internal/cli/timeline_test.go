package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/timeline"
)

func TestTimelineCLIJSONHumanHelpAndValidation(t *testing.T) {
	project := t.TempDir()
	app, stdout, stderr := testApp(project)
	fixed := time.Date(2026, 8, 26, 12, 30, 0, 0, time.UTC)
	app.Now = func() time.Time { return fixed }
	fixture, err := filepath.Abs(filepath.Join("..", "..", "testdata", "deployment", "timeline-rollback-concurrent.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if code := app.Run(t.Context(), []string{"init", "--project-dir", project, "--json"}); code != 0 {
		t.Fatal(code)
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(t.Context(), []string{"deployments", "ingest", "--file", fixture, "--project-dir", project, "--json"}); code != 0 {
		t.Fatalf("ingest=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(t.Context(), []string{"timeline", "--environment", "reference", "--project-dir", project, "--json"}); code != 0 {
		t.Fatalf("json=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	envelope := decodeEnvelope(t, stdout.Bytes())
	data := envelope["data"].(map[string]any)
	if data["since"] != "2026-08-26T10:30:00Z" || len(data["items"].([]any)) != 3 || stderr.Len() != 0 {
		t.Fatalf("timeline data=%#v stderr=%q", data, stderr.String())
	}
	for _, raw := range data["items"].([]any) {
		item := raw.(map[string]any)
		if item["causality_claimed"] != false || item["confidence"].(map[string]any)["level"] == "EXACT" || item["limitations"] == nil {
			t.Fatalf("item=%#v", item)
		}
	}
	stdout.Reset()
	stderr.Reset()
	app.Now = func() time.Time { return time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC) }
	if code := app.Run(t.Context(), []string{"timeline", "--environment", "reference", "--since", "2026-08-26T11:00:00Z", "--until", "2026-08-26T13:00:00Z", "--project-dir", project}); code != 0 || !strings.Contains(stdout.String(), "Timeline: reference") || !strings.Contains(stdout.String(), "UNKNOWN") || strings.Contains(stdout.String(), "%!s") || stderr.Len() != 0 {
		t.Fatalf("human code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(t.Context(), []string{"timeline", "--help"}); code != 0 || !strings.Contains(stdout.String(), "usage: prodmap timeline") || stderr.Len() != 0 {
		t.Fatalf("help code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(t.Context(), []string{"timeline", "--since", "not-time", "--json", "--project-dir", project}); code != 2 || stderr.Len() != 0 {
		t.Fatalf("invalid code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestTimelineOutputFreshnessAndFutureSafety(t *testing.T) {
	generatedAt := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	past := generatedAt.Add(-10 * time.Second)
	tests := []struct {
		name                 string
		eventTime            time.Time
		sourceObservedAt     time.Time
		wantLevel            string
		wantFreshness        *int64
		wantBasis            string
		wantFutureLimitation bool
	}{
		{name: "past", eventTime: past, sourceObservedAt: past, wantLevel: "HIGH", wantFreshness: int64Pointer(10)},
		{name: "future source", eventTime: past, sourceObservedAt: generatedAt.Add(time.Second), wantLevel: "UNKNOWN", wantBasis: "source timestamp is in the future relative to generated_at", wantFutureLimitation: true},
		{name: "future event", eventTime: generatedAt.Add(time.Second), sourceObservedAt: past, wantLevel: "UNKNOWN", wantFreshness: int64Pointer(10), wantBasis: "event timestamp is in the future relative to generated_at", wantFutureLimitation: true},
		{name: "equal", eventTime: generatedAt, sourceObservedAt: generatedAt, wantLevel: "HIGH", wantFreshness: int64Pointer(0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := timeline.Event{ID: "event", Time: tt.eventTime, Source: timeline.Source{Kind: "docker", ObservedAt: tt.sourceObservedAt}, Confidence: timeline.Confidence{Level: "HIGH", Basis: "validated source"}, Limitations: []string{"existing", "existing"}}
			output := timelineOutputFrom(event, generatedAt)
			if output.Confidence.Level != tt.wantLevel || output.Confidence.Level == "LOW" || output.Confidence.Level == "EXACT" || output.CausalityClaimed {
				t.Fatalf("confidence=%#v causality=%t", output.Confidence, output.CausalityClaimed)
			}
			if output.Confidence.Basis != tt.wantBasis && tt.wantBasis != "" {
				t.Fatalf("basis=%q want=%q", output.Confidence.Basis, tt.wantBasis)
			}
			if !equalInt64Pointers(output.Source.FreshnessSeconds, tt.wantFreshness) {
				t.Fatalf("freshness=%v want=%v", output.Source.FreshnessSeconds, tt.wantFreshness)
			}
			futureLimitations := 0
			for _, limitation := range output.Limitations {
				if strings.Contains(limitation, "future relative to generated_at") {
					futureLimitations++
				}
			}
			if (futureLimitations == 1) != tt.wantFutureLimitation {
				t.Fatalf("limitations=%#v future count=%d", output.Limitations, futureLimitations)
			}
			if countTimelineLimitations(output.Limitations, "existing") != 1 {
				t.Fatalf("duplicate limitations=%#v", output.Limitations)
			}
		})
	}
}

func TestTimelineCLIUsesOneGeneratedAtForEnvelopeAndItems(t *testing.T) {
	project := t.TempDir()
	app, stdout, stderr := testApp(project)
	fixed := time.Date(2026, 8, 26, 12, 30, 0, 0, time.UTC)
	app.Now = func() time.Time { return fixed }
	fixture, err := filepath.Abs(filepath.Join("..", "..", "testdata", "deployment", "timeline-rollback-concurrent.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if code := app.Run(t.Context(), []string{"init", "--project-dir", project, "--json"}); code != 0 {
		t.Fatal(code)
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(t.Context(), []string{"deployments", "ingest", "--file", fixture, "--project-dir", project, "--json"}); code != 0 {
		t.Fatalf("ingest=%d stderr=%q", code, stderr.String())
	}
	var calls int
	generatedAt := fixed.Add(time.Minute)
	app.Now = func() time.Time {
		calls++
		return generatedAt
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run(t.Context(), []string{"timeline", "--environment", "reference", "--since", "2026-08-26T11:00:00Z", "--until", "2026-08-26T13:00:00Z", "--project-dir", project, "--json"}); code != 0 {
		t.Fatalf("timeline=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if calls != 1 || stderr.Len() != 0 {
		t.Fatalf("Now calls=%d stderr=%q", calls, stderr.String())
	}
	var envelope struct {
		GeneratedAt string `json:"generated_at"`
		Data        struct {
			Items []struct {
				Source struct {
					ObservedAt       string `json:"observed_at"`
					FreshnessSeconds *int64 `json:"freshness_seconds"`
				} `json:"source"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.NewDecoder(bytes.NewReader(stdout.Bytes())).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.GeneratedAt != generatedAt.Format(time.RFC3339Nano) {
		t.Fatalf("generated_at=%q", envelope.GeneratedAt)
	}
	for _, item := range envelope.Data.Items {
		observedAt, err := time.Parse(time.RFC3339Nano, item.Source.ObservedAt)
		if err != nil || item.Source.FreshnessSeconds == nil {
			t.Fatalf("source=%#v err=%v", item.Source, err)
		}
		want := int64(generatedAt.Sub(observedAt).Seconds())
		if *item.Source.FreshnessSeconds != want {
			t.Fatalf("freshness=%d want=%d", *item.Source.FreshnessSeconds, want)
		}
	}
}

func int64Pointer(value int64) *int64 { return &value }

func equalInt64Pointers(left, right *int64) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func countTimelineLimitations(limitations []string, wanted string) int {
	count := 0
	for _, limitation := range limitations {
		if limitation == wanted {
			count++
		}
	}
	return count
}
