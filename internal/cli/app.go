package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/guijoazeiro/prodmap/internal/config"
	"github.com/guijoazeiro/prodmap/internal/deployment"
	prodmapdocker "github.com/guijoazeiro/prodmap/internal/docker"
	"github.com/guijoazeiro/prodmap/internal/errs"
	prodmapgit "github.com/guijoazeiro/prodmap/internal/git"
	prodmapgithub "github.com/guijoazeiro/prodmap/internal/github"
	"github.com/guijoazeiro/prodmap/internal/inventory"
	"github.com/guijoazeiro/prodmap/internal/logging"
)

// App owns the CLI process dependencies and keeps command execution testable.
type App struct {
	Stdout                 io.Writer
	Stderr                 io.Writer
	Now                    func() time.Time
	WorkingDir             func() (string, error)
	Environment            map[string]string
	UserConfigPath         string
	BuildInfo              BuildInfo
	LookPath               func(string) (string, error)
	RunExternal            func(context.Context, string, ...string) error
	RuntimeSource          func() inventory.RuntimeSource
	CommitSource           func(string) inventory.CommitSource
	GitHubDeploymentSource func(string) (deployment.DeploymentSource, error)
	GitHubSyncTimeout      time.Duration
}

// NewApp constructs a CLI using process-backed defaults.
func NewApp(stdout, stderr io.Writer, buildInfo BuildInfo) *App {
	return &App{
		Stdout:      stdout,
		Stderr:      stderr,
		Now:         time.Now,
		WorkingDir:  os.Getwd,
		Environment: processEnvironment(),
		BuildInfo:   buildInfo,
		LookPath:    exec.LookPath,
		RunExternal: func(ctx context.Context, name string, args ...string) error {
			return exec.CommandContext(ctx, name, args...).Run()
		},
		RuntimeSource:          func() inventory.RuntimeSource { return prodmapdocker.NewSource() },
		CommitSource:           func(projectDir string) inventory.CommitSource { return prodmapgit.New(projectDir) },
		GitHubDeploymentSource: func(token string) (deployment.DeploymentSource, error) { return prodmapgithub.New(token) },
		GitHubSyncTimeout:      2 * time.Minute,
	}
}

// Run executes one CLI invocation and returns its process exit code.
func (a *App) Run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(a.Stderr, "usage: prodmap <version|init|doctor|status|services|runtime|explain|telemetry|graph|endpoints|baseline|regression|deployments|deploys|timeline> [flags]")
		return ExitCode(errs.ErrInvalid)
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(a.Stdout, "usage: prodmap <version|init|doctor|status|services|runtime|explain|telemetry|graph|endpoints|baseline|regression|deployments|deploys|timeline> [flags]")
		return 0
	}

	command := args[0]
	publicCommand := command
	if command == "telemetry" && len(args) > 1 && args[1] == "ingest" {
		publicCommand = "telemetry ingest"
	}
	if command == "deployments" && len(args) > 1 && args[1] == "ingest" {
		publicCommand = "deployments ingest"
	}
	if command == "deployments" && len(args) > 2 && args[1] == "sync" && args[2] == "github-actions" {
		publicCommand = "deployments sync github-actions"
	}
	jsonRequested := containsJSONFlag(args[1:])
	var err error
	switch command {
	case "version":
		err = a.runVersion(args[1:])
	case "init":
		err = a.runInit(ctx, args[1:])
	case "doctor":
		err = a.runDoctor(ctx, args[1:])
	case "status":
		err = a.runStatus(ctx, args[1:])
	case "services":
		err = a.runServices(ctx, args[1:])
	case "runtime":
		err = a.runRuntime(ctx, args[1:])
	case "explain":
		err = a.runExplain(ctx, args[1:])
	case "telemetry":
		err = a.runTelemetry(ctx, args[1:])
	case "graph":
		err = a.runGraph(ctx, args[1:])
	case "endpoints":
		err = a.runEndpoints(ctx, args[1:])
	case "baseline":
		err = a.runBaseline(ctx, args[1:])
	case "regression":
		err = a.runRegression(ctx, args[1:])
	case "deployments":
		err = a.runDeployments(ctx, args[1:])
	case "deploys":
		err = a.runDeploys(ctx, args[1:])
	case "timeline":
		err = a.runTimeline(ctx, args[1:])
	case "help":
		switch {
		case len(args) == 2 && args[1] == "endpoints":
			writeEndpointsUsage(a.Stdout)
			return 0
		case len(args) == 2 && args[1] == "deployments":
			writeDeploymentsUsage(a.Stdout)
			return 0
		case len(args) == 2 && args[1] == "deploys":
			writeDeploysUsage(a.Stdout)
			return 0
		case len(args) == 2 && args[1] == "timeline":
			writeTimelineUsage(a.Stdout)
			return 0
		case len(args) == 2 && args[1] == "baseline":
			writeBaselineUsage(a.Stdout)
			return 0
		case len(args) == 2 && args[1] == "regression":
			writeRegressionUsage(a.Stdout)
			return 0
		default:
			err = fmt.Errorf("unknown help topic: %w", errs.ErrInvalid)
		}
	default:
		err = fmt.Errorf("unknown command %q: %w", command, errs.ErrInvalid)
	}
	if err == nil {
		return 0
	}

	var rendered *renderedError
	if errors.As(err, &rendered) {
		return ExitCode(rendered.err)
	}
	if jsonRequested {
		payload := ErrorPayload{Code: PublicErrorCode(err), Message: err.Error(), Retryable: errors.Is(err, errs.ErrUnavailable)}
		var ambiguous *inventory.AmbiguousSelectorError
		if errors.As(err, &ambiguous) {
			payload.Details = map[string]any{"candidates": nonNilSelectorCandidates(ambiguous.Candidates)}
		}
		if writeErr := WriteError(a.Stdout, publicCommand, a.Now(), payload); writeErr != nil {
			fmt.Fprintf(a.Stderr, "write JSON error: %v\n", writeErr)
			return 1
		}
	} else {
		var ambiguous *inventory.AmbiguousSelectorError
		if errors.As(err, &ambiguous) {
			fmt.Fprintln(a.Stderr, "target is ambiguous; choose one candidate:")
			for _, candidate := range nonNilSelectorCandidates(ambiguous.Candidates) {
				fmt.Fprintf(a.Stderr, "  %s %s", candidate.Type, candidate.ID)
				if candidate.DisplayLabel != "" {
					fmt.Fprintf(a.Stderr, " — %s", safeDisplayLabel(candidate.DisplayLabel))
				}
				fmt.Fprintln(a.Stderr)
			}
		} else {
			fmt.Fprintln(a.Stderr, err)
		}
	}
	return ExitCode(err)
}

