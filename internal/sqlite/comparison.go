package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/guijoazeiro/prodmap/internal/baseline"
	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/regression"
)

// Comparison reads a deployment and the exact service-level telemetry windows
// on both sides. It intentionally creates no analytical persistence.
func (s *Store) Comparison(ctx context.Context, query regression.Query) (regression.Input, error) {
	if err := ctx.Err(); err != nil {
		return regression.Input{}, err
	}
	if err := regression.ValidateQuery(query); err != nil {
		return regression.Input{}, err
	}
	input := regression.Input{Query: query, Before: []baseline.Window{}, After: []baseline.Window{}, Contamination: regression.Contamination{BeforeDeployments: []string{}, AfterDeployments: []string{}, ConcurrentDeployments: []string{}}}
	var started string
	err := s.db.QueryRowContext(ctx, `SELECT id,external_id,environment,service_key,started_at FROM deployments WHERE id=?`, query.DeploymentID).Scan(&input.Deployment.ID, &input.Deployment.ExternalID, &input.Deployment.Environment, &input.Deployment.Service, &started)
	if errors.Is(err, sql.ErrNoRows) {
		return regression.Input{}, fmt.Errorf("%w: deployment was not found", errs.ErrNotFound)
	}
	if err != nil {
		return regression.Input{}, fmt.Errorf("%w: resolve deployment: %w", errs.ErrUnavailable, err)
	}
	input.Deployment.StartedAt, err = parseTime(started)
	if err != nil {
		return regression.Input{}, fmt.Errorf("%w: decode deployment timestamp: %w", errs.ErrIncompatible, err)
	}
	input.Target = baseline.Target{Kind: baseline.ServiceTarget, Service: input.Deployment.Service}
	err = s.db.QueryRowContext(ctx, `SELECT id FROM services WHERE environment=? AND logical_key=?`, input.Deployment.Environment, input.Deployment.Service).Scan(&input.Target.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return regression.Input{}, fmt.Errorf("%w: resolve comparison service: %w", errs.ErrUnavailable, err)
	}
	beforeStart := input.Deployment.StartedAt.Add(-query.Before)
	afterEnd := input.Deployment.StartedAt.Add(query.After)
	input.Before, err = s.comparisonWindows(ctx, input.Deployment.Environment, input.Deployment.Service, beforeStart, input.Deployment.StartedAt)
	if err != nil {
		return regression.Input{}, err
	}
	input.After, err = s.comparisonWindows(ctx, input.Deployment.Environment, input.Deployment.Service, input.Deployment.StartedAt, afterEnd)
	if err != nil {
		return regression.Input{}, err
	}
	input.Contamination, err = s.comparisonContamination(ctx, input.Deployment, beforeStart, afterEnd)
	if err != nil {
		return regression.Input{}, err
	}
	return input, nil
}

func (s *Store) comparisonWindows(ctx context.Context, environment, service string, start, end time.Time) ([]baseline.Window, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT w.id,w.window_start,w.window_end,w.observed_at,w.request_count,w.error_count,w.p50_ns,w.p95_ns,w.p99_ns,w.coverage_ratio,w.is_complete FROM telemetry_windows w JOIN services s ON s.id=w.service_id WHERE s.environment=? AND s.logical_key=? AND w.endpoint_id IS NULL AND w.window_start=? AND w.window_end=? ORDER BY w.id LIMIT ?`, environment, service, formatTime(start), formatTime(end), baseline.MaxCandidates+1)
	if err != nil {
		return nil, fmt.Errorf("%w: query exact comparison window: %w", errs.ErrUnavailable, err)
	}
	defer rows.Close()
	windows := []baseline.Window{}
	for rows.Next() {
		var window baseline.Window
		var startValue, endValue, observed string
		var coverage sql.NullFloat64
		var complete int
		if err := rows.Scan(&window.ID, &startValue, &endValue, &observed, &window.RequestCount, &window.ErrorCount, &window.P50NS, &window.P95NS, &window.P99NS, &coverage, &complete); err != nil {
			return nil, fmt.Errorf("%w: scan comparison window: %w", errs.ErrIncompatible, err)
		}
		var parseErr error
		window.Start, parseErr = parseTime(startValue)
		if parseErr == nil {
			window.End, parseErr = parseTime(endValue)
		}
		if parseErr == nil {
			window.ObservedAt, parseErr = parseTime(observed)
		}
		if parseErr != nil {
			return nil, fmt.Errorf("%w: decode comparison window: %w", errs.ErrIncompatible, parseErr)
		}
		if coverage.Valid {
			window.CoverageRatio = new(coverage.Float64)
		}
		window.IsComplete = complete != 0
		windows = append(windows, window)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate comparison windows: %w", errs.ErrUnavailable, err)
	}
	return windows, nil
}

func (s *Store) comparisonContamination(ctx context.Context, selected regression.Deployment, beforeStart, afterEnd time.Time) (regression.Contamination, error) {
	result := regression.Contamination{BeforeDeployments: []string{}, AfterDeployments: []string{}, ConcurrentDeployments: []string{}}
	rows, err := s.db.QueryContext(ctx, `SELECT id,started_at FROM deployments WHERE environment=? AND service_key=? AND id<>? AND started_at>=? AND started_at<? ORDER BY started_at,id LIMIT ?`, selected.Environment, selected.Service, selected.ID, formatTime(beforeStart), formatTime(afterEnd), regression.MaxContaminatingDeployments+1)
	if err != nil {
		return result, fmt.Errorf("%w: query comparison contamination: %w", errs.ErrUnavailable, err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, value string
		if err := rows.Scan(&id, &value); err != nil {
			return result, fmt.Errorf("%w: scan comparison contamination: %w", errs.ErrIncompatible, err)
		}
		count++
		if count > regression.MaxContaminatingDeployments {
			result.Truncated = true
			continue
		}
		startedAt, parseErr := parseTime(value)
		if parseErr != nil {
			return result, fmt.Errorf("%w: decode comparison contamination: %w", errs.ErrIncompatible, parseErr)
		}
		switch {
		case startedAt.Equal(selected.StartedAt):
			result.ConcurrentDeployments = append(result.ConcurrentDeployments, id)
		case startedAt.Before(selected.StartedAt):
			result.BeforeDeployments = append(result.BeforeDeployments, id)
		default:
			result.AfterDeployments = append(result.AfterDeployments, id)
		}
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("%w: iterate comparison contamination: %w", errs.ErrUnavailable, err)
	}
	return result, nil
}

var _ regression.Reader = (*Store)(nil)
