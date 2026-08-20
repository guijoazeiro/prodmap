package docker

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/errs"
)

func TestRealDockerInspection(t *testing.T) {
	if os.Getenv("PRODMAP_TEST_REAL_DOCKER") != "1" {
		t.Skip("set PRODMAP_TEST_REAL_DOCKER=1 to run the read-only Docker integration test")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker executable is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	batch, err := NewSource().InspectRuntime(ctx)
	if errors.Is(err, errs.ErrUnavailable) {
		t.Skipf("Docker daemon is unavailable: %v", err)
	}
	if err != nil {
		t.Fatalf("read-only Docker inspection failed: %v", err)
	}
	if batch.ObservedAt.IsZero() {
		t.Fatal("Docker inspection returned no freshness timestamp")
	}
	for _, observation := range batch.Observations {
		if observation.ExternalID == "" || observation.ImageID == "" || observation.ObservedAt.IsZero() {
			t.Fatalf("Docker observation lacks immutable identity or freshness: %+v", observation)
		}
		switch observation.Health {
		case "none", "starting", "healthy", "unhealthy":
		default:
			t.Fatalf("Docker observation has unsupported health status %q: %+v", observation.Health, observation)
		}
	}
}
