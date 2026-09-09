// Package mcpserver exposes the bounded local investigation view over MCP.
package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/guijoazeiro/prodmap/internal/baseline"
	"github.com/guijoazeiro/prodmap/internal/deployment"
	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/identity"
	"github.com/guijoazeiro/prodmap/internal/investigation"
	investigationpackage "github.com/guijoazeiro/prodmap/internal/investigationpackage"
	"github.com/guijoazeiro/prodmap/internal/redaction"
	"github.com/guijoazeiro/prodmap/internal/regression"
)

const (
	InvestigateDeploymentToolName = "investigate_deployment"
	ListDeploymentsToolName       = "list_deployments"
	// ToolName remains an internal compatibility alias for v0.2 consumers.
	ToolName         = InvestigateDeploymentToolName
	ServerVersion    = "prodmap-mcp/v2"
	DiscoveryVersion = "deployment-discovery/v1"
	maxCallDuration  = 30 * time.Second
	maxResponseBytes = 2 << 20
	maxCursorBytes   = 2048
)

// Reader is the small read-only dependency required by the MCP tool.
type Reader interface {
	investigation.Reader
	DiscoverDeployments(context.Context, deployment.DiscoveryQuery) (deployment.DiscoveryResult, error)
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
type deploymentStatus string

// Input is the closed request schema for investigate_deployment.
type Input struct {
	Deployment  string   `json:"deployment" jsonschema:"internal UUIDv7 deployment identifier"`
	Metric      metric   `json:"metric" jsonschema:"comparison metric"`
	Before      *string  `json:"before,omitempty" jsonschema:"optional duration from 5m through 24h"`
	After       *string  `json:"after,omitempty" jsonschema:"optional duration from 5m through 24h"`
	MinSamples  *int64   `json:"min_samples,omitempty" jsonschema:"optional sample count from 1 through 1000000"`
	MinCoverage *float64 `json:"min_coverage,omitempty" jsonschema:"optional coverage from 0 through 1"`
}

// ListDeploymentsInput is a closed, sanitized request schema for discovery.
type ListDeploymentsInput struct {
	Environment *string           `json:"environment,omitempty" jsonschema:"optional deployment environment"`
	Service     *string           `json:"service,omitempty" jsonschema:"optional logical service"`
	Status      *deploymentStatus `json:"status,omitempty" jsonschema:"optional deployment status"`
	Since       *string           `json:"since,omitempty" jsonschema:"optional inclusive RFC3339Nano timestamp"`
	Until       *string           `json:"until,omitempty" jsonschema:"optional exclusive RFC3339Nano timestamp"`
	Limit       *int              `json:"limit,omitempty" jsonschema:"optional page size from 1 through 100"`
	Cursor      *string           `json:"cursor,omitempty" jsonschema:"optional opaque page cursor"`
}

type discoveryFiltersOutput struct {
	Environment *string `json:"environment"`
	Service     *string `json:"service"`
	Status      *string `json:"status"`
	Since       string  `json:"since"`
	Until       string  `json:"until"`
	Limit       int     `json:"limit"`
}

type discoveryConfidenceOutput struct {
	Level string `json:"level"`
	Basis string `json:"basis"`
}

type discoveryProvenanceOutput struct {
	Status      string                    `json:"status"`
	Confidence  discoveryConfidenceOutput `json:"confidence"`
	Limitations []string                  `json:"limitations"`
}

type discoveryItemOutput struct {
	DeploymentID     string                    `json:"deployment_id"`
	Environment      string                    `json:"environment"`
	Service          string                    `json:"service"`
	StartedAt        string                    `json:"started_at"`
	Status           string                    `json:"status"`
	Strategy         string                    `json:"strategy"`
	Provenance       discoveryProvenanceOutput `json:"provenance"`
	CausalityClaimed bool                      `json:"causality_claimed"`
}

// ListDeploymentsOutput is the allowlisted, versioned discovery projection.
type ListDeploymentsOutput struct {
	SchemaVersion string                 `json:"schema_version"`
	GeneratedAt   string                 `json:"generated_at"`
	Filters       discoveryFiltersOutput `json:"filters"`
	Items         []discoveryItemOutput  `json:"items"`
	NextCursor    *string                `json:"next_cursor"`
	Limitations   []string               `json:"limitations"`
}

// New constructs the MCP server with its two bounded, read-only tools.
func New(config Config) (*mcp.Server, error) {
	if config.OpenReader == nil {
		return nil, fmt.Errorf("MCP reader is required: %w", errs.ErrInvalid)
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	investigateSchema, err := inputSchema()
	if err != nil {
		return nil, fmt.Errorf("construct MCP input schema: %w", err)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "prodmap", Version: ServerVersion}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        InvestigateDeploymentToolName,
		Description: "Returns a bounded, read-only, non-causal operational investigation for one internal deployment UUID.",
		InputSchema: investigateSchema,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, investigationpackage.Output, error) {
		return handleInvestigation(ctx, config, input)
	})
	listSchema, err := listInputSchema()
	if err != nil {
		return nil, fmt.Errorf("construct MCP deployment discovery schema: %w", err)
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        ListDeploymentsToolName,
		Description: "Lists local recent deployments through bounded, read-only filters and returns internal UUIDs for investigate_deployment.",
		InputSchema: listSchema,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input ListDeploymentsInput) (*mcp.CallToolResult, ListDeploymentsOutput, error) {
		return handleListDeployments(ctx, config, input)
	})
	return server, nil
}

