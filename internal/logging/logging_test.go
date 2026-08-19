package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/guijoazeiro/prodmap/internal/errs"
)

func TestNewTextHonorsLevel(t *testing.T) {
	var out bytes.Buffer
	logger, err := New(&out, Options{Level: "warn", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("hidden")
	logger.Warn("visible", "component", "test")
	if strings.Contains(out.String(), "hidden") || !strings.Contains(out.String(), "level=WARN") || !strings.Contains(out.String(), "msg=visible") {
		t.Fatalf("unexpected text log: %q", out.String())
	}
}

func TestNewJSON(t *testing.T) {
	var out bytes.Buffer
	logger, err := New(&out, Options{Level: "debug", Format: "json"})
	if err != nil {
		t.Fatal(err)
	}
	logger.Debug("ready", "count", 2)
	var record map[string]any
	if err := json.Unmarshal(out.Bytes(), &record); err != nil {
		t.Fatalf("invalid JSON %q: %v", out.String(), err)
	}
	if record["level"] != "DEBUG" || record["msg"] != "ready" || record["count"] != float64(2) {
		t.Fatalf("unexpected record: %#v", record)
	}
}

func TestNewSupportsEveryLevel(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error"} {
		t.Run(level, func(t *testing.T) {
			logger, err := New(&bytes.Buffer{}, Options{Level: level, Format: "text"})
			if err != nil {
				t.Fatal(err)
			}
			if !logger.Enabled(context.Background(), map[string]slog.Level{"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError}[level]) {
				t.Fatal("configured level disabled")
			}
		})
	}
}

func TestNewRejectsInvalidOptions(t *testing.T) {
	for _, opts := range []Options{{Level: "trace", Format: "text"}, {Level: "info", Format: "xml"}} {
		if _, err := New(&bytes.Buffer{}, opts); !errors.Is(err, errs.ErrInvalid) {
			t.Fatalf("got %v", err)
		}
	}
	if _, err := New(nil, Options{Level: "info", Format: "text"}); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("nil writer: %v", err)
	}
}
