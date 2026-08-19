// Package git reads commit provenance from a local Git repository.
package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/inventory"
)

const (
	defaultOutputLimit    = 64 * 1024
	defaultCommandTimeout = 5 * time.Second
	maxDiagnosticRunes    = 512
	maxRepositoryRunes    = 255
	maxSubjectRunes       = 1024
)

var (
	fullSHA        = regexp.MustCompile(`^[0-9a-fA-F]{40}([0-9a-fA-F]{24})?$`)
	scpLikeRemote  = regexp.MustCompile(`^(?:[^@/:[:space:]]+@)?([^/:[:space:]]+):(.+)$`)
	credentialPair = regexp.MustCompile(`(?i)\b(token|password|passwd|secret|authorization|credential)=\S+`)
	urlUserInfo    = regexp.MustCompile(`://[^/@[:space:]]+@`)
	errOutputLimit = errors.New("command output limit exceeded")
)

// Source implements inventory.CommitSource using only local, read-only Git
// commands. It never fetches, checks out, or changes repository state.
type Source struct {
	projectDir string
	runner     commandRunner
	timeout    time.Duration
}

// Option configures a Source. Options are intentionally package-scoped today;
// the only non-default option exists to isolate command execution in tests.
type Option func(*Source)

// New creates a local Git commit source rooted at projectDir.
func New(projectDir string, options ...Option) *Source {
	source := &Source{
		projectDir: projectDir,
		runner:     execRunner{outputLimit: defaultOutputLimit},
		timeout:    defaultCommandTimeout,
	}
	for _, option := range options {
		if option != nil {
			option(source)
		}
	}
	return source
}

// withRunner is kept unexported because command execution is an adapter detail,
// not part of the inventory contract. Package tests can inject deterministic
// failures without exposing a process runner to consumers.
func withRunner(runner commandRunner) Option {
	return func(source *Source) {
		if runner != nil {
			source.runner = runner
		}
	}
}

// Repository returns a path-safe identity for the repository containing the
// configured project directory. The absolute root is hashed and never returned.
func (s *Source) Repository(ctx context.Context) (inventory.Repository, error) {
	if err := s.validate(); err != nil {
		return inventory.Repository{}, err
	}

	result, err := s.run(ctx, operationRepository, "-C", s.projectDir, "rev-parse", "--show-toplevel")
	if err != nil {
		return inventory.Repository{}, err
	}
	root, err := parseRepositoryRoot(result.stdout)
	if err != nil {
		return inventory.Repository{}, fmt.Errorf("read Git repository root: %w", err)
	}
	rootHash := hashString(root)

	canonicalURL, err := s.originURL(ctx)
	if err != nil {
		return inventory.Repository{}, err
	}

	externalID := "local:" + rootHash
	name := "repository-" + rootHash[:12]
	if canonicalURL != "" {
		externalID = "origin:" + hashString(canonicalURL)
		if remoteName := repositoryName(canonicalURL); remoteName != "" {
			name = remoteName
		}
	}

	return inventory.Repository{
		ExternalID:   externalID,
		Name:         name,
		CanonicalURL: canonicalURL,
		RootPathHash: rootHash,
	}, nil
}

// ResolveCommit verifies a complete object ID and returns the corresponding
// commit metadata. Abbreviated revisions, ref names, and revision expressions
// are rejected before Git is invoked.
func (s *Source) ResolveCommit(ctx context.Context, revision string) (inventory.Commit, error) {
	if err := s.validate(); err != nil {
		return inventory.Commit{}, err
	}
	if !fullSHA.MatchString(revision) {
		return inventory.Commit{}, fmt.Errorf("Git revision must be a complete 40- or 64-character hexadecimal SHA: %w", errs.ErrInvalid)
	}

	verified, err := s.run(ctx, operationResolve, "-C", s.projectDir, "--no-replace-objects", "rev-parse", "--verify", revision+"^{commit}")
	if err != nil {
		return inventory.Commit{}, err
	}
	sha := strings.TrimSpace(string(verified.stdout))
	if !fullSHA.MatchString(sha) {
		return inventory.Commit{}, fmt.Errorf("Git returned an invalid commit object ID: %w", errs.ErrUnavailable)
	}
	sha = strings.ToLower(sha)
	if sha != strings.ToLower(revision) {
		return inventory.Commit{}, fmt.Errorf("Git resolved a commit identity different from the declared complete SHA: %w", errs.ErrConflict)
	}

	metadata, err := s.run(
		ctx,
		operationMetadata,
		"-C", s.projectDir,
		"--no-replace-objects",
		"show", "--no-patch", "--no-show-signature",
		"--format=%H%x00%at%x00%ct%x00%s%x00%T",
		sha,
	)
	if err != nil {
		return inventory.Commit{}, err
	}
	commit, err := parseCommit(metadata.stdout)
	if err != nil {
		return inventory.Commit{}, fmt.Errorf("parse Git commit metadata: %w", err)
	}
	if commit.SHA != sha {
		return inventory.Commit{}, fmt.Errorf("Git metadata did not match verified commit %s: %w", sha, errs.ErrUnavailable)
	}
	return commit, nil
}