func nonNilSelectorCandidates(values []inventory.SelectorCandidate) []inventory.SelectorCandidate {
	if values == nil {
		return []inventory.SelectorCandidate{}
	}
	return values
}

func safeDisplayLabel(value string) string {
	value = strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return ' '
		}
		return character
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	const limit = 160
	runes := []rune(value)
	if len(runes) > limit {
		value = string(runes[:limit]) + "…"
	}
	return value
}

func (a *App) runVersion(args []string) error {
	flags := flag.NewFlagSet("version", flag.ContinueOnError)
	jsonOutput := flags.Bool("json", false, "emit JSON")
	help, err := a.parseCommandFlags(flags, args)
	if help {
		return nil
	}
	if err != nil {
		return fmt.Errorf("parse version flags: %w: %v", errs.ErrInvalid, err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("version accepts no arguments: %w", errs.ErrInvalid)
	}
	return WriteVersion(a.Stdout, a.BuildInfo, *jsonOutput, a.Now())
}

func (a *App) parseCommandFlags(flags *flag.FlagSet, args []string) (bool, error) {
	var output bytes.Buffer
	flags.SetOutput(&output)
	err := flags.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		if _, writeErr := a.Stdout.Write(output.Bytes()); writeErr != nil {
			return false, writeErr
		}
		return true, nil
	}
	return false, err
}

func (a *App) loadConfig(projectDir, dataDir string) (config.Config, error) {
	overrides := config.Overrides{}
	if projectDir != "" {
		overrides.ProjectDir = &projectDir
	}
	if dataDir != "" {
		overrides.DataDir = &dataDir
	}
	userPath := a.UserConfigPath
	if userPath == "" {
		if xdg := a.Environment["XDG_CONFIG_HOME"]; xdg != "" {
			userPath = filepath.Join(xdg, "prodmap", "config.yaml")
		} else if home := a.Environment["HOME"]; home != "" {
			userPath = filepath.Join(home, ".config", "prodmap", "config.yaml")
		}
	}
	return config.Load(config.Options{
		ProjectDir:     projectDir,
		UserConfigPath: userPath,
		Env:            a.Environment,
		Overrides:      overrides,
	})
}

func (a *App) commandLogger(cfg config.Config, operationID string) (*slog.Logger, error) {
	logger, err := logging.New(a.Stderr, logging.Options{Level: cfg.Log.Level, Format: cfg.Log.Format})
	if err != nil {
		return nil, &errs.ConfigError{Err: fmt.Errorf("construct logger: %w", err)}
	}
	return logger.With("operation_id", operationID, "source", "cli"), nil
}

func (a *App) currentWorkingDir() (string, error) {
	dir, err := a.WorkingDir()
	if err != nil {
		return "", fmt.Errorf("determine current directory: %w", errs.ErrInvalid)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve current directory: %w", errs.ErrInvalid)
	}
	return abs, nil
}

func operationID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "unavailable"
	}
	return hex.EncodeToString(raw[:])
}

func processEnvironment() map[string]string {
	result := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			result[key] = value
		}
	}
	return result
}

func containsJSONFlag(args []string) bool {
	requested := false
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "--json" || arg == "-json" {
			requested = true
			continue
		}
		if key, value, ok := strings.Cut(arg, "="); ok && (key == "--json" || key == "-json") {
			parsed, err := strconv.ParseBool(value)
			if err == nil {
				requested = parsed
			}
		}
	}
	return requested
}

type renderedError struct{ err error }

func (e *renderedError) Error() string { return e.err.Error() }
func (e *renderedError) Unwrap() error { return e.err }