func listInputSchema() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[ListDeploymentsInput](&jsonschema.ForOptions{TypeSchemas: map[reflect.Type]*jsonschema.Schema{
		reflect.TypeFor[deploymentStatus](): {Type: "string", Enum: []any{"pending", "running", "succeeded", "failed", "cancelled", "rolled_back", "unknown"}},
	}})
	if err != nil {
		return nil, err
	}
	schema.Properties["limit"].Minimum = jsonschema.Ptr(1.0)
	schema.Properties["limit"].Maximum = jsonschema.Ptr(float64(deployment.DiscoveryMaxLimit))
	return schema, nil
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

func handleInvestigation(ctx context.Context, config Config, input Input) (*mcp.CallToolResult, investigationpackage.Output, error) {
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

// handle is retained for focused v0.2 handler tests.
func handle(ctx context.Context, config Config, input Input) (*mcp.CallToolResult, investigationpackage.Output, error) {
	return handleInvestigation(ctx, config, input)
}

type listCursor struct {
	Version     int    `json:"v"`
	Environment string `json:"e"`
	Service     string `json:"s"`
	Status      string `json:"x"`
	Since       string `json:"a"`
	Until       string `json:"b"`
	StartedAt   string `json:"t"`
	ID          string `json:"i"`
	Checksum    string `json:"c"`
}

func handleListDeployments(ctx context.Context, config Config, input ListDeploymentsInput) (*mcp.CallToolResult, ListDeploymentsOutput, error) {
	if err := validateListInput(input); err != nil {
		return nil, ListDeploymentsOutput{}, publicError(err)
	}
	ctx, cancel := context.WithTimeout(ctx, maxCallDuration)
	defer cancel()
	generatedAt := config.Now().UTC()
	query, filters, err := listQueryFrom(input, generatedAt)
	if err != nil {
		return nil, ListDeploymentsOutput{}, publicError(err)
	}
	reader, closeReader, err := config.OpenReader(ctx)
	if err != nil {
		return nil, ListDeploymentsOutput{}, publicError(err)
	}
	if reader == nil || closeReader == nil {
		return nil, ListDeploymentsOutput{}, publicError(fmt.Errorf("MCP reader factory returned an invalid snapshot: %w", errs.ErrIncompatible))
	}
	closed := false
	defer func() {
		if !closed {
			_ = closeReader()
		}
	}()
	result, err := reader.DiscoverDeployments(ctx, query)
	if err != nil {
		return nil, ListDeploymentsOutput{}, publicError(err)
	}
	if err := closeReader(); err != nil {
		return nil, ListDeploymentsOutput{}, publicError(err)
	}
	closed = true
	output, err := listOutputFrom(result, filters, generatedAt)
	if err != nil {
		return nil, ListDeploymentsOutput{}, publicError(err)
	}
	if result.HasMore {
		if result.LastCursor.ID == "" || result.LastCursor.StartedAt.IsZero() {
			return nil, ListDeploymentsOutput{}, publicError(fmt.Errorf("%w: invalid deployment discovery page", errs.ErrIncompatible))
		}
		cursor := encodeListCursor(filters, result.LastCursor)
		output.NextCursor = &cursor
		output.Limitations = append(output.Limitations, "additional deployments are available through the opaque cursor")
	}
	data, err := json.Marshal(output)
	if err != nil || len(data) > maxResponseBytes {
		return nil, ListDeploymentsOutput{}, publicError(fmt.Errorf("%w: response exceeds maximum size", errs.ErrIncompatible))
	}
	return nil, output, nil
}

func validateListInput(input ListDeploymentsInput) error {
	if input.Limit != nil && (*input.Limit < 1 || *input.Limit > deployment.DiscoveryMaxLimit) {
		return fmt.Errorf("%w: invalid limit", errs.ErrInvalid)
	}
	if _, err := validatedOptionalEnvironment(input.Environment); err != nil {
		return err
	}
	if _, err := validatedOptionalService(input.Service); err != nil {
		return err
	}
	if input.Status != nil && !deployment.ValidStatus(string(*input.Status)) {
		return fmt.Errorf("%w: invalid status", errs.ErrInvalid)
	}
	var since, until time.Time
	var err error
	if input.Since != nil {
		since, err = parseMCPTime(*input.Since)
		if err != nil {
			return err
		}
	}
	if input.Until != nil {
		until, err = parseMCPTime(*input.Until)
		if err != nil {
			return err
		}
	}
	if !since.IsZero() && !until.IsZero() && (!until.After(since) || until.Sub(since) > 90*24*time.Hour) {
		return fmt.Errorf("%w: invalid time interval", errs.ErrInvalid)
	}
	if input.Cursor != nil {
		if *input.Cursor == "" || len(*input.Cursor) > maxCursorBytes {
			return fmt.Errorf("%w: invalid cursor", errs.ErrInvalid)
		}
		if _, err := decodeListCursor(*input.Cursor); err != nil {
			return err
		}
	}
	return nil
}

func listQueryFrom(input ListDeploymentsInput, generatedAt time.Time) (deployment.DiscoveryQuery, discoveryFiltersOutput, error) {
	if generatedAt.IsZero() {
		return deployment.DiscoveryQuery{}, discoveryFiltersOutput{}, fmt.Errorf("%w: invalid generated timestamp", errs.ErrInvalid)
	}
	filters := discoveryFiltersOutput{Limit: deployment.DiscoveryDefaultLimit}
	if input.Limit != nil {
		filters.Limit = *input.Limit
	}
	if filters.Limit < 1 || filters.Limit > deployment.DiscoveryMaxLimit {
		return deployment.DiscoveryQuery{}, discoveryFiltersOutput{}, fmt.Errorf("%w: invalid limit", errs.ErrInvalid)
	}
	if input.Cursor != nil {
		if *input.Cursor == "" || len(*input.Cursor) > maxCursorBytes {
			return deployment.DiscoveryQuery{}, discoveryFiltersOutput{}, fmt.Errorf("%w: invalid cursor", errs.ErrInvalid)
		}
		cursor, err := decodeListCursor(*input.Cursor)
		if err != nil {
			return deployment.DiscoveryQuery{}, discoveryFiltersOutput{}, err
		}
		filters = discoveryFiltersOutput{Environment: stringPointer(cursor.Environment), Service: stringPointer(cursor.Service), Status: stringPointer(cursor.Status), Since: cursor.Since, Until: cursor.Until, Limit: filters.Limit}
		if cursor.Environment == "" {
			filters.Environment = nil
		}
		if cursor.Service == "" {
			filters.Service = nil
		}
		if cursor.Status == "" {
			filters.Status = nil
		}
		if err := sameOptionalString(input.Environment, filters.Environment); err != nil || sameOptionalString(input.Service, filters.Service) != nil || sameOptionalStatus(input.Status, filters.Status) != nil || sameOptionalTime(input.Since, filters.Since) != nil || sameOptionalTime(input.Until, filters.Until) != nil {
			return deployment.DiscoveryQuery{}, discoveryFiltersOutput{}, fmt.Errorf("%w: cursor filters do not match request", errs.ErrInvalid)
		}
		since, _ := time.Parse(time.RFC3339Nano, cursor.Since)
		until, _ := time.Parse(time.RFC3339Nano, cursor.Until)
		started, _ := time.Parse(time.RFC3339Nano, cursor.StartedAt)
		return deployment.DiscoveryQuery{Environment: cursor.Environment, Service: cursor.Service, Status: cursor.Status, Since: since.UTC(), Until: until.UTC(), Limit: filters.Limit, CursorStartedAt: started.UTC(), CursorID: cursor.ID}, filters, nil
	}
	var err error
	if filters.Environment, err = validatedOptionalEnvironment(input.Environment); err != nil {
		return deployment.DiscoveryQuery{}, discoveryFiltersOutput{}, err
	}
	if filters.Service, err = validatedOptionalService(input.Service); err != nil {
		return deployment.DiscoveryQuery{}, discoveryFiltersOutput{}, err
	}
	if input.Status != nil {
		value := string(*input.Status)
		if !deployment.ValidStatus(value) {
			return deployment.DiscoveryQuery{}, discoveryFiltersOutput{}, fmt.Errorf("%w: invalid status", errs.ErrInvalid)
		}
		filters.Status = new(value)
	}
	until := generatedAt
	if input.Until != nil {
		until, err = parseMCPTime(*input.Until)
		if err != nil {
			return deployment.DiscoveryQuery{}, discoveryFiltersOutput{}, err
		}
	}
	since := until.Add(-24 * time.Hour)
	if input.Since != nil {
		since, err = parseMCPTime(*input.Since)
		if err != nil {
			return deployment.DiscoveryQuery{}, discoveryFiltersOutput{}, err
		}
	}
	if !until.After(since) || until.Sub(since) > 90*24*time.Hour {
		return deployment.DiscoveryQuery{}, discoveryFiltersOutput{}, fmt.Errorf("%w: invalid time interval", errs.ErrInvalid)
	}
	filters.Since, filters.Until = since.Format(time.RFC3339Nano), until.Format(time.RFC3339Nano)
	return deployment.DiscoveryQuery{Environment: valueOrEmpty(filters.Environment), Service: valueOrEmpty(filters.Service), Status: valueOrEmpty(filters.Status), Since: since, Until: until, Limit: filters.Limit}, filters, nil
}

func validatedOptionalEnvironment(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	validated, err := identity.ValidEnvironment(*value)
	if err != nil || validated != *value || redaction.ValidatePublicValue(redaction.Identifier, validated) != nil {
		return nil, fmt.Errorf("%w: invalid environment", errs.ErrInvalid)
	}
	return new(validated), nil
}
func validatedOptionalService(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	if *value == "" || strings.TrimSpace(*value) != *value || len(*value) > 255 || redaction.ValidatePublicValue(redaction.Identifier, *value) != nil {
		return nil, fmt.Errorf("%w: invalid service", errs.ErrInvalid)
	}
	return new(*value), nil
}
func parseMCPTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: invalid timestamp", errs.ErrInvalid)
	}
	return parsed.UTC(), nil
}
func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func sameOptionalString(got, want *string) error {
	if got == nil {
		return nil
	}
	if want == nil || *got != *want {
		return errs.ErrInvalid
	}
	return nil
}
func sameOptionalStatus(got *deploymentStatus, want *string) error {
	if got == nil {
		return nil
	}
	if got == nil || want == nil || string(*got) != *want {
		return errs.ErrInvalid
	}
	return nil
}
func sameOptionalTime(got *string, want string) error {
	if got == nil {
		return nil
	}
	parsed, err := parseMCPTime(*got)
	if err != nil || parsed.Format(time.RFC3339Nano) != want {
		return errs.ErrInvalid
	}
	return nil
}
func stringPointer(value string) *string { return new(value) }