func (s *Source) validate() error {
	if s == nil || strings.TrimSpace(s.projectDir) == "" {
		return fmt.Errorf("Git project directory is required: %w", errs.ErrInvalid)
	}
	if s.runner == nil {
		return fmt.Errorf("Git command runner is not configured: %w", errs.ErrUnavailable)
	}
	return nil
}

type commandOperation string

const (
	operationRepository commandOperation = "discover repository"
	operationOrigin     commandOperation = "read origin URL"
	operationHead       commandOperation = "read HEAD commit"
	operationResolve    commandOperation = "resolve commit"
	operationMetadata   commandOperation = "read commit metadata"
)

// Head returns the complete commit currently referenced by local HEAD.
func (s *Source) Head(ctx context.Context) (inventory.Commit, error) {
	if err := s.validate(); err != nil {
		return inventory.Commit{}, err
	}
	resolved, err := s.run(ctx, operationHead, "-C", s.projectDir, "--no-replace-objects", "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return inventory.Commit{}, err
	}
	sha := strings.ToLower(strings.TrimSpace(string(resolved.stdout)))
	if !fullSHA.MatchString(sha) {
		return inventory.Commit{}, fmt.Errorf("Git returned an invalid HEAD object ID: %w", errs.ErrUnavailable)
	}
	return s.ResolveCommit(ctx, sha)
}

func (s *Source) originURL(ctx context.Context) (string, error) {
	commandCtx, cancel := s.commandContext(ctx)
	defer cancel()
	result, err := s.runner.Run(commandCtx, "git", "-C", s.projectDir, "config", "--get", "remote.origin.url")
	if err == nil {
		return canonicalizeRemote(string(result.stdout)), nil
	}
	if contextErr := commandContextError(commandCtx, err); contextErr != nil {
		return "", fmt.Errorf("%s: %w", operationOrigin, contextErr)
	}
	if !errors.Is(err, errOutputLimit) && exitCode(err) == 1 {
		// Git uses status 1 for a missing config key. An origin is optional.
		return "", nil
	}
	return "", s.classify(commandCtx, operationOrigin, result, err)
}

func (s *Source) run(ctx context.Context, operation commandOperation, args ...string) (commandResult, error) {
	commandCtx, cancel := s.commandContext(ctx)
	defer cancel()
	result, err := s.runner.Run(commandCtx, "git", args...)
	if err == nil {
		return result, nil
	}
	return commandResult{}, s.classify(commandCtx, operation, result, err)
}

func (s *Source) commandContext(parent context.Context) (context.Context, context.CancelFunc) {
	timeout := s.timeout
	if timeout <= 0 {
		timeout = defaultCommandTimeout
	}
	return context.WithTimeout(parent, timeout)
}

func (s *Source) classify(ctx context.Context, operation commandOperation, result commandResult, cause error) error {
	if contextErr := commandContextError(ctx, cause); contextErr != nil {
		return fmt.Errorf("%s: %w", operation, contextErr)
	}

	category := errs.ErrUnavailable
	code := exitCode(cause)
	if !errors.Is(cause, errOutputLimit) && (operation == operationRepository || operation == operationHead || operation == operationResolve) && (code == 1 || code == 128) {
		category = errs.ErrNotFound
	}
	diagnostic := sanitizeDiagnostic(result.stderr, s.projectDir)
	if diagnostic == "" {
		return fmt.Errorf("%s: %w: %w", operation, category, cause)
	}
	return fmt.Errorf("%s (%s): %w: %w", operation, diagnostic, category, cause)
}

func commandContextError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return nil
}

func exitCode(err error) int {
	type exitCoder interface {
		ExitCode() int
	}
	var coder exitCoder
	if errors.As(err, &coder) {
		return coder.ExitCode()
	}
	return -1
}

func parseRepositoryRoot(output []byte) (string, error) {
	root := strings.TrimSuffix(string(output), "\n")
	root = strings.TrimSuffix(root, "\r")
	if root == "" || !filepath.IsAbs(root) {
		return "", fmt.Errorf("Git returned a non-absolute repository root: %w", errs.ErrUnavailable)
	}
	return filepath.Clean(root), nil
}

