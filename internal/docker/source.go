// Package docker provides read-only runtime discovery through the Docker CLI.
package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/inventory"
)

const (
	defaultConcurrency    = 4
	defaultInspectTimeout = 30 * time.Second
	maxStdoutBytes        = 4 << 20
	maxStderrBytes        = 64 << 10
)

var allowedOCILabels = [...]string{
	"org.opencontainers.image.revision",
	"org.opencontainers.image.source",
	"org.opencontainers.image.created",
}

// These templates deliberately project only the fields Prodmap needs. In
// particular, Docker never returns container environment variables, mounts,
// health-check logs, or the complete image label map to this process.
const containerInspectTemplate = `{"id":{{json .Id}},"name":{{json .Name}},"image_reference":{{json .Config.Image}},"image_id":{{json .Image}},"state":{{json .State.Status}},"health":{{with (index .State "Health")}}{{json (index . "Status")}}{{else}}""{{end}},"restart_count":{{json .RestartCount}},"started_at":{{json .State.StartedAt}}}`

const imageInspectTemplate = `{"id":{{json .Id}},"repo_digests":{{json .RepoDigests}},"repo_tags":{{json .RepoTags}},"labels":{"org.opencontainers.image.revision":{{json (index .Config.Labels "org.opencontainers.image.revision")}},"org.opencontainers.image.source":{{json (index .Config.Labels "org.opencontainers.image.source")}},"org.opencontainers.image.created":{{json (index .Config.Labels "org.opencontainers.image.created")}}}}`

type commandRunner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// Source inspects the local Docker runtime without changing it.
type Source struct {
	runner      commandRunner
	now         func() time.Time
	concurrency int
	timeout     time.Duration
}

// NewSource creates a runtime source backed by the docker executable on PATH.
func NewSource() *Source {
	return &Source{
		runner: execRunner{
			binary:      "docker",
			stdoutLimit: maxStdoutBytes,
			stderrLimit: maxStderrBytes,
		},
		now:         time.Now,
		concurrency: defaultConcurrency,
		timeout:     defaultInspectTimeout,
	}
}

var _ inventory.RuntimeSource = (*Source)(nil)

// InspectRuntime lists every existing container and inspects its container and
// image metadata. All Docker operations used here are read-only.
func (s *Source) InspectRuntime(ctx context.Context) (inventory.RuntimeBatch, error) {
	if err := ctx.Err(); err != nil {
		return inventory.RuntimeBatch{}, err
	}
	timeout := s.timeout
	if timeout <= 0 {
		timeout = defaultInspectTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	runner := s.runner
	if runner == nil {
		runner = NewSource().runner
	}

	stdout, err := runner.Run(ctx, "ps", "-a", "--no-trunc", "--format", "{{.ID}}")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return inventory.RuntimeBatch{}, ctxErr
		}
		return inventory.RuntimeBatch{}, fmt.Errorf("%w: list Docker containers: %w", errs.ErrUnavailable, err)
	}

	containerIDs, err := parseContainerIDs(stdout)
	if err != nil {
		return inventory.RuntimeBatch{}, err
	}
	now := s.now
	if now == nil {
		now = time.Now
	}
	observedAt := now().UTC()
	if len(containerIDs) == 0 {
		return inventory.RuntimeBatch{ObservedAt: observedAt}, nil
	}
	concurrency := s.concurrency
	if concurrency <= 0 || concurrency > defaultConcurrency {
		concurrency = defaultConcurrency
	}
	if concurrency > len(containerIDs) {
		concurrency = len(containerIDs)
	}

	type job struct {
		index int
		id    string
	}
	type result struct {
		observation inventory.RuntimeObservation
		disappeared string
		err         error
	}

	jobs := make(chan job, len(containerIDs))
	results := make([]result, len(containerIDs))
	for index, id := range containerIDs {
		jobs <- job{index: index, id: id}
	}
	close(jobs)

	var workers sync.WaitGroup
	workers.Add(concurrency)
	for range concurrency {
		go func() {
			defer workers.Done()
			for item := range jobs {
				observation, disappeared, inspectErr := inspectOne(ctx, runner, item.id, observedAt)
				results[item.index] = result{
					observation: observation,
					disappeared: disappeared,
					err:         inspectErr,
				}
			}
		}()
	}
	workers.Wait()

	if err := ctx.Err(); err != nil {
		return inventory.RuntimeBatch{}, err
	}

	batch := inventory.RuntimeBatch{ObservedAt: observedAt, Observations: make([]inventory.RuntimeObservation, 0, len(results))}
	var firstInspectionFailure error
	for _, result := range results {
		if result.disappeared != "" {
			batch.Rejected++
			batch.Warnings = append(batch.Warnings, result.disappeared+" disappeared during read-only inspection; observation rejected")
			continue
		}
		if result.err != nil {
			if errors.Is(result.err, context.Canceled) || errors.Is(result.err, context.DeadlineExceeded) {
				return inventory.RuntimeBatch{}, result.err
			}
			if errors.Is(result.err, errs.ErrUnavailable) {
				return inventory.RuntimeBatch{}, result.err
			}
			if firstInspectionFailure == nil {
				firstInspectionFailure = result.err
			}
			batch.Rejected++
			batch.Warnings = append(batch.Warnings, inspectionWarning(result.err))
			continue
		}
		batch.Observations = append(batch.Observations, result.observation)
	}
	if len(batch.Observations) == 0 && batch.Rejected > 0 {
		if firstInspectionFailure != nil {
			return inventory.RuntimeBatch{}, fmt.Errorf("inspect Docker runtime: all %d listed containers failed inspection: %w", batch.Rejected, firstInspectionFailure)
		}
		return inventory.RuntimeBatch{}, fmt.Errorf("inspect Docker runtime: all %d listed containers disappeared during inspection", batch.Rejected)
	}
	return batch, nil
}