func encodeListCursor(filters discoveryFiltersOutput, item deployment.DiscoveryItem) string {
	cursor := listCursor{Version: 1, Environment: valueOrEmpty(filters.Environment), Service: valueOrEmpty(filters.Service), Status: valueOrEmpty(filters.Status), Since: filters.Since, Until: filters.Until, StartedAt: item.StartedAt.UTC().Format(time.RFC3339Nano), ID: item.ID}
	cursor.Checksum = cursorChecksum(cursor)
	data, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(data)
}
func decodeListCursor(value string) (listCursor, error) {
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(data) == 0 || len(data) > maxCursorBytes {
		return listCursor{}, fmt.Errorf("%w: malformed cursor", errs.ErrInvalid)
	}
	var cursor listCursor
	if json.Unmarshal(data, &cursor) != nil || cursor.Version != 1 || cursor.ID == "" || cursor.Checksum != cursorChecksum(cursor) {
		return listCursor{}, fmt.Errorf("%w: malformed cursor", errs.ErrInvalid)
	}
	for _, value := range []string{cursor.Since, cursor.Until, cursor.StartedAt} {
		if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
			return listCursor{}, fmt.Errorf("%w: malformed cursor", errs.ErrInvalid)
		}
	}
	since, _ := time.Parse(time.RFC3339Nano, cursor.Since)
	until, _ := time.Parse(time.RFC3339Nano, cursor.Until)
	if !until.After(since) || until.Sub(since) > 90*24*time.Hour {
		return listCursor{}, fmt.Errorf("%w: malformed cursor", errs.ErrInvalid)
	}
	if cursor.Environment != "" {
		environment, err := identity.ValidEnvironment(cursor.Environment)
		if err != nil || environment != cursor.Environment {
			return listCursor{}, fmt.Errorf("%w: malformed cursor", errs.ErrInvalid)
		}
	}
	if cursor.Service != "" {
		if _, err := validatedOptionalService(stringPointer(cursor.Service)); err != nil {
			return listCursor{}, fmt.Errorf("%w: malformed cursor", errs.ErrInvalid)
		}
	}
	if cursor.Status != "" && !deployment.ValidStatus(cursor.Status) {
		return listCursor{}, fmt.Errorf("%w: malformed cursor", errs.ErrInvalid)
	}
	return cursor, nil
}
func cursorChecksum(cursor listCursor) string {
	cursor.Checksum = ""
	data, _ := json.Marshal(cursor)
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest[:])
}

