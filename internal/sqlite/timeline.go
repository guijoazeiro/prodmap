package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/identity"
	"github.com/guijoazeiro/prodmap/internal/timeline"
)

type timelineCursor struct {
	Version, Priority   int
	Time, ID, Signature string
}

func (s *Store) Timeline(ctx context.Context, query timeline.Query) (timeline.Result, error) {
	if query.Limit < 1 || query.Limit > 1000 || !query.Until.After(query.Since) {
		return timeline.Result{}, fmt.Errorf("%w: invalid timeline query", errs.ErrInvalid)
	}
	environment, err := identity.ValidEnvironment(query.Environment)
	if err != nil || environment != query.Environment {
		return timeline.Result{}, fmt.Errorf("%w: invalid timeline environment", errs.ErrInvalid)
	}
	cursor, err := decodeTimelineCursor(query)
	if err != nil {
		return timeline.Result{}, err
	}
	result := timeline.Result{Since: query.Since.UTC(), Until: query.Until.UTC(), Environment: environment, Items: []timeline.Event{}}
	statement := `WITH deployment_concurrency AS (
SELECT environment,service_key,started_at,COUNT(*) AS event_count FROM deployments GROUP BY environment,service_key,started_at
), events AS (
SELECT 'evt_deployment_' || d.id AS event_id,d.started_at AS event_time,CASE WHEN d.status='rolled_back' THEN 10 ELSE 20 END AS priority,
d.status,d.environment,d.service_key AS service,d.id AS subject_id,d.external_id AS subject_name,d.observed_at AS observed_at,d.provenance_status,d.confidence_level,d.confidence_basis,d.strategy,
NULL AS runtime_state,NULL AS runtime_health,NULL AS runtime_restarts,NULL AS runtime_artifact,COALESCE(c.event_count,1) AS concurrent_count
FROM deployments d LEFT JOIN deployment_concurrency c ON c.environment=d.environment AND c.service_key=d.service_key AND c.started_at=d.started_at
WHERE d.environment=? AND d.started_at>=? AND d.started_at<? AND (?='' OR d.service_key=?)
UNION ALL
SELECT 'evt_runtime_' || r.id,r.observed_at,30,NULL,s.environment,s.logical_key AS service,r.id,s.logical_key,r.observed_at,NULL,'HIGH','validated Docker runtime snapshot',NULL,
r.state,r.health,r.restart_count,a.identity,0
FROM runtime_instances r JOIN services s ON s.id=r.service_id JOIN artifacts a ON a.id=r.artifact_id
WHERE s.environment=? AND r.observed_at>=? AND r.observed_at<? AND (?='' OR s.logical_key=?)
)
SELECT event_id,event_time,priority,status,environment,service,subject_id,subject_name,observed_at,provenance_status,confidence_level,confidence_basis,strategy,runtime_state,runtime_health,runtime_restarts,runtime_artifact,concurrent_count
FROM events WHERE (event_time<? OR (event_time=? AND (priority>? OR (priority=? AND event_id>?))))
ORDER BY event_time DESC,priority ASC,event_id ASC LIMIT ?`
	args := []any{environment, formatTime(query.Since), formatTime(query.Until), query.Service, query.Service, environment, formatTime(query.Since), formatTime(query.Until), query.Service, query.Service, cursor.Time, cursor.Time, cursor.Priority, cursor.Priority, cursor.ID, query.Limit + 1}
	rows, err := s.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return result, fmt.Errorf("%w: query timeline: %w", errs.ErrUnavailable, err)
	}
	defer rows.Close()
	for rows.Next() {
		var event timeline.Event
		var eventTime, status, observedAt, provenanceStatus, level, basis, strategy sql.NullString
		var runtimeState, runtimeHealth, runtimeArtifact sql.NullString
		var runtimeRestarts, concurrentCount sql.NullInt64
		var priority int
		if err := rows.Scan(&event.ID, &eventTime, &priority, &status, &event.Environment, &event.Service, &event.Subject.ID, &event.Subject.Name, &observedAt, &provenanceStatus, &level, &basis, &strategy, &runtimeState, &runtimeHealth, &runtimeRestarts, &runtimeArtifact, &concurrentCount); err != nil {
			return result, fmt.Errorf("%w: scan timeline event: %w", errs.ErrIncompatible, err)
		}
		var parseErr error
		event.Time, parseErr = parseTime(eventTime.String)
		if parseErr != nil {
			return result, parseErr
		}
		event.Source.ObservedAt, parseErr = parseTime(observedAt.String)
		if parseErr != nil {
			return result, parseErr
		}
		event.Limitations = []string{}
		event.CausalityClaimed = false
		if status.Valid {
			event.Kind = timeline.DeploymentKind(status.String)
			event.Subject.Type = "deployment"
			event.Source.Kind = "deployment_ledger"
			event.RelationType = "DECLARED"
			event.Confidence = timeline.Confidence{Level: "HIGH", Basis: "validated deployment ledger record"}
			event.Deployment = &timeline.Deployment{Status: status.String, Strategy: strategy.String, ProvenanceStatus: provenanceStatus.String, ProvenanceConfidence: timeline.Confidence{Level: level.String, Basis: basis.String}}
			event.Concurrency = timeline.Concurrency{Detected: concurrentCount.Int64 > 1, Count: int(concurrentCount.Int64)}
			if event.Kind == "rollback_declared" {
				event.Limitations = append(event.Limitations, "declared rollback does not prove runtime effect")
			}
			if event.Concurrency.Detected {
				event.Limitations = append(event.Limitations, "concurrent deployments prevent exclusive runtime association")
			}
		} else {
			event.Kind = "runtime_observed"
			event.Subject.Type = "runtime_instance"
			event.Source.Kind = "docker"
			event.RelationType = "OBSERVED"
			event.Confidence = timeline.Confidence{Level: "HIGH", Basis: "validated Docker runtime snapshot"}
			event.Runtime = &timeline.Runtime{State: runtimeState.String, Health: runtimeHealth.String, RestartCount: int(runtimeRestarts.Int64), ArtifactIdentity: runtimeArtifact.String}
			event.Concurrency = timeline.Concurrency{Detected: false, Count: 0}
		}
		result.Items = append(result.Items, event)
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("%w: iterate timeline events: %w", errs.ErrUnavailable, err)
	}
	if len(result.Items) > query.Limit {
		result.Items = result.Items[:query.Limit]
		last := result.Items[len(result.Items)-1]
		result.NextCursor = encodeTimelineCursor(query, last)
	}
	return result, nil
}

