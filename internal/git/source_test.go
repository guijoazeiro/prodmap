package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/guijoazeiro/prodmap/internal/errs"
)

func TestSourceReadsRepositoryAndCompleteCommit(t *testing.T) {
	repositoryDir := newTestRepository(t)
	runGit(t, repositoryDir, "remote", "add", "origin", "https://user:secret@example.com/acme/sample.git?token=secret#fragment")
	sha := strings.TrimSpace(runGit(t, repositoryDir, "rev-parse", "HEAD"))

	source := New(repositoryDir)
	repository, err := source.Repository(context.Background())
	if err != nil {
		t.Fatalf("Repository() error = %v", err)
	}

	wantRootHash := sha256.Sum256([]byte(filepath.Clean(repositoryDir)))
	if repository.RootPathHash != hex.EncodeToString(wantRootHash[:]) {
		t.Fatalf("RootPathHash = %q, want %q", repository.RootPathHash, hex.EncodeToString(wantRootHash[:]))
	}
	if repository.CanonicalURL != "https://example.com/acme/sample" {
		t.Fatalf("CanonicalURL = %q, want sanitized canonical URL", repository.CanonicalURL)
	}
	if repository.Name != "sample" {
		t.Fatalf("Name = %q, want sample", repository.Name)
	}
	if repository.ExternalID != "origin:"+hashString(repository.CanonicalURL) {
		t.Fatalf("ExternalID = %q, want origin-derived identity", repository.ExternalID)
	}
	for field, value := range map[string]string{
		"ExternalID":   repository.ExternalID,
		"Name":         repository.Name,
		"CanonicalURL": repository.CanonicalURL,
		"RootPathHash": repository.RootPathHash,
	} {
		if strings.Contains(value, repositoryDir) {
			t.Fatalf("%s leaks repository root %q", field, value)
		}
		if strings.Contains(value, "secret") || strings.Contains(value, "user") {
			t.Fatalf("%s leaks remote credentials: %q", field, value)
		}
	}

	commit, err := source.ResolveCommit(context.Background(), strings.ToUpper(sha))
	if err != nil {
		t.Fatalf("ResolveCommit() error = %v", err)
	}
	if commit.SHA != sha || len(commit.SHA) != 40 {
		t.Fatalf("SHA = %q, want complete SHA %q", commit.SHA, sha)
	}
	if commit.TreeSHA == "" || len(commit.TreeSHA) != 40 || !fullSHA.MatchString(commit.TreeSHA) {
		t.Fatalf("TreeSHA = %q, want complete tree SHA", commit.TreeSHA)
	}
	if commit.AuthorTime == nil {
		t.Fatal("AuthorTime = nil, want Git author time")
	}
	wantAuthorTime := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	wantCommitTime := time.Date(2002, 3, 4, 5, 6, 7, 0, time.UTC)
	if !commit.AuthorTime.Equal(wantAuthorTime) || commit.AuthorTime.Location() != time.UTC {
		t.Fatalf("AuthorTime = %v, want %v in UTC", commit.AuthorTime, wantAuthorTime)
	}
	if !commit.CommitTime.Equal(wantCommitTime) || commit.CommitTime.Location() != time.UTC {
		t.Fatalf("CommitTime = %v, want %v in UTC", commit.CommitTime, wantCommitTime)
	}
	if commit.Subject != "test provenance" {
		t.Fatalf("Subject = %q, want test provenance", commit.Subject)
	}
	head, err := source.Head(context.Background())
	if err != nil {
		t.Fatalf("Head() error = %v", err)
	}
	if head.SHA != sha {
		t.Fatalf("Head().SHA = %q, want %q", head.SHA, sha)
	}
}

func TestRepositoryWithoutOriginUsesOnlyHashedLocalIdentity(t *testing.T) {
	repositoryDir := newTestRepository(t)
	repository, err := New(repositoryDir).Repository(context.Background())
	if err != nil {
		t.Fatalf("Repository() error = %v", err)
	}
	if repository.CanonicalURL != "" {
		t.Fatalf("CanonicalURL = %q, want empty for repository without origin", repository.CanonicalURL)
	}
	if !strings.HasPrefix(repository.ExternalID, "local:") {
		t.Fatalf("ExternalID = %q, want local hash identity", repository.ExternalID)
	}
	if !strings.HasPrefix(repository.Name, "repository-") {
		t.Fatalf("Name = %q, want non-path fallback name", repository.Name)
	}
	if strings.Contains(repository.ExternalID+repository.Name, repositoryDir) {
		t.Fatalf("repository identity leaks root path: %+v", repository)
	}
}

