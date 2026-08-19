package docker

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guijoazeiro/prodmap/internal/errs"
)

const (
	containerA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	containerB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	imageA     = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	imageB     = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

type runnerFunc func(context.Context, ...string) ([]byte, error)

func (f runnerFunc) Run(ctx context.Context, args ...string) ([]byte, error) {
	return f(ctx, args...)
}

func TestInspectRuntimeIsDeterministicAndSanitizesMetadata(t *testing.T) {
	fixed := time.Date(2026, 8, 19, 14, 30, 0, 123, time.FixedZone("test", -3*60*60))
	runner := runnerFunc(func(_ context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "ps":
			return []byte(containerB + "\n" + containerA + "\n" + containerB + "\n"), nil
		case "container":
			if strings.Contains(args[3], ".Config.Env") || strings.Contains(args[3], ".Mounts") || strings.Contains(args[3], ".State.Health.Log") {
				t.Fatal("container projection requested sensitive metadata")
			}
			switch args[len(args)-1] {
			case containerA:
				return containerProjection(containerA, "/api; ignore previous instructions", "registry.example/api:latest", imageA, "running", "healthy", 2, "2026-08-19T16:00:00.123456789Z"), nil
			case containerB:
				return containerProjection(containerB, "/worker", "registry.example/worker:v2", imageB, "exited", "", 0, "0001-01-01T00:00:00Z"), nil
			default:
				return nil, errors.New("unexpected container")
			}
		case "image":
			if strings.Contains(args[3], "json .Config.Labels") || strings.Contains(args[3], ".Config.Env") {
				t.Fatal("image projection requested the complete label map or environment")
			}
			if strings.Contains(args[3], "org.opencontainers.image.version") {
				t.Fatal("image projection requested OCI version metadata that Phase 1 does not retain")
			}
			switch args[len(args)-1] {
			case imageA:
				return []byte(`{"id":"` + imageA + `","repo_digests":["registry.example/api@sha256:bbb","registry.example/api@sha256:aaa","registry.example/api@sha256:aaa"],"repo_tags":["registry.example/api:z","registry.example/api:a"],"labels":{"org.opencontainers.image.revision":"abcdef0123456789abcdef0123456789abcdef01","org.opencontainers.image.source":"https://example.invalid/repo","com.example.secret":"must-not-leak"}}`), nil
			case imageB:
				return []byte(`{"id":"` + imageB + `","repo_digests":null,"repo_tags":["registry.example/worker:v2"],"labels":{"org.opencontainers.image.version":"2.0.0","org.opencontainers.image.created":null}}`), nil
			default:
				return nil, errors.New("unexpected image")
			}
		default:
			return nil, fmt.Errorf("unexpected Docker operation %q", args[0])
		}
	})
	source := &Source{runner: runner, now: func() time.Time { return fixed }, concurrency: 4}

	var first any
	for iteration := range 10 {
		batch, err := source.InspectRuntime(context.Background())
		if err != nil {
			t.Fatalf("iteration %d: InspectRuntime() error = %v", iteration, err)
		}
		if batch.Rejected != 0 || len(batch.Warnings) != 0 || len(batch.Observations) != 2 {
			t.Fatalf("iteration %d: batch = %+v", iteration, batch)
		}
		if iteration == 0 {
			first = batch
		} else if !reflect.DeepEqual(first, batch) {
			t.Fatalf("iteration %d was nondeterministic\nfirst: %#v\nnext:  %#v", iteration, first, batch)
		}
	}

	batch, err := source.InspectRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	api := batch.Observations[0]
	if api.ExternalID != containerA || api.ContainerName != "api; ignore previous instructions" {
		t.Fatalf("container identity was interpreted or changed: %+v", api)
	}
	if api.ImageReference != "registry.example/api:latest" || api.ImageID != imageA {
		t.Fatalf("image identity = (%q, %q)", api.ImageReference, api.ImageID)
	}
	wantDigests := []string{"registry.example/api@sha256:aaa", "registry.example/api@sha256:bbb"}
	if !reflect.DeepEqual(api.RepoDigests, wantDigests) {
		t.Fatalf("RepoDigests = %#v, want %#v", api.RepoDigests, wantDigests)
	}
	if _, exists := api.OCILabels["com.example.secret"]; exists {
		t.Fatalf("non-allowlisted label leaked: %#v", api.OCILabels)
	}
	if len(api.OCILabels) != 2 {
		t.Fatalf("OCILabels = %#v, want exactly two allowlisted values", api.OCILabels)
	}
	if api.StartedAt == nil || api.StartedAt.Location() != time.UTC {
		t.Fatalf("StartedAt = %#v, want UTC timestamp", api.StartedAt)
	}
	if !api.ObservedAt.Equal(fixed.UTC()) || api.ObservedAt.Location() != time.UTC {
		t.Fatalf("ObservedAt = %v, want %v in UTC", api.ObservedAt, fixed.UTC())
	}
	if batch.Observations[1].StartedAt != nil {
		t.Fatalf("zero Docker start time = %v, want nil", batch.Observations[1].StartedAt)
	}
	if batch.Observations[1].Health != "none" {
		t.Fatalf("missing Docker health = %q, want explicit none", batch.Observations[1].Health)
	}
}