func inspectOne(ctx context.Context, runner commandRunner, containerID string, observedAt time.Time) (inventory.RuntimeObservation, string, error) {
	stdout, err := runner.Run(ctx, "container", "inspect", "--format", containerInspectTemplate, containerID)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return inventory.RuntimeObservation{}, "", ctxErr
		}
		if isObjectNotFound(err) {
			return inventory.RuntimeObservation{}, "container", nil
		}
		if isUnavailable(err) {
			return inventory.RuntimeObservation{}, "", fmt.Errorf("%w: inspect Docker container: %w", errs.ErrUnavailable, err)
		}
		return inventory.RuntimeObservation{}, "", &itemInspectionError{stage: "container", cause: err}
	}

	var container containerView
	if err := decodeProjection(stdout, &container); err != nil {
		return inventory.RuntimeObservation{}, "", &itemInspectionError{stage: "container", cause: err}
	}
	if container.ID == "" || container.ImageID == "" {
		return inventory.RuntimeObservation{}, "", &itemInspectionError{stage: "container", cause: errors.New("required immutable identity is missing")}
	}
	if !strings.EqualFold(container.ID, containerID) {
		return inventory.RuntimeObservation{}, "", &itemInspectionError{stage: "container", cause: errors.New("unexpected immutable identity")}
	}

	stdout, err = runner.Run(ctx, "image", "inspect", "--format", imageInspectTemplate, container.ImageID)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return inventory.RuntimeObservation{}, "", ctxErr
		}
		if isObjectNotFound(err) {
			return inventory.RuntimeObservation{}, "image", nil
		}
		if isUnavailable(err) {
			return inventory.RuntimeObservation{}, "", fmt.Errorf("%w: inspect Docker image: %w", errs.ErrUnavailable, err)
		}
		return inventory.RuntimeObservation{}, "", &itemInspectionError{stage: "image", cause: err}
	}

	var image imageView
	if err := decodeProjection(stdout, &image); err != nil {
		return inventory.RuntimeObservation{}, "", &itemInspectionError{stage: "image", cause: err}
	}
	if image.ID == "" {
		return inventory.RuntimeObservation{}, "", &itemInspectionError{stage: "image", cause: errors.New("immutable image identity is missing")}
	}
	if image.ID != container.ImageID {
		return inventory.RuntimeObservation{}, "", &itemInspectionError{stage: "image", cause: errors.New("identity differs from container reference")}
	}

	startedAt, err := parseStartedAt(container.StartedAt)
	if err != nil {
		return inventory.RuntimeObservation{}, "", &itemInspectionError{stage: "container", cause: errors.New("invalid start time")}
	}
	health := strings.TrimSpace(container.Health)
	if health == "" {
		health = "none"
	}

	return inventory.RuntimeObservation{
		ExternalID:     container.ID,
		ContainerName:  strings.TrimPrefix(container.Name, "/"),
		ImageReference: container.ImageReference,
		ImageID:        image.ID,
		RepoDigests:    uniqueSorted(image.RepoDigests),
		RepoTags:       uniqueSorted(image.RepoTags),
		OCILabels:      filterOCILabels(image.Labels),
		State:          container.State,
		Health:         health,
		RestartCount:   container.RestartCount,
		StartedAt:      startedAt,
		ObservedAt:     observedAt,
	}, "", nil
}

