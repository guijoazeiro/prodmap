// Package mcpserver exposes the bounded local investigation view over MCP.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/guijoazeiro/prodmap/internal/baseline"
	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/investigation"
	investigationpackage "github.com/guijoazeiro/prodmap/internal/investigationpackage"
	"github.com/guijoazeiro/prodmap/internal/regression"
)

const (
	ToolName         = "investigate_deployment"
	ServerVersion    = "prodmap-mcp/v1"
	maxCallDuration  = 30 * time.Second
	maxResponseBytes = 2 << 20
)

// Reader is the small read-only dependency required by the MCP tool.
type Reader interface {
	investigation.Reader
}

// OpenReader creates one short-lived reader for a single tool call. The close
// function must release its snapshot before the result is rendered.
type OpenReader func(context.Context) (Reader, func() error, error)

// Config makes local MCP server dependencies explicit.
type Config struct {
	OpenReader OpenReader
	Now        func() time.Time
}

type metric string

// Input is the closed request schema for investigate_deployment.
type Input struct {
	Deployment  string   `json:"deployment" jsonschema:"internal UUIDv7 deployment identifier"`
	Metric      metric   `json:"metric" jsonschema:"comparison metric"`
	Before      *string  `json:"before,omitempty" jsonschema:"optional duration from 5m through 24h"`
	After       *string  `json:"after,omitempty" jsonschema:"optional duration from 5m through 24h"`
	MinSamples  *int64   `json:"min_samples,omitempty" jsonschema:"optional sample count from 1 through 1000000"`
	MinCoverage *float64 `json:"min_coverage,omitempty" jsonschema:"optional coverage from 0 through 1"`
}

// New constructs the MCP server with exactly one read-only tool.
func New(config Config) (*mcp.Server, error) {
	if config.OpenReader == nil {
		return nil, fmt.Errorf("MCP reader is required: %w", errs.ErrInvalid)
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	inputSchema, err := inputSchema()
	if err != nil {
		return nil, fmt.Errorf("construct MCP input schema: %w", err)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "prodmap", Version: ServerVersion}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        ToolName,
		Description: "Returns a bounded, read-only, non-causal operational investigation for one internal deployment UUID.",
		InputSchema: inputSchema,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, investigationpackage.Output, error) {
		return handle(ctx, config, input)
	})
	return server, nil
}

// RunStdio runs the server on the process standard streams.
func RunStdio(ctx context.Context, config Config) error {
	server, err := New(config)
	if err != nil {
		return err
	}
	return server.Run(ctx, &mcp.StdioTransport{})
}

func inputSchema() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[Input](&jsonschema.ForOptions{TypeSchemas: map[reflect.Type]*jsonschema.Schema{
		reflect.TypeFor[metric](): {Type: "string", Enum: []any{"request_count", "error_rate", "latency_p50", "latency_p95", "latency_p99"}},
	}})
	if err != nil {
		return nil, err
	}
	schema.Properties["min_samples"].Minimum = jsonschema.Ptr(1.0)
	schema.Properties["min_samples"].Maximum = jsonschema.Ptr(1_000_000.0)
	schema.Properties["min_coverage"].Minimum = jsonschema.Ptr(0.0)
	schema.Properties["min_coverage"].Maximum = jsonschema.Ptr(1.0)
	return schema, nil
}

func handle(ctx context.Context, config Config, input Input) (*mcp.CallToolResult, investigationpackage.Output, error) {
	query, err := queryFrom(input)
	if err != nil {
		return nil, investigationpackage.Output{}, publicError(err)
	}
	ctx, cancel := context.WithTimeout(ctx, maxCallDuration)
	defer cancel()
	generatedAt := config.Now().UTC()
	reader, closeReader, err := config.OpenReader(ctx)
	if err != nil {
		return nil, investigationpackage.Output{}, publicError(err)
	}
	if reader == nil || closeReader == nil {
		return nil, investigationpackage.Output{}, publicError(fmt.Errorf("MCP reader factory returned an invalid snapshot: %w", errs.ErrIncompatible))
	}
	closed := false
	defer func() {
		if !closed {
			_ = closeReader()
		}
	}()
	result, err := investigation.Compose(ctx, reader, investigation.Query{Comparison: query, GeneratedAt: generatedAt})
	if err != nil {
		return nil, investigationpackage.Output{}, publicError(err)
	}
	if err := closeReader(); err != nil {
		return nil, investigationpackage.Output{}, publicError(err)
	}
	closed = true
	output, err := investigationpackage.OutputFrom(result)
	if err != nil {
		return nil, investigationpackage.Output{}, publicError(err)
	}
	// AddTool serializes structured output to equivalent text. Check its exact
	// JSON representation before it can be emitted on the stdio protocol.
	data, err := json.Marshal(output)
	if err != nil || len(data) > maxResponseBytes {
		return nil, investigationpackage.Output{}, publicError(fmt.Errorf("%w: response exceeds maximum size", errs.ErrIncompatible))
	}
	return nil, output, nil
}

func queryFrom(input Input) (regression.Query, error) {
	query := regression.Query{
		DeploymentID: input.Deployment,
		Metric:       baseline.Metric(input.Metric),
		Before:       baseline.DefaultWindow,
		After:        baseline.DefaultWindow,
		MinSamples:   baseline.DefaultMinSamples,
		MinCoverage:  baseline.DefaultMinCoverage,
	}
	if input.Before != nil {
		value, err := time.ParseDuration(*input.Before)
		if err != nil {
			return regression.Query{}, fmt.Errorf("invalid before: %w", errs.ErrInvalid)
		}
		query.Before = value
	}
	if input.After != nil {
		value, err := time.ParseDuration(*input.After)
		if err != nil {
			return regression.Query{}, fmt.Errorf("invalid after: %w", errs.ErrInvalid)
		}
		query.After = value
	}
	if input.MinSamples != nil {
		query.MinSamples = *input.MinSamples
	}
	if input.MinCoverage != nil {
		query.MinCoverage = *input.MinCoverage
	}
	if err := regression.ValidateQuery(query); err != nil {
		return regression.Query{}, err
	}
	return query, nil
}

func publicError(err error) error {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return errors.New("SOURCE_UNAVAILABLE")
	case errors.Is(err, errs.ErrInvalid):
		return errors.New("INVALID_ARGUMENT")
	case errors.Is(err, errs.ErrNotFound):
		return errors.New("NOT_FOUND")
	case errors.Is(err, errs.ErrUnavailable):
		return errors.New("SOURCE_UNAVAILABLE")
	case errors.Is(err, errs.ErrInsufficient):
		return errors.New("INSUFFICIENT_DATA")
	case errors.Is(err, errs.ErrConflict):
		return errors.New("CONFLICT")
	case errors.Is(err, errs.ErrUnauthorized):
		return errors.New("ACCESS_DENIED")
	case errors.Is(err, errs.ErrIncompatible):
		return errors.New("INCOMPATIBLE_SCHEMA")
	default:
		return errors.New("INTERNAL")
	}
}
