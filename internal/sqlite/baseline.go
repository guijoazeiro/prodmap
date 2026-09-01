package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/guijoazeiro/prodmap/internal/baseline"
	"github.com/guijoazeiro/prodmap/internal/errs"
)

// Baseline resolves one target and reads only exact previous telemetry windows.
// It is intentionally read-only: baseline conclusions are not persisted.
func (s *Store) Baseline(ctx context.Context, query baseline.Query) (baseline.Input, error) {
	if err := ctx.Err(); err != nil {
		return baseline.Input{}, err
	}
	input := baseline.Input{Query: query, Candidates: []baseline.Window{}}
	var serviceID string
	if strings.TrimSpace(query.ServiceKey) != "" && strings.TrimSpace(query.EndpointID) == "" {
		input.Target.Kind = baseline.ServiceTarget
		if err := s.db.QueryRowContext(ctx, `SELECT id,logical_key FROM services WHERE environment=? AND logical_key=?`, query.Environment, strings.TrimSpace(query.ServiceKey)).Scan(&serviceID, &input.Target.Service); err != nil {
			return baseline.Input{}, baselineTargetError("service", err)
		}
		input.Target.ID = serviceID
	} else if strings.TrimSpace(query.EndpointID) != "" && strings.TrimSpace(query.ServiceKey) == "" {
		input.Target.Kind = baseline.EndpointTarget
		if err := s.db.QueryRowContext(ctx, `SELECT e.id,s.logical_key,e.protocol,e.operation FROM endpoints e JOIN services s ON s.id=e.service_id WHERE e.id=? AND s.environment=?`, query.EndpointID, query.Environment).Scan(&input.Target.ID, &input.Target.Service, &input.Target.Protocol, &input.Target.Operation); err != nil {
			return baseline.Input{}, baselineTargetError("endpoint", err)
		}
		serviceID = ""
	} else {
		return baseline.Input{}, fmt.Errorf("%w: exactly one baseline target is required", errs.ErrInvalid)
	}

	start := query.At.Add(-query.Window)
	statement := `SELECT w.id,w.window_start,w.window_end,w.observed_at,w.request_count,w.error_count,w.p50_ns,w.p95_ns,w.p99_ns,w.coverage_ratio,w.is_complete,
		EXISTS(SELECT 1 FROM deployments d WHERE d.environment=s.environment AND d.service_key=s.logical_key AND d.started_at>=w.window_start AND d.started_at<w.window_end)
		FROM telemetry_windows w JOIN services s ON s.id=w.service_id
		WHERE s.environment=? AND w.window_start=? AND w.window_end=?`
	args := []any{query.Environment, formatTime(start), formatTime(query.At)}
	if input.Target.Kind == baseline.ServiceTarget {
		statement += ` AND w.service_id=? AND w.endpoint_id IS NULL`
		args = append(args, serviceID)
	} else {
		statement += ` AND w.endpoint_id=?`
		args = append(args, input.Target.ID)
	}
	statement += ` ORDER BY w.id LIMIT ?`
	args = append(args, baseline.MaxCandidates+1)
	rows, err := s.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return baseline.Input{}, fmt.Errorf("%w: query exact baseline window: %w", errs.ErrUnavailable, err)
	}
	defer rows.Close()
	for rows.Next() {
		var window baseline.Window
		var startValue, endValue, observed string
		var coverage sql.NullFloat64
		var complete, contaminated int
		if err := rows.Scan(&window.ID, &startValue, &endValue, &observed, &window.RequestCount, &window.ErrorCount, &window.P50NS, &window.P95NS, &window.P99NS, &coverage, &complete, &contaminated); err != nil {
			return baseline.Input{}, fmt.Errorf("%w: scan exact baseline window: %w", errs.ErrIncompatible, err)
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
			return baseline.Input{}, fmt.Errorf("%w: decode exact baseline window: %w", errs.ErrIncompatible, parseErr)
		}
		if coverage.Valid {
			window.CoverageRatio = new(coverage.Float64)
		}
		window.IsComplete = complete != 0
		window.Contaminated = contaminated != 0
		input.Candidates = append(input.Candidates, window)
	}
	if err := rows.Err(); err != nil {
		return baseline.Input{}, fmt.Errorf("%w: iterate exact baseline windows: %w", errs.ErrUnavailable, err)
	}
	return input, nil
}

func baselineTargetError(kind string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: baseline %s was not found", errs.ErrNotFound, kind)
	}
	return fmt.Errorf("%w: resolve baseline %s: %w", errs.ErrUnavailable, kind, err)
}