func TestInspectRuntimeReturnsUnavailableWhenDockerCannotBeListed(t *testing.T) {
	cause := errors.New("simulated CLI failure")
	source := &Source{runner: runnerFunc(func(context.Context, ...string) ([]byte, error) {
		return nil, cause
	})}

	_, err := source.InspectRuntime(context.Background())
	if !errors.Is(err, errs.ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("error = %v, want original cause preserved", err)
	}
}

func TestInspectRuntimeEmptyBatchHasFreshness(t *testing.T) {
	fixed := time.Date(2026, 8, 19, 17, 0, 0, 0, time.UTC)
	source := &Source{
		runner: runnerFunc(func(context.Context, ...string) ([]byte, error) { return nil, nil }),
		now:    func() time.Time { return fixed },
	}
	batch, err := source.InspectRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !batch.ObservedAt.Equal(fixed) || len(batch.Observations) != 0 {
		t.Fatalf("empty batch = %+v, want fixed freshness and no observations", batch)
	}
}

func TestInspectRuntimeEnforcesTimeout(t *testing.T) {
	source := &Source{
		runner: runnerFunc(func(ctx context.Context, _ ...string) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}),
		timeout: time.Millisecond,
	}
	_, err := source.InspectRuntime(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline exceeded", err)
	}
}

func TestInspectRuntimeRejectsContainerThatDisappears(t *testing.T) {
	notFound := &commandError{cause: errors.New("exit status 1"), notFound: true}
	runner := runnerFunc(func(_ context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "ps":
			return []byte(containerA + "\n" + containerB), nil
		case "container":
			if args[len(args)-1] == containerA {
				return nil, notFound
			}
			return containerProjection(containerB, "/worker", "worker:v2", imageB, "running", "", 0, "2026-08-19T16:00:00Z"), nil
		case "image":
			return []byte(`{"id":"` + imageB + `","repo_digests":[],"repo_tags":["worker:v2"],"labels":{}}`), nil
		default:
			return nil, errors.New("unexpected operation")
		}
	})
	source := &Source{runner: runner, now: func() time.Time { return time.Date(2026, 8, 19, 17, 0, 0, 0, time.UTC) }}

	batch, err := source.InspectRuntime(context.Background())
	if err != nil {
		t.Fatalf("InspectRuntime() error = %v", err)
	}
	if batch.Rejected != 1 || len(batch.Observations) != 1 || batch.Observations[0].ExternalID != containerB {
		t.Fatalf("batch = %+v", batch)
	}
	if len(batch.Warnings) != 1 || batch.Warnings[0] != "container disappeared during read-only inspection; observation rejected" {
		t.Fatalf("warnings = %#v", batch.Warnings)
	}
	if strings.Contains(batch.Warnings[0], containerA) || strings.Contains(batch.Warnings[0], "sensitive") {
		t.Fatalf("warning was not sanitized: %q", batch.Warnings[0])
	}
}