func parseCommit(output []byte) (inventory.Commit, error) {
	line := strings.TrimSuffix(string(output), "\n")
	line = strings.TrimSuffix(line, "\r")
	fields := strings.Split(line, "\x00")
	if len(fields) != 5 {
		return inventory.Commit{}, fmt.Errorf("expected 5 metadata fields, got %d: %w", len(fields), errs.ErrUnavailable)
	}
	sha := strings.ToLower(fields[0])
	treeSHA := strings.ToLower(fields[4])
	if !fullSHA.MatchString(sha) || !fullSHA.MatchString(treeSHA) {
		return inventory.Commit{}, fmt.Errorf("Git returned invalid commit or tree object IDs: %w", errs.ErrUnavailable)
	}
	authorUnix, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return inventory.Commit{}, fmt.Errorf("parse author timestamp: %w: %w", errs.ErrUnavailable, err)
	}
	commitUnix, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return inventory.Commit{}, fmt.Errorf("parse commit timestamp: %w: %w", errs.ErrUnavailable, err)
	}
	authorTime := time.Unix(authorUnix, 0).UTC()
	return inventory.Commit{
		SHA:        sha,
		AuthorTime: &authorTime,
		CommitTime: time.Unix(commitUnix, 0).UTC(),
		Subject:    truncateRunes(fields[3], maxSubjectRunes),
		TreeSHA:    treeSHA,
	}, nil
}

func hashString(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func canonicalizeRemote(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "\r\n\x00") {
		return ""
	}

	if !strings.Contains(raw, "://") {
		if match := scpLikeRemote.FindStringSubmatch(raw); len(match) == 3 {
			return canonicalRemoteURL("ssh", match[1], match[2])
		}
		// Local paths are deliberately not exposed.
		return ""
	}

	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && scheme != "http" && scheme != "ssh" && scheme != "git" {
		return ""
	}
	return canonicalRemoteURL(scheme, parsed.Host, parsed.Path)
}

func canonicalRemoteURL(scheme, host, remotePath string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	remotePath = strings.Trim(strings.TrimSpace(remotePath), "/")
	remotePath = strings.TrimSuffix(remotePath, ".git")
	if host == "" || remotePath == "" {
		return ""
	}
	return (&url.URL{Scheme: scheme, Host: host, Path: "/" + remotePath}).String()
}

func repositoryName(canonicalURL string) string {
	parsed, err := url.Parse(canonicalURL)
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(strings.TrimSuffix(path.Base(strings.TrimSuffix(parsed.Path, "/")), ".git"))
	if name == "" || name == "." {
		return ""
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return ""
		}
	}
	return truncateRunes(name, maxRepositoryRunes)
}

func sanitizeDiagnostic(raw []byte, projectDir string) string {
	diagnostic := strings.TrimSpace(string(raw))
	if projectDir != "" {
		diagnostic = strings.ReplaceAll(diagnostic, projectDir, "<project-dir>")
		if absolute, err := filepath.Abs(projectDir); err == nil {
			diagnostic = strings.ReplaceAll(diagnostic, absolute, "<project-dir>")
		}
	}
	diagnostic = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, diagnostic)
	diagnostic = strings.Join(strings.Fields(diagnostic), " ")
	diagnostic = credentialPair.ReplaceAllString(diagnostic, "$1=<redacted>")
	diagnostic = urlUserInfo.ReplaceAllString(diagnostic, "://<redacted>@")
	return truncateRunes(diagnostic, maxDiagnosticRunes)
}

func truncateRunes(value string, limit int) string {
	if limit < 1 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

type commandResult struct {
	stdout []byte
	stderr []byte
}

type commandRunner interface {
	Run(ctx context.Context, name string, args ...string) (commandResult, error)
}

type execRunner struct {
	outputLimit int
}

func (r execRunner) Run(ctx context.Context, name string, args ...string) (commandResult, error) {
	limit := r.outputLimit
	if limit <= 0 {
		limit = defaultOutputLimit
	}
	budget := &outputBudget{limit: limit}
	stdout := &boundedBuffer{budget: budget}
	stderr := &boundedBuffer{budget: budget}
	command := exec.CommandContext(ctx, name, args...)
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	result := commandResult{stdout: stdout.Bytes(), stderr: stderr.Bytes()}
	if budget.Exceeded() {
		if err != nil {
			return result, fmt.Errorf("%w: %w", errOutputLimit, err)
		}
		return result, errOutputLimit
	}
	return result, err
}

type outputBudget struct {
	mu       sync.Mutex
	limit    int
	used     int
	exceeded bool
}

func (b *outputBudget) Exceeded() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.exceeded
}

type boundedBuffer struct {
	budget *outputBudget
	buffer bytes.Buffer
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	b.budget.mu.Lock()
	defer b.budget.mu.Unlock()

	remaining := b.budget.limit - b.budget.used
	if remaining > len(value) {
		remaining = len(value)
	}
	if remaining > 0 {
		_, _ = b.buffer.Write(value[:remaining])
		b.budget.used += remaining
	}
	if remaining < len(value) {
		b.budget.exceeded = true
	}
	// Report the entire write as consumed so the child process cannot block or
	// turn truncation into an unrelated broken-pipe failure.
	return len(value), nil
}

func (b *boundedBuffer) Bytes() []byte {
	b.budget.mu.Lock()
	defer b.budget.mu.Unlock()
	return bytes.Clone(b.buffer.Bytes())
}

var _ inventory.CommitSource = (*Source)(nil)