func listOutputFrom(result deployment.DiscoveryResult, filters discoveryFiltersOutput, generatedAt time.Time) (ListDeploymentsOutput, error) {
	output := ListDeploymentsOutput{SchemaVersion: DiscoveryVersion, GeneratedAt: generatedAt.UTC().Format(time.RFC3339Nano), Filters: filters, Items: make([]discoveryItemOutput, 0, len(result.Items)), Limitations: []string{}}
	for _, item := range result.Items {
		values := []string{item.ID, item.Environment, item.Service, item.Status, item.Strategy, item.Provenance.Status, item.Provenance.Confidence, item.Provenance.Basis}
		for _, value := range values {
			if redaction.ValidatePublicValue(redaction.FreeText, value) != nil {
				return ListDeploymentsOutput{}, fmt.Errorf("%w: unsafe deployment discovery output", errs.ErrIncompatible)
			}
		}
		limitations := append([]string{}, item.Provenance.Limitations...)
		for _, limitation := range limitations {
			if redaction.ValidatePublicValue(redaction.FreeText, limitation) != nil {
				return ListDeploymentsOutput{}, fmt.Errorf("%w: unsafe deployment discovery output", errs.ErrIncompatible)
			}
		}
		output.Items = append(output.Items, discoveryItemOutput{DeploymentID: item.ID, Environment: item.Environment, Service: item.Service, StartedAt: item.StartedAt.UTC().Format(time.RFC3339Nano), Status: item.Status, Strategy: item.Strategy, Provenance: discoveryProvenanceOutput{Status: item.Provenance.Status, Confidence: discoveryConfidenceOutput{Level: item.Provenance.Confidence, Basis: item.Provenance.Basis}, Limitations: limitations}, CausalityClaimed: false})
	}
	return output, nil
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