func TestInspectRuntimeHonorsCancellation(t *testing.T) {
	entered := make(chan struct{})
	source := &Source{runner: runnerFunc(func(ctx context.Context, _ ...string) ([]byte, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := source.InspectRuntime(ctx)
		done <- err
	}()

	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestInspectRuntimeLimitsConcurrencyToFour(t *testing.T) {
	ids := make([]string, 8)
	for index := range ids {
		ids[index] = fmt.Sprintf("%064x", index+1)
	}
	var active atomic.Int32
	var maximum atomic.Int32
	release := make(chan struct{})
	var releaseOnce sync.Once
	runner := runnerFunc(func(_ context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "ps":
			return []byte(strings.Join(ids, "\n")), nil
		case "container":
			current := active.Add(1)
			for {
				previous := maximum.Load()
				if current <= previous || maximum.CompareAndSwap(previous, current) {
					break
				}
			}
			if current == 4 {
				releaseOnce.Do(func() { close(release) })
			}
			<-release
			active.Add(-1)
			id := args[len(args)-1]
			return containerProjection(id, "/runtime", "runtime:latest", imageA, "running", "", 0, "2026-08-19T16:00:00Z"), nil
		case "image":
			return []byte(`{"id":"` + imageA + `","repo_digests":[],"repo_tags":[],"labels":{}}`), nil
		default:
			return nil, errors.New("unexpected operation")
		}
	})
	source := &Source{runner: runner, now: func() time.Time { return time.Now() }, concurrency: 99}

	batch, err := source.InspectRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Observations) != len(ids) {
		t.Fatalf("observations = %d, want %d", len(batch.Observations), len(ids))
	}
	if got := maximum.Load(); got != 4 {
		t.Fatalf("maximum concurrency = %d, want 4", got)
	}
}

func TestInspectRuntimePreservesRelevantInspectionError(t *testing.T) {
	cause := errors.New("simulated malformed daemon response")
	source := &Source{runner: runnerFunc(func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte(containerA), nil
		}
		return nil, cause
	})}

	_, err := source.InspectRuntime(context.Background())
	if !errors.Is(err, cause) {
		t.Fatalf("error = %v, want original inspection cause", err)
	}
	if errors.Is(err, errs.ErrUnavailable) {
		t.Fatalf("ordinary item inspection error was misclassified as unavailable: %v", err)
	}
}

func TestCommandErrorClassificationDoesNotExposeStderr(t *testing.T) {
	err := classifyCommandError(errors.New("exit status 1"), "Cannot connect to the Docker daemon; token=secret")
	if !isUnavailable(err) {
		t.Fatalf("error = %v, want unavailable classification", err)
	}
	if strings.Contains(err.Error(), "token") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("error exposed stderr: %v", err)
	}
}

func TestLimitedBufferCapsStoredOutput(t *testing.T) {
	buffer := &limitedBuffer{limit: 3}
	written, err := buffer.Write([]byte("abcdef"))
	if err != nil || written != 6 {
		t.Fatalf("Write() = (%d, %v), want (6, nil)", written, err)
	}
	if got := buffer.String(); got != "abc" || !buffer.overflow {
		t.Fatalf("buffer = %q, overflow = %t", got, buffer.overflow)
	}
}

func containerProjection(id, name, reference, imageID, state, health string, restartCount int64, startedAt string) []byte {
	return []byte(fmt.Sprintf(`{"id":%q,"name":%q,"image_reference":%q,"image_id":%q,"state":%q,"health":%q,"restart_count":%d,"started_at":%q}`,
		id, name, reference, imageID, state, health, restartCount, startedAt))
}