func TestSourceReadsCommitFromLinkedWorktree(t *testing.T) {
	repositoryDir := newTestRepository(t)
	sha := strings.TrimSpace(runGit(t, repositoryDir, "rev-parse", "HEAD"))
	linkedDir := filepath.Join(t.TempDir(), "linked")
	runGit(t, repositoryDir, "worktree", "add", "--detach", linkedDir, "HEAD")

	source := New(linkedDir)
	repository, err := source.Repository(context.Background())
	if err != nil {
		t.Fatalf("Repository(linked worktree) error = %v", err)
	}
	wantRootHash := sha256.Sum256([]byte(filepath.Clean(linkedDir)))
	if repository.RootPathHash != hex.EncodeToString(wantRootHash[:]) {
		t.Fatalf("linked RootPathHash = %q, want hash of worktree root", repository.RootPathHash)
	}
	commit, err := source.ResolveCommit(context.Background(), sha)
	if err != nil {
		t.Fatalf("ResolveCommit(linked worktree) error = %v", err)
	}
	if commit.SHA != sha {
		t.Fatalf("ResolveCommit(linked worktree).SHA = %q, want %q", commit.SHA, sha)
	}
}

func TestRepositoryNotFoundIsCategorized(t *testing.T) {
	_, err := New(t.TempDir()).Repository(context.Background())
	if !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("Repository() error = %v, want ErrNotFound", err)
	}
}

func TestResolveCommitRejectsNonFullSHAWithoutCallingGit(t *testing.T) {
	runner := &recordingRunner{}
	_, err := New("/project", withRunner(runner)).ResolveCommit(context.Background(), "HEAD")
	if !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("ResolveCommit(HEAD) error = %v, want ErrInvalid", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner calls = %d, want 0 for invalid input", len(runner.calls))
	}

	_, err = New("/project", withRunner(runner)).ResolveCommit(context.Background(), strings.Repeat("a", 39))
	if !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("ResolveCommit(short SHA) error = %v, want ErrInvalid", err)
	}
	_, err = New("/project", withRunner(runner)).ResolveCommit(context.Background(), " "+strings.Repeat("a", 40))
	if !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("ResolveCommit(padded SHA) error = %v, want ErrInvalid", err)
	}
}

func TestResolveCommitNotFoundIsCategorized(t *testing.T) {
	repositoryDir := newTestRepository(t)
	_, err := New(repositoryDir).ResolveCommit(context.Background(), strings.Repeat("f", 40))
	if !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("ResolveCommit() error = %v, want ErrNotFound", err)
	}
}

func TestResolveCommitAcceptsCompleteSHA256ObjectID(t *testing.T) {
	sha := strings.Repeat("a", 64)
	treeSHA := strings.Repeat("b", 64)
	calls := 0
	runner := runnerFunc(func(_ context.Context, name string, args ...string) (commandResult, error) {
		calls++
		if name != "git" {
			t.Fatalf("runner name = %q, want git", name)
		}
		switch calls {
		case 1:
			if got := args[len(args)-1]; got != sha+"^{commit}" {
				t.Fatalf("verification revision = %q, want complete SHA-256 expression", got)
			}
			return commandResult{stdout: []byte(sha + "\n")}, nil
		case 2:
			return commandResult{stdout: []byte(sha + "\x001\x002\x00sha256 commit\x00" + treeSHA + "\n")}, nil
		default:
			t.Fatalf("unexpected runner call %d", calls)
			return commandResult{}, nil
		}
	})

	commit, err := New("/project", withRunner(runner)).ResolveCommit(context.Background(), sha)
	if err != nil {
		t.Fatalf("ResolveCommit(SHA-256) error = %v", err)
	}
	if commit.SHA != sha || commit.TreeSHA != treeSHA {
		t.Fatalf("ResolveCommit(SHA-256) = %+v, want complete object IDs", commit)
	}
}

func TestResolveCommitRejectsDifferentResolvedIdentity(t *testing.T) {
	declared := strings.Repeat("a", 40)
	resolved := strings.Repeat("a", 64)
	runner := runnerFunc(func(_ context.Context, _ string, _ ...string) (commandResult, error) {
		return commandResult{stdout: []byte(resolved + "\n")}, nil
	})
	_, err := New("/project", withRunner(runner)).ResolveCommit(context.Background(), declared)
	if !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("ResolveCommit() error = %v, want ErrConflict", err)
	}
}

func TestSourcePreservesRunnerUnavailableCause(t *testing.T) {
	cause := &exec.Error{Name: "git", Err: exec.ErrNotFound}
	runner := runnerFunc(func(context.Context, string, ...string) (commandResult, error) {
		return commandResult{}, cause
	})

	_, err := New("/project", withRunner(runner)).Repository(context.Background())
	if !errors.Is(err, errs.ErrUnavailable) {
		t.Fatalf("Repository() error = %v, want ErrUnavailable", err)
	}
	var gotCause *exec.Error
	if !errors.As(err, &gotCause) || gotCause != cause {
		t.Fatalf("Repository() error = %v, want preserved exec.Error cause", err)
	}
}

func TestSourcePreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := runnerFunc(func(ctx context.Context, _ string, _ ...string) (commandResult, error) {
		return commandResult{}, ctx.Err()
	})

	_, err := New("/project", withRunner(runner)).Repository(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Repository() error = %v, want context.Canceled", err)
	}
	if errors.Is(err, errs.ErrUnavailable) {
		t.Fatalf("Repository() cancellation was incorrectly categorized unavailable: %v", err)
	}
}

func TestSourceAppliesCommandTimeout(t *testing.T) {
	runner := runnerFunc(func(ctx context.Context, _ string, _ ...string) (commandResult, error) {
		<-ctx.Done()
		return commandResult{}, ctx.Err()
	})
	source := New("/project", withRunner(runner))
	source.timeout = time.Millisecond
	_, err := source.Repository(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Repository() error = %v, want context deadline exceeded", err)
	}
}

func TestSourceCategorizesOutputLimitWithoutExposingDiagnostic(t *testing.T) {
	projectDir := "/private/work/project"
	runner := runnerFunc(func(context.Context, string, ...string) (commandResult, error) {
		return commandResult{stderr: []byte(projectDir + "\naccess_token=must-not-leak api_key=opaque-secret Authorization: Bearer alphanumericsecret https://user:password@example.invalid/repo")}, errOutputLimit
	})

	_, err := New(projectDir, withRunner(runner)).Repository(context.Background())
	if !errors.Is(err, errs.ErrUnavailable) || !errors.Is(err, errOutputLimit) {
		t.Fatalf("Repository() error = %v, want ErrUnavailable preserving output-limit cause", err)
	}
	if strings.Contains(err.Error(), projectDir) {
		t.Fatalf("Repository() error leaks project directory: %v", err)
	}
	if strings.ContainsAny(err.Error(), "\n\r\t") {
		t.Fatalf("Repository() diagnostic contains control whitespace: %q", err)
	}
	if strings.Contains(err.Error(), "must-not-leak") || strings.Contains(err.Error(), "opaque-secret") || strings.Contains(err.Error(), "alphanumericsecret") || strings.Contains(err.Error(), "user:password") {
		t.Fatalf("Repository() diagnostic leaks credentials: %v", err)
	}
}

func TestResolveExit128WithoutMissingObjectEvidenceIsUnavailable(t *testing.T) {
	sha := strings.Repeat("a", 40)
	runner := runnerFunc(func(context.Context, string, ...string) (commandResult, error) {
		return commandResult{stderr: []byte("fatal: permission denied; Authorization: Bearer must-not-leak")}, fakeExitError{code: 128}
	})
	_, err := New("/project", withRunner(runner)).ResolveCommit(context.Background(), sha)
	if !errors.Is(err, errs.ErrUnavailable) || errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("ResolveCommit(permission failure) error=%v, want only ErrUnavailable", err)
	}
	if strings.Contains(err.Error(), "must-not-leak") || strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("ResolveCommit() exposed Git diagnostic: %v", err)
	}
}

func TestResolveExit128WithMissingObjectEvidenceIsNotFound(t *testing.T) {
	sha := strings.Repeat("a", 40)
	runner := runnerFunc(func(context.Context, string, ...string) (commandResult, error) {
		return commandResult{stderr: []byte("fatal: Needed a single revision")}, fakeExitError{code: 128}
	})
	_, err := New("/project", withRunner(runner)).ResolveCommit(context.Background(), sha)
	if !errors.Is(err, errs.ErrNotFound) || errors.Is(err, errs.ErrUnavailable) {
		t.Fatalf("ResolveCommit(missing object) error=%v, want only ErrNotFound", err)
	}
}

func TestOutputLimitTakesPrecedenceOverNotFoundExitCode(t *testing.T) {
	cause := fmt.Errorf("%w: %w", errOutputLimit, fakeExitError{code: 128})
	runner := runnerFunc(func(context.Context, string, ...string) (commandResult, error) {
		return commandResult{}, cause
	})
	_, err := New("/project", withRunner(runner)).Repository(context.Background())
	if !errors.Is(err, errs.ErrUnavailable) || errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("Repository() error = %v, want output limit categorized unavailable", err)
	}
}