type itemInspectionError struct {
	stage string
	cause error
}

func (e *itemInspectionError) Error() string {
	return "Docker " + e.stage + " inspection failed"
}

func (e *itemInspectionError) Unwrap() error { return e.cause }

func inspectionWarning(err error) string {
	var itemErr *itemInspectionError
	if errors.As(err, &itemErr) && itemErr.stage == "image" {
		return "image inspection failed; observation rejected"
	}
	return "container inspection failed; observation rejected"
}

type containerView struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	ImageReference string `json:"image_reference"`
	ImageID        string `json:"image_id"`
	State          string `json:"state"`
	Health         string `json:"health"`
	RestartCount   int64  `json:"restart_count"`
	StartedAt      string `json:"started_at"`
}

type imageView struct {
	ID          string             `json:"id"`
	RepoDigests []string           `json:"repo_digests"`
	RepoTags    []string           `json:"repo_tags"`
	Labels      map[string]*string `json:"labels"`
}

func decodeProjection(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func parseContainerIDs(data []byte) ([]string, error) {
	set := make(map[string]struct{})
	for _, line := range strings.Split(string(data), "\n") {
		id := strings.TrimSpace(line)
		if id == "" {
			continue
		}
		if !validContainerID(id) {
			return nil, errors.New("decode Docker container list: invalid container identifier")
		}
		set[id] = struct{}{}
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func validContainerID(id string) bool {
	if len(id) < 12 || len(id) > 64 {
		return false
	}
	for _, character := range id {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return true
}

func parseStartedAt(value string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, err
	}
	parsed = parsed.UTC()
	if parsed.IsZero() || parsed.Year() <= 1 {
		return nil, nil
	}
	return &parsed, nil
}

func filterOCILabels(labels map[string]*string) map[string]string {
	filtered := make(map[string]string)
	for _, key := range allowedOCILabels {
		if value, ok := labels[key]; ok && value != nil {
			filtered[key] = *value
		}
	}
	return filtered
}

func uniqueSorted(values []string) []string {
	set := make(map[string]struct{})
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			set[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

type execRunner struct {
	binary      string
	stdoutLimit int
	stderrLimit int
}

func (r execRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	stdout := &limitedBuffer{limit: r.stdoutLimit}
	stderr := &limitedBuffer{limit: r.stderrLimit}
	command := exec.CommandContext(ctx, r.binary, args...)
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if stdout.overflow {
		if err != nil {
			return nil, classifyCommandError(errors.Join(err, errOutputLimit), stderr.String())
		}
		return nil, &commandError{cause: errOutputLimit}
	}
	if err != nil {
		return nil, classifyCommandError(err, stderr.String())
	}
	return append([]byte(nil), stdout.Bytes()...), nil
}

var errOutputLimit = errors.New("Docker command output exceeded the configured limit")

type limitedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	written := len(data)
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if remaining > len(data) {
			remaining = len(data)
		}
		_, _ = b.buffer.Write(data[:remaining])
	}
	if len(data) > remaining {
		b.overflow = true
	}
	return written, nil
}

func (b *limitedBuffer) Bytes() []byte  { return b.buffer.Bytes() }
func (b *limitedBuffer) String() string { return b.buffer.String() }

type commandError struct {
	cause       error
	notFound    bool
	unavailable bool
}

func (e *commandError) Error() string {
	switch {
	case e.notFound:
		return "Docker object not found: " + e.cause.Error()
	case e.unavailable:
		return "Docker daemon unavailable: " + e.cause.Error()
	default:
		return "Docker command failed: " + e.cause.Error()
	}
}

func (e *commandError) Unwrap() error { return e.cause }

func classifyCommandError(cause error, stderr string) error {
	message := strings.ToLower(stderr)
	return &commandError{
		cause:    cause,
		notFound: containsAny(message, "no such container", "no such image", "no such object"),
		unavailable: isExecutableMissing(cause) || containsAny(message,
			"cannot connect to the docker daemon",
			"is the docker daemon running",
			"error during connect",
			"docker daemon is not running",
			"connection refused",
			"permission denied while trying to connect",
		),
	}
}

func isExecutableMissing(err error) bool {
	var execError *exec.Error
	return errors.As(err, &execError)
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}

func isObjectNotFound(err error) bool {
	var commandErr *commandError
	return errors.As(err, &commandErr) && commandErr.notFound
}

func isUnavailable(err error) bool {
	var commandErr *commandError
	return errors.As(err, &commandErr) && commandErr.unavailable
}