func decodeTimelineCursor(query timeline.Query) (timelineCursor, error) {
	cursor := timelineCursor{Time: "9999-12-31T23:59:59.999999999Z", Priority: -1, ID: ""}
	if query.Cursor == "" {
		return cursor, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(query.Cursor)
	if err != nil {
		return cursor, fmt.Errorf("%w: malformed timeline cursor", errs.ErrInvalid)
	}
	if err := json.Unmarshal(raw, &cursor); err != nil || cursor.Version != 1 || cursor.Signature != timelineSignature(query) {
		return cursor, fmt.Errorf("%w: incompatible timeline cursor", errs.ErrInvalid)
	}
	if _, err := parseTime(cursor.Time); err != nil || cursor.ID == "" || cursor.Priority < 0 {
		return cursor, fmt.Errorf("%w: malformed timeline cursor", errs.ErrInvalid)
	}
	return cursor, nil
}
func timelineSignature(query timeline.Query) string {
	value := strings.Join([]string{query.Environment, query.Service, formatTime(query.Since), formatTime(query.Until)}, "\x00")
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:])
}
func encodeTimelineCursor(query timeline.Query, event timeline.Event) string {
	raw, _ := json.Marshal(timelineCursor{Version: 1, Time: formatTime(event.Time), Priority: timeline.Priority(event.Kind), ID: event.ID, Signature: timelineSignature(query)})
	return base64.RawURLEncoding.EncodeToString(raw)
}