func TestExecRunnerSeparatesAndLimitsOutput(t *testing.T) {
	t.Setenv("PRODMAP_GIT_HELPER_PROCESS", "1")
	runner := execRunner{outputLimit: 10}
	result, err := runner.Run(context.Background(), os.Args[0], "-test.run=TestGitCommandHelperProcess", "--", "emit")
	if !errors.Is(err, errOutputLimit) {
		t.Fatalf("Run() error = %v, want errOutputLimit", err)
	}
	if len(result.stdout)+len(result.stderr) != 10 {
		t.Fatalf("captured bytes = %d, want shared limit 10", len(result.stdout)+len(result.stderr))
	}
	if strings.ContainsRune(string(result.stdout), 'e') || strings.ContainsRune(string(result.stderr), 'o') {
		t.Fatalf("stdout and stderr were mixed: stdout=%q stderr=%q", result.stdout, result.stderr)
	}
}

func TestGitCommandHelperProcess(t *testing.T) {
	if os.Getenv("PRODMAP_GIT_HELPER_PROCESS") != "1" {
		return
	}
	_, _ = fmt.Fprint(os.Stdout, strings.Repeat("o", 8))
	_, _ = fmt.Fprint(os.Stderr, strings.Repeat("e", 8))
	os.Exit(0)
}

func TestParseCommitLimitsSubjectByRunes(t *testing.T) {
	sha := strings.Repeat("a", 40)
	tree := strings.Repeat("b", 40)
	subject := strings.Repeat("á", maxSubjectRunes+10)
	commit, err := parseCommit([]byte(sha + "\x001\x002\x00" + subject + "\x00" + tree + "\n"))
	if err != nil {
		t.Fatalf("parseCommit() error = %v", err)
	}
	if utf8.RuneCountInString(commit.Subject) != maxSubjectRunes {
		t.Fatalf("subject runes = %d, want %d", utf8.RuneCountInString(commit.Subject), maxSubjectRunes)
	}
}

func TestCanonicalizeRemoteRejectsLocalPathsAndCredentials(t *testing.T) {
	tests := map[string]string{
		"scp-like":           "git@GitHub.COM:acme/prodmap.git",
		"credentialed HTTPS": "https://user:token@GitHub.COM/acme/prodmap.git?token=secret#fragment",
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if got := canonicalizeRemote(input); got != map[string]string{
				"scp-like":           "ssh://github.com/acme/prodmap",
				"credentialed HTTPS": "https://github.com/acme/prodmap",
			}[name] {
				t.Fatalf("canonicalizeRemote(%q) = %q", input, got)
			}
		})
	}
	for _, input := range []string{"/srv/git/prodmap.git", "../prodmap.git", "file:///srv/git/prodmap.git"} {
		if got := canonicalizeRemote(input); got != "" {
			t.Fatalf("canonicalizeRemote(%q) = %q, want empty", input, got)
		}
	}
}

func TestRepositoryNameIsSafeAndLimited(t *testing.T) {
	if got := repositoryName("https://example.com/acme/repo%0Aname"); got != "" {
		t.Fatalf("repositoryName(control character) = %q, want empty", got)
	}
	longName := strings.Repeat("á", maxRepositoryRunes+10)
	if got := repositoryName("https://example.com/acme/" + longName); utf8.RuneCountInString(got) != maxRepositoryRunes {
		t.Fatalf("repositoryName(long name) has %d runes, want %d", utf8.RuneCountInString(got), maxRepositoryRunes)
	}
}

func newTestRepository(t *testing.T) string {
	t.Helper()
	repositoryDir := t.TempDir()
	runGit(t, repositoryDir, "init", "--quiet")
	runGit(t, repositoryDir, "config", "user.name", "Prodmap Test")
	runGit(t, repositoryDir, "config", "user.email", "prodmap@example.invalid")
	command := exec.Command("git", "-C", repositoryDir, "commit", "--quiet", "--allow-empty", "-m", "test provenance")
	command.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE=2001-02-03T04:05:06Z",
		"GIT_COMMITTER_DATE=2002-03-04T05:06:07Z",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create test commit: %v: %s", err, output)
	}
	return repositoryDir
}

func runGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", directory}, args...)
	command := exec.Command("git", commandArgs...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

type runnerFunc func(context.Context, string, ...string) (commandResult, error)

func (f runnerFunc) Run(ctx context.Context, name string, args ...string) (commandResult, error) {
	return f(ctx, name, args...)
}

type commandCall struct {
	name string
	args []string
}

type recordingRunner struct {
	calls []commandCall
}

type fakeExitError struct {
	code int
}

func (e fakeExitError) Error() string {
	return fmt.Sprintf("exit status %d", e.code)
}

func (e fakeExitError) ExitCode() int {
	return e.code
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) (commandResult, error) {
	r.calls = append(r.calls, commandCall{name: name, args: append([]string(nil), args...)})
	return commandResult{}, nil
}
