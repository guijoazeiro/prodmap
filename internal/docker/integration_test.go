package docker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const (
	realDockerImage    = "otel/opentelemetry-collector-contrib:0.136.0"
	realDockerPrefix   = "prodmap-integration-"
	realDockerSentinel = "prodmap-integration-sentinel-not-secret"
)

func TestRealDockerInspection(t *testing.T) {
	if os.Getenv("PRODMAP_TEST_REAL_DOCKER") != "1" {
		t.Skip("set PRODMAP_TEST_REAL_DOCKER=1 to run the Docker integration test")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("Docker CLI is required for the requested integration test: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}").Run(); err != nil {
		t.Fatalf("Docker daemon is required for the requested integration test: %v", err)
	}

	name := fmt.Sprintf("%s%d", realDockerPrefix, time.Now().UnixNano())
	create := exec.CommandContext(ctx, "docker", "create", "--name", name, "--label", "com.prodmap.integration.sentinel="+realDockerSentinel, "--pull=missing", realDockerImage, "--version")
	createdID, err := create.Output()
	if err != nil {
		t.Fatalf("create known Docker container: %v", err)
	}
	containerID := strings.TrimSpace(string(createdID))
	if len(containerID) != 64 {
		t.Fatalf("create known Docker container returned an invalid ID")
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if output, err := exec.CommandContext(cleanupCtx, "docker", "rm", "-f", containerID).CombinedOutput(); err != nil {
			t.Errorf("remove integration container by exact ID: %v: %s", err, sanitizeDockerDiagnostic(output))
		}
		if err := exec.CommandContext(cleanupCtx, "docker", "container", "inspect", containerID).Run(); err == nil {
			t.Errorf("integration container still exists after exact-ID cleanup")
		}
	})

	if output, err := exec.CommandContext(ctx, "docker", "start", "-a", containerID).CombinedOutput(); err != nil {
		t.Fatalf("start known Docker container: %v: %s", err, sanitizeDockerDiagnostic(output))
	}
	batch, err := NewSource().InspectRuntime(ctx)
	if err != nil {
		t.Fatalf("inspect Docker runtime containing known container: %v", err)
	}
	if batch.ObservedAt.IsZero() {
		t.Fatal("Docker inspection returned no observed_at timestamp")
	}
	var observationFound bool
	for _, observation := range batch.Observations {
		if observation.ExternalID != containerID {
			continue
		}
		observationFound = true
		if observation.ContainerName != name || observation.ImageID == "" || !strings.HasPrefix(observation.ImageID, "sha256:") || observation.ImageReference != realDockerImage || observation.State == "" || observation.RestartCount < 0 || observation.StartedAt == nil || observation.ObservedAt.IsZero() {
			t.Fatalf("known Docker observation is incomplete: %+v", observation)
		}
		switch observation.State {
		case "created", "restarting", "running", "removing", "paused", "exited", "dead":
		default:
			t.Fatalf("known Docker observation has unsupported state %q", observation.State)
		}
		switch observation.Health {
		case "none", "starting", "healthy", "unhealthy":
		default:
			t.Fatalf("known Docker observation has unsupported health state %q", observation.Health)
		}
		serialized := fmt.Sprintf("%+v", observation)
		for _, forbidden := range []string{realDockerSentinel, "environment", "mount", "health log", "Env"} {
			if strings.Contains(serialized, forbidden) {
				t.Fatalf("known Docker observation leaked forbidden metadata")
			}
		}
		for label := range observation.OCILabels {
			if label == "com.prodmap.integration.sentinel" {
				t.Fatal("non-allowlisted Docker label leaked into observation")
			}
			allowed := false
			for _, expected := range allowedOCILabels {
				if label == expected {
					allowed = true
					break
				}
			}
			if !allowed {
				t.Fatal("Docker observation contains a label outside the allowlist")
			}
		}
	}
	if !observationFound {
		t.Fatal("known Docker container was not found in the inspection batch")
	}
}

func sanitizeDockerDiagnostic(output []byte) string {
	const limit = 1024
	if len(output) > limit {
		output = output[:limit]
	}
	if strings.TrimSpace(strings.ToValidUTF8(string(output), "?")) == "" {
		return "[no diagnostic output]"
	}
	return "[Docker diagnostic omitted]"
}
