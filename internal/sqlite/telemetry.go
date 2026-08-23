package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/identity"
	"github.com/guijoazeiro/prodmap/internal/telemetry"
	"github.com/guijoazeiro/prodmap/internal/topology"
)

func (s *Store) SaveTelemetry(ctx context.Context, snapshot telemetry.Snapshot) (telemetry.IngestResult, error) {
	if err := validateTelemetrySnapshot(snapshot); err != nil {
		return telemetry.IngestResult{}, err
	}
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		result, err := s.saveTelemetry(ctx, snapshot)
		if err == nil || !isSQLiteBusy(err) {
			return result, err
		}
		lastErr = err
		timer := time.NewTimer(time.Duration(attempt+1) * 10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return telemetry.IngestResult{}, ctx.Err()
		case <-timer.C:
		}
	}
	return telemetry.IngestResult{}, lastErr
}

func validateTelemetrySnapshot(snapshot telemetry.Snapshot) error {
	invalid := func(message string) error { return fmt.Errorf("%w: %s", errs.ErrInvalid, message) }
	if !validTelemetryText(snapshot.SourceKey, 512) || !validSHA256(snapshot.SourceHash) || snapshot.ObservedAt.IsZero() || snapshot.WindowStart.IsZero() || !snapshot.WindowEnd.After(snapshot.WindowStart) {
		return invalid("incomplete telemetry snapshot identity")
	}
	environment, err := telemetry.ValidEnvironment(snapshot.Environment)
	if err != nil || environment != snapshot.Environment {
		return invalid("invalid telemetry environment")
	}
	if snapshot.Stats.Lines < 0 || snapshot.Stats.ResourceSpans < 0 || snapshot.Stats.SpansSeen < 0 || snapshot.Stats.SpansAccepted < 0 || snapshot.Stats.SpansIgnored < 0 {
		return invalid("telemetry counters must be non-negative")
	}
	if snapshot.Stats.SpansAccepted+snapshot.Stats.SpansIgnored != snapshot.Stats.SpansSeen {
		return invalid("telemetry span counters are inconsistent")
	}
	if snapshot.Stats.Lines > telemetry.MaxLines || snapshot.Stats.SpansSeen > telemetry.MaxSpans || snapshot.Stats.ResourceSpans > telemetry.MaxSpans {
		return invalid("telemetry counters exceed ingestion limits")
	}
	for _, warning := range snapshot.Warnings {
		if !validTelemetryText(warning, 2048) {
			return invalid("telemetry warning is invalid")
		}
	}
	if len(snapshot.Services) > telemetry.MaxServices || len(snapshot.Endpoints) > telemetry.MaxEndpoints || len(snapshot.Dependencies) > telemetry.MaxDependencies || len(snapshot.Observations) > telemetry.MaxSpans || len(snapshot.Windows) > telemetry.MaxSpans {
		return invalid("telemetry snapshot exceeds cardinality limits")
	}
	if snapshot.Stats.Services != len(snapshot.Services) || snapshot.Stats.Endpoints != len(snapshot.Endpoints) || snapshot.Stats.Dependencies != len(snapshot.Dependencies) || snapshot.Stats.Observations != len(snapshot.Observations) || snapshot.Stats.TelemetryWindows != len(snapshot.Windows) {
		return invalid("telemetry summary counts do not match normalized records")
	}
	services := make(map[string]struct{}, len(snapshot.Services))
	for _, service := range snapshot.Services {
		if !validTelemetryText(service.LogicalKey, 1024) || !validTelemetryText(service.DisplayName, 1024) {
			return invalid("service identity is incomplete")
		}
		if _, duplicate := services[service.LogicalKey]; duplicate {
			return invalid("duplicate service identity")
		}
		services[service.LogicalKey] = struct{}{}
	}
	type endpointReference struct {
		serviceKey string
		identity   string
	}
	endpoints := make(map[string]endpointReference, len(snapshot.Endpoints))
	endpointIdentities := make(map[string]struct{}, len(snapshot.Endpoints))
	for _, endpoint := range snapshot.Endpoints {
		if !validInternalKey(endpoint.Key, 2048) || !validTelemetryText(endpoint.Operation, 1024) || (endpoint.RouteTemplate != "" && !validTelemetryText(endpoint.RouteTemplate, 1024)) || (endpoint.Protocol != "http" && endpoint.Protocol != "grpc" && endpoint.Protocol != "rpc") {
			return invalid("endpoint identity is incomplete")
		}
		if _, ok := services[endpoint.ServiceKey]; !ok {
			return invalid("endpoint references an unknown service")
		}
		if _, duplicate := endpoints[endpoint.Key]; duplicate {
			return invalid("duplicate endpoint identity")
		}
		identity := endpoint.ServiceKey + "\x00" + endpoint.Protocol + "\x00" + endpoint.Operation
		if _, duplicate := endpointIdentities[identity]; duplicate {
			return invalid("duplicate endpoint persistence identity")
		}
		endpointIdentities[identity] = struct{}{}
		endpoints[endpoint.Key] = endpointReference{serviceKey: endpoint.ServiceKey, identity: identity}
	}
	type dependencyReference struct {
		identity   string
		kind       string
		logicalKey string
	}
	dependencies := make(map[string]dependencyReference, len(snapshot.Dependencies))
	dependencyIdentities := make(map[string]struct{}, len(snapshot.Dependencies))
	for _, dependency := range snapshot.Dependencies {
		if !validInternalKey(dependency.Key, 2048) || !validTelemetryText(dependency.LogicalKey, 1024) || !validTelemetryText(dependency.DisplayName, 1024) || !validDependencyKind(dependency.Kind) {
			return invalid("dependency identity is incomplete")
		}
		if _, duplicate := dependencies[dependency.Key]; duplicate {
			return invalid("duplicate dependency identity")
		}
		identity := dependency.Kind + "\x00" + dependency.LogicalKey
		if _, duplicate := dependencyIdentities[identity]; duplicate {
			return invalid("duplicate dependency persistence identity")
		}
		dependencyIdentities[identity] = struct{}{}
		dependencies[dependency.Key] = dependencyReference{identity: identity, kind: dependency.Kind, logicalKey: dependency.LogicalKey}
	}
	evidence := make(map[string]struct{}, len(snapshot.Evidence))
	for _, item := range snapshot.Evidence {
		if !validEvidenceFingerprint(item.Fingerprint) || !validTelemetryText(item.Claim, 512) || !item.ObservedAt.Equal(snapshot.ObservedAt) {
			return invalid("topology evidence is invalid")
		}
		if _, duplicate := evidence[item.Fingerprint]; duplicate {
			return invalid("duplicate topology evidence")
		}
		evidence[item.Fingerprint] = struct{}{}
	}
	observationIdentities := make(map[string]struct{}, len(snapshot.Observations))
	for _, observation := range snapshot.Observations {
		if !validInternalKey(observation.Key, 4096) {
			return invalid("dependency observation identity is invalid")
		}
		if !observation.WindowStart.Equal(snapshot.WindowStart) || !observation.WindowEnd.Equal(snapshot.WindowEnd) {
			return invalid("dependency observation window differs from ingestion window")
		}
		if _, ok := services[observation.FromServiceKey]; !ok {
			return invalid("dependency observation references an unknown source service")
		}
		dependency, ok := dependencies[observation.DependencyKey]
		if !ok {
			return invalid("dependency observation references an unknown dependency")
		}
		if observation.TargetServiceKey != "" {
			if dependency.kind != "service" || observation.TargetServiceKey != dependency.logicalKey {
				return invalid("dependency observation target is inconsistent with its dependency")
			}
			if _, ok := services[observation.TargetServiceKey]; !ok {
				return invalid("dependency observation target references an unknown service")
			}
		}
		endpointIdentity := ""
		if observation.OriginEndpointKey != "" {
			reference, ok := endpoints[observation.OriginEndpointKey]
			if !ok {
				return invalid("dependency observation references an unknown endpoint")
			}
			if reference.serviceKey != observation.FromServiceKey {
				return invalid("dependency observation endpoint belongs to another service")
			}
			endpointIdentity = reference.identity
		}
		identity := observation.FromServiceKey + "\x00" + endpointIdentity + "\x00" + dependency.identity
		if _, duplicate := observationIdentities[identity]; duplicate {
			return invalid("duplicate dependency observation identity")
		}
		observationIdentities[identity] = struct{}{}
		if observation.RequestCount < 0 || observation.ErrorCount < 0 || observation.ErrorCount > observation.RequestCount || observation.DurationSumNS < 0 {
			return invalid("dependency observation counters are invalid")
		}
		if observation.Confidence != topology.Low && observation.Confidence != topology.Medium && observation.Confidence != topology.High {
			return invalid("dependency observation confidence must be LOW, MEDIUM, or HIGH")
		}
		if !validTelemetryText(observation.Basis, 512) || len(observation.EvidenceFingerprints) == 0 || len(observation.EvidenceFingerprints) > telemetry.MaxEvidenceSamples {
			return invalid("dependency observation evidence is invalid")
		}
		if observation.Confidence == topology.High && len(observation.EvidenceFingerprints) < 2 {
			return invalid("HIGH dependency observation requires a linked evidence pair")
		}
		seenEvidence := make(map[string]struct{}, len(observation.EvidenceFingerprints))
		for _, fingerprint := range observation.EvidenceFingerprints {
			if _, ok := evidence[fingerprint]; !ok {
				return invalid("dependency observation references unknown evidence")
			}
			if _, duplicate := seenEvidence[fingerprint]; duplicate {
				return invalid("dependency observation repeats evidence")
			}
			seenEvidence[fingerprint] = struct{}{}
		}
		for _, limitation := range observation.Limitations {
			if !validTelemetryText(limitation, 1024) {
				return invalid("dependency observation limitation is invalid")
			}
		}
	}
	windowIdentities := make(map[string]struct{}, len(snapshot.Windows))
	for _, window := range snapshot.Windows {
		if !validInternalKey(window.Key, 4096) {
			return invalid("telemetry aggregate identity is invalid")
		}
		if !window.WindowStart.Equal(snapshot.WindowStart) || !window.WindowEnd.Equal(snapshot.WindowEnd) {
			return invalid("telemetry aggregate window differs from ingestion window")
		}
		if _, ok := services[window.ServiceKey]; !ok {
			return invalid("telemetry aggregate references an unknown service")
		}
		endpointIdentity := ""
		if window.EndpointKey != "" {
			reference, ok := endpoints[window.EndpointKey]
			if !ok {
				return invalid("telemetry aggregate references an unknown endpoint")
			}
			if reference.serviceKey != window.ServiceKey {
				return invalid("telemetry aggregate endpoint belongs to another service")
			}
			endpointIdentity = reference.identity
		}
		identity := window.ServiceKey + "\x00" + endpointIdentity
		if _, duplicate := windowIdentities[identity]; duplicate {
			return invalid("duplicate telemetry aggregate identity")
		}
		windowIdentities[identity] = struct{}{}
		if window.RequestCount < 0 || window.ErrorCount < 0 || window.ErrorCount > window.RequestCount || window.DurationSumNS < 0 || window.P50NS < 0 || window.P95NS < window.P50NS || window.P99NS < window.P95NS {
			return invalid("telemetry aggregate counters or percentiles are invalid")
		}
		if window.CoverageRatio != nil && (math.IsNaN(*window.CoverageRatio) || math.IsInf(*window.CoverageRatio, 0) || *window.CoverageRatio < 0 || *window.CoverageRatio > 1) {
			return invalid("telemetry aggregate coverage ratio is invalid")
		}
	}
	return nil
}

func validTelemetryText(value string, limit int) bool {
	return value != "" && utf8.ValidString(value) && len([]rune(value)) <= limit && strings.IndexFunc(value, unicode.IsControl) < 0
}

func validInternalKey(value string, limit int) bool {
	return value != "" && utf8.ValidString(value) && len([]rune(value)) <= limit && strings.IndexFunc(value, func(r rune) bool { return r != 0 && unicode.IsControl(r) }) < 0
}

func validSHA256(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func validEvidenceFingerprint(value string) bool {
	return strings.HasPrefix(value, telemetry.EvidenceFingerprintV1+":") && len(value) == len(telemetry.EvidenceFingerprintV1)+1+64 && validSHA256("sha256:"+strings.TrimPrefix(value, telemetry.EvidenceFingerprintV1+":"))
}

func validDependencyKind(value string) bool {
	switch value {
	case "service", "database", "cache", "queue", "external_api", "other":
		return true
	default:
		return false
	}
}

func (s *Store) saveTelemetry(ctx context.Context, snapshot telemetry.Snapshot) (telemetry.IngestResult, error) {
	tx, release, err := s.beginTransaction(ctx)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return telemetry.IngestResult{}, ctxErr
		}
		return telemetry.IngestResult{}, fmt.Errorf("%w: begin telemetry ingestion: %w", errs.ErrUnavailable, err)
	}
	defer release()
	defer tx.Rollback()
	now := s.clockNow()
	var sourceID string
	sourceLookupErr := tx.QueryRowContext(ctx, `SELECT id FROM sources WHERE kind='otel' AND instance_key=?`, snapshot.SourceKey).Scan(&sourceID)
	if sourceLookupErr != nil && !errors.Is(sourceLookupErr, sql.ErrNoRows) {
		return telemetry.IngestResult{}, fmt.Errorf("%w: find OTel source: %w", errs.ErrUnavailable, sourceLookupErr)
	}
	if sourceLookupErr == nil {
		if existing, found, err := existingTelemetryIngestion(ctx, tx, sourceID, snapshot); err != nil {
			return telemetry.IngestResult{}, err
		} else if found {
			if existing.SourceHash != snapshot.SourceHash {
				return telemetry.IngestResult{}, fmt.Errorf("%w: telemetry source/environment/window already contains different content", errs.ErrConflict)
			}
			existing.IdempotentReplay = true
			if err := tx.Commit(); err != nil {
				return telemetry.IngestResult{}, fmt.Errorf("%w: commit telemetry replay: %w", errs.ErrUnavailable, err)
			}
			return existing, nil
		}
	}
	sourceID, err = ensureSource(ctx, tx, "otel", snapshot.SourceKey, "success", snapshot.ObservedAt, now)
	if err != nil {
		return telemetry.IngestResult{}, err
	}

	ingestionID, err := identity.NewV7(now)
	if err != nil {
		return telemetry.IngestResult{}, err
	}
	warningsJSON, err := sanitizedJSON(nonNilTelemetryStrings(snapshot.Warnings), 32768)
	if err != nil {
		return telemetry.IngestResult{}, err
	}
	stamp := formatTime(now)
	_, err = tx.ExecContext(ctx, `INSERT INTO telemetry_ingestions(
		id,source_id,source_hash,environment,window_start,window_end,observed_at,ingested_at,
		lines,resource_spans,spans_seen,spans_accepted,spans_ignored,services_count,endpoints_count,
		dependencies_count,observations_count,telemetry_windows_count,warnings_json,created_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, ingestionID, sourceID, snapshot.SourceHash, snapshot.Environment,
		formatTime(snapshot.WindowStart), formatTime(snapshot.WindowEnd), formatTime(snapshot.ObservedAt), stamp,
		snapshot.Stats.Lines, snapshot.Stats.ResourceSpans, snapshot.Stats.SpansSeen, snapshot.Stats.SpansAccepted,
		snapshot.Stats.SpansIgnored, snapshot.Stats.Services, snapshot.Stats.Endpoints, snapshot.Stats.Dependencies,
		snapshot.Stats.Observations, snapshot.Stats.TelemetryWindows, warningsJSON, stamp)
	if err != nil {
		return telemetry.IngestResult{}, fmt.Errorf("%w: save telemetry ingestion: %w", errs.ErrUnavailable, err)
	}

	serviceIDs := make(map[string]string, len(snapshot.Services))
	for _, service := range snapshot.Services {
		id, err := ensureTelemetryService(ctx, tx, service, snapshot.Environment, snapshot.WindowEnd, now)
		if err != nil {
			return telemetry.IngestResult{}, err
		}
		serviceIDs[service.LogicalKey] = id
	}
	endpointIDs := make(map[string]string, len(snapshot.Endpoints))
	for _, endpoint := range snapshot.Endpoints {
		id, err := ensureTelemetryEndpoint(ctx, tx, endpoint, serviceIDs[endpoint.ServiceKey], snapshot.WindowEnd, now)
		if err != nil {
			return telemetry.IngestResult{}, err
		}
		endpointIDs[endpoint.Key] = id
	}
	dependencyIDs := make(map[string]string, len(snapshot.Dependencies))
	for _, dependency := range snapshot.Dependencies {
		id, err := ensureTelemetryDependency(ctx, tx, dependency, snapshot.Environment, snapshot.WindowEnd, now)
		if err != nil {
			return telemetry.IngestResult{}, err
		}
		dependencyIDs[dependency.Key] = id
	}
	evidenceIDs := make(map[string]string, len(snapshot.Evidence))
	for _, evidence := range snapshot.Evidence {
		id, err := identity.NewV7(now)
		if err != nil {
			return telemetry.IngestResult{}, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO topology_evidence(id,ingestion_id,fingerprint_version,fingerprint,claim,observed_at,created_at) VALUES(?,?,?,?,?,?,?)`, id, ingestionID, telemetry.EvidenceFingerprintV1, evidence.Fingerprint, evidence.Claim, formatTime(evidence.ObservedAt), stamp)
		if err != nil {
			return telemetry.IngestResult{}, fmt.Errorf("%w: save topology evidence: %w", errs.ErrUnavailable, err)
		}
		evidenceIDs[evidence.Fingerprint] = id
	}
	for _, observation := range snapshot.Observations {
		id, err := identity.NewV7(now)
		if err != nil {
			return telemetry.IngestResult{}, err
		}
		ids := make([]string, 0, len(observation.EvidenceFingerprints))
		for _, fingerprint := range observation.EvidenceFingerprints {
			if evidenceID := evidenceIDs[fingerprint]; evidenceID != "" {
				ids = append(ids, evidenceID)
			}
		}
		sort.Strings(ids)
		idsJSON, err := sanitizedJSON(ids, 8192)
		if err != nil {
			return telemetry.IngestResult{}, err
		}
		limitationsJSON, err := sanitizedJSON(nonNilTelemetryStrings(observation.Limitations), 8192)
		if err != nil {
			return telemetry.IngestResult{}, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO service_dependency_observations(
			id,ingestion_id,from_service_id,origin_endpoint_id,dependency_id,target_service_id,window_start,window_end,
			request_count,error_count,duration_sum_ns,relation_type,confidence_level,confidence_basis,
			algorithm_version,evidence_ids_json,limitations_json,observed_at,ingested_at,created_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,'OBSERVED',?,?,?,?,?,?,?,?)`, id, ingestionID, serviceIDs[observation.FromServiceKey],
			nullString(endpointIDs[observation.OriginEndpointKey]), dependencyIDs[observation.DependencyKey],
			nullString(serviceIDs[observation.TargetServiceKey]),
			formatTime(observation.WindowStart), formatTime(observation.WindowEnd), observation.RequestCount,
			observation.ErrorCount, observation.DurationSumNS, observation.Confidence, observation.Basis,
			topology.AlgorithmVersion, idsJSON, limitationsJSON, formatTime(snapshot.ObservedAt), stamp, stamp)
		if err != nil {
			return telemetry.IngestResult{}, fmt.Errorf("%w: save dependency observation: %w", errs.ErrUnavailable, err)
		}
	}
	for _, window := range snapshot.Windows {
		id, err := identity.NewV7(now)
		if err != nil {
			return telemetry.IngestResult{}, err
		}
		complete := 0
		if window.IsComplete {
			complete = 1
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO telemetry_windows(
			id,ingestion_id,service_id,endpoint_id,window_start,window_end,request_count,error_count,duration_sum_ns,
			p50_ns,p95_ns,p99_ns,observed_at,ingested_at,is_complete,coverage_ratio,algorithm_version,created_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, ingestionID, serviceIDs[window.ServiceKey],
			nullString(endpointIDs[window.EndpointKey]), formatTime(window.WindowStart), formatTime(window.WindowEnd),
			window.RequestCount, window.ErrorCount, window.DurationSumNS, window.P50NS, window.P95NS, window.P99NS,
			formatTime(snapshot.ObservedAt), stamp, complete, window.CoverageRatio, telemetry.AggregationVersion, stamp)
		if err != nil {
			return telemetry.IngestResult{}, fmt.Errorf("%w: save telemetry window: %w", errs.ErrUnavailable, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return telemetry.IngestResult{}, fmt.Errorf("%w: commit telemetry ingestion: %w", errs.ErrUnavailable, err)
	}
	return telemetry.IngestResult{IngestionID: ingestionID, SourceHash: snapshot.SourceHash, Environment: snapshot.Environment, WindowStart: snapshot.WindowStart, WindowEnd: snapshot.WindowEnd, Stats: snapshot.Stats, Warnings: append([]string(nil), snapshot.Warnings...)}, nil
}

func existingTelemetryIngestion(ctx context.Context, tx *sql.Tx, sourceID string, snapshot telemetry.Snapshot) (telemetry.IngestResult, bool, error) {
	var result telemetry.IngestResult
	var start, end, warnings string
	err := tx.QueryRowContext(ctx, `SELECT id,source_hash,environment,window_start,window_end,
		lines,resource_spans,spans_seen,spans_accepted,spans_ignored,services_count,endpoints_count,
		dependencies_count,observations_count,telemetry_windows_count,warnings_json
		FROM telemetry_ingestions WHERE source_id=? AND environment=? AND window_start=? AND window_end=?`,
		sourceID, snapshot.Environment, formatTime(snapshot.WindowStart), formatTime(snapshot.WindowEnd)).Scan(
		&result.IngestionID, &result.SourceHash, &result.Environment, &start, &end, &result.Stats.Lines,
		&result.Stats.ResourceSpans, &result.Stats.SpansSeen, &result.Stats.SpansAccepted, &result.Stats.SpansIgnored,
		&result.Stats.Services, &result.Stats.Endpoints, &result.Stats.Dependencies, &result.Stats.Observations,
		&result.Stats.TelemetryWindows, &warnings)
	if errors.Is(err, sql.ErrNoRows) {
		return result, false, nil
	}
	if err != nil {
		return result, false, fmt.Errorf("%w: find telemetry ingestion: %w", errs.ErrUnavailable, err)
	}
	result.WindowStart, err = parseTime(start)
	if err == nil {
		result.WindowEnd, err = parseTime(end)
	}
	if err == nil {
		err = json.Unmarshal([]byte(warnings), &result.Warnings)
	}
	if err != nil {
		return result, false, fmt.Errorf("%w: decode stored telemetry ingestion: %w", errs.ErrIncompatible, err)
	}
	return result, true, nil
}

func ensureTelemetryService(ctx context.Context, tx *sql.Tx, service telemetry.Service, environment string, observedAt, now time.Time) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, "SELECT id FROM services WHERE environment=? AND logical_key=?", environment, service.LogicalKey).Scan(&id)
	observed, stamp := formatTime(observedAt), formatTime(now)
	if errors.Is(err, sql.ErrNoRows) {
		id, err = identity.NewV7(now)
		if err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO services(id,logical_key,environment,display_name,first_seen_at,last_seen_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, id, service.LogicalKey, environment, service.DisplayName, observed, observed, stamp, stamp)
		}
	} else if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE services SET first_seen_at=MIN(first_seen_at,?),last_seen_at=MAX(last_seen_at,?),display_name=CASE WHEN last_seen_at<? THEN ? WHEN last_seen_at=? AND display_name>? THEN ? ELSE display_name END,updated_at=? WHERE id=?`, observed, observed, observed, service.DisplayName, observed, service.DisplayName, service.DisplayName, stamp, id)
	}
	if err != nil {
		return "", fmt.Errorf("%w: save telemetry service: %w", errs.ErrUnavailable, err)
	}
	return id, nil
}

func ensureTelemetryEndpoint(ctx context.Context, tx *sql.Tx, endpoint telemetry.Endpoint, serviceID string, observedAt, now time.Time) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, "SELECT id FROM endpoints WHERE service_id=? AND protocol=? AND operation=?", serviceID, endpoint.Protocol, endpoint.Operation).Scan(&id)
	observed, stamp := formatTime(observedAt), formatTime(now)
	if errors.Is(err, sql.ErrNoRows) {
		id, err = identity.NewV7(now)
		if err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO endpoints(id,service_id,protocol,operation,route_template,first_seen_at,last_seen_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, id, serviceID, endpoint.Protocol, endpoint.Operation, nullString(endpoint.RouteTemplate), observed, observed, stamp, stamp)
		}
	} else if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE endpoints SET first_seen_at=MIN(first_seen_at,?),last_seen_at=MAX(last_seen_at,?),updated_at=? WHERE id=?`, observed, observed, stamp, id)
	}
	if err != nil {
		return "", fmt.Errorf("%w: save endpoint: %w", errs.ErrUnavailable, err)
	}
	return id, nil
}

func ensureTelemetryDependency(ctx context.Context, tx *sql.Tx, dependency telemetry.Dependency, environment string, observedAt, now time.Time) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, "SELECT id FROM dependencies WHERE environment=? AND kind=? AND logical_key=?", environment, dependency.Kind, dependency.LogicalKey).Scan(&id)
	stamp, observed := formatTime(now), formatTime(observedAt)
	if errors.Is(err, sql.ErrNoRows) {
		id, err = identity.NewV7(now)
		if err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO dependencies(id,environment,kind,logical_key,display_name,first_seen_at,last_seen_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, id, environment, dependency.Kind, dependency.LogicalKey, dependency.DisplayName, observed, observed, stamp, stamp)
		}
	} else if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE dependencies SET display_name=CASE WHEN last_seen_at<? THEN ? WHEN last_seen_at=? AND display_name>? THEN ? ELSE display_name END,first_seen_at=MIN(first_seen_at,?),last_seen_at=MAX(last_seen_at,?),updated_at=? WHERE id=?`, observed, dependency.DisplayName, observed, dependency.DisplayName, dependency.DisplayName, observed, observed, stamp, id)
	}
	if err != nil {
		return "", fmt.Errorf("%w: save dependency: %w", errs.ErrUnavailable, err)
	}
	return id, nil
}

func sanitizedJSON(value any, max int) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("%w: encode sanitized JSON", errs.ErrInvalid)
	}
	if len(encoded) > max {
		return "", fmt.Errorf("%w: sanitized JSON exceeds %d bytes", errs.ErrInvalid, max)
	}
	return string(encoded), nil
}

func nonNilTelemetryStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func (s *Store) ResolveGraphRoots(ctx context.Context, environment, selector string, all bool) ([]topology.Node, error) {
	statement := `SELECT id,logical_key,display_name FROM services WHERE environment=?`
	args := []any{environment}
	if !all {
		statement += ` AND (id=? OR logical_key=? OR display_name=?)`
		args = append(args, selector, selector, selector)
	}
	statement += ` ORDER BY logical_key,id`
	rows, err := s.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve graph roots: %w", errs.ErrUnavailable, err)
	}
	defer rows.Close()
	var result []topology.Node
	for rows.Next() {
		var node topology.Node
		node.Type = "service"
		if err := rows.Scan(&node.ID, &node.LogicalKey, &node.DisplayName); err != nil {
			return nil, err
		}
		result = append(result, node)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !all && len(result) > 1 {
		return nil, topology.Ambiguous(selector, result)
	}
	return result, nil
}

func (s *Store) ObservedGraph(ctx context.Context, environment string, at time.Time, minimum topology.Confidence, fromServiceIDs []string, limit int) ([]topology.Node, []topology.Edge, bool, error) {
	if limit < 1 || limit > 10000 {
		return nil, nil, false, fmt.Errorf("%w: graph edge limit must be between 1 and 10000", errs.ErrInvalid)
	}
	if len(fromServiceIDs) == 0 {
		return []topology.Node{}, []topology.Edge{}, false, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(fromServiceIDs)), ",")
	statement := `SELECT o.id,fs.id,fs.logical_key,fs.display_name,
		d.id,d.kind,d.logical_key,d.display_name,COALESCE(ts.id,''),COALESCE(ts.logical_key,''),COALESCE(ts.display_name,''),
		o.window_start,o.window_end,o.request_count,o.error_count,o.duration_sum_ns,o.confidence_level,
		o.confidence_basis,o.algorithm_version,o.evidence_ids_json,o.limitations_json
		FROM service_dependency_observations o
		JOIN telemetry_ingestions i ON i.id=o.ingestion_id
		JOIN services fs ON fs.id=o.from_service_id
		JOIN dependencies d ON d.id=o.dependency_id
		LEFT JOIN services ts ON ts.id=o.target_service_id
		WHERE i.environment=? AND o.window_start<=? AND o.window_end>?
		AND CASE o.confidence_level WHEN 'LOW' THEN 1 WHEN 'MEDIUM' THEN 2 WHEN 'HIGH' THEN 3 ELSE 0 END >= ?
		AND o.from_service_id IN (` + placeholders + `)
		ORDER BY fs.logical_key,fs.display_name,
		CASE WHEN ts.id IS NULL THEN 0 ELSE 1 END,
		COALESCE(ts.logical_key,d.logical_key),COALESCE(ts.display_name,d.display_name),d.kind,
		o.window_start,o.window_end,
		CASE o.confidence_level WHEN 'HIGH' THEN 3 WHEN 'MEDIUM' THEN 2 WHEN 'LOW' THEN 1 ELSE 0 END DESC,
		o.confidence_basis,o.request_count,o.error_count,o.duration_sum_ns,o.algorithm_version,
		o.limitations_json,o.evidence_ids_json,fs.id,COALESCE(ts.id,d.id),o.id LIMIT ?`
	arguments := []any{environment, formatTime(at), formatTime(at), topology.Rank(minimum)}
	for _, id := range fromServiceIDs {
		arguments = append(arguments, id)
	}
	arguments = append(arguments, limit+1)
	rows, err := s.db.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return nil, nil, false, fmt.Errorf("%w: query observed graph: %w", errs.ErrUnavailable, err)
	}
	defer rows.Close()
	nodes := make(map[string]topology.Node)
	var edges []topology.Edge
	truncated := false
	for rows.Next() {
		var edge topology.Edge
		var fromKey, fromDisplay, dependencyID, dependencyKey, dependencyDisplay, targetID, targetKey, targetDisplay string
		var start, end, evidenceJSON, limitationsJSON string
		if err := rows.Scan(&edge.ID, &edge.From, &fromKey, &fromDisplay, &dependencyID, &edge.DependencyKind, &dependencyKey, &dependencyDisplay,
			&targetID, &targetKey, &targetDisplay, &start, &end, &edge.RequestCount, &edge.ErrorCount, &edge.DurationSumNS,
			&edge.Confidence, &edge.Basis, &edge.AlgorithmVersion, &evidenceJSON, &limitationsJSON); err != nil {
			return nil, nil, false, err
		}
		edge.WindowStart, err = parseTime(start)
		if err == nil {
			edge.WindowEnd, err = parseTime(end)
		}
		if err == nil {
			err = json.Unmarshal([]byte(evidenceJSON), &edge.EvidenceIDs)
		}
		if err == nil {
			err = json.Unmarshal([]byte(limitationsJSON), &edge.Limitations)
		}
		if err != nil {
			return nil, nil, false, fmt.Errorf("%w: decode observed graph row: %w", errs.ErrIncompatible, err)
		}
		if len(edges) >= limit {
			truncated = true
			continue
		}
		nodes[edge.From] = topology.Node{ID: edge.From, Type: "service", LogicalKey: fromKey, DisplayName: fromDisplay}
		if targetID != "" {
			edge.To = targetID
			nodes[targetID] = topology.Node{ID: targetID, Type: "service", LogicalKey: targetKey, DisplayName: targetDisplay}
		} else {
			edge.To = dependencyID
			nodes[dependencyID] = topology.Node{ID: dependencyID, Type: "dependency", LogicalKey: dependencyKey, DisplayName: dependencyDisplay}
		}
		edges = append(edges, edge)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, false, err
	}
	resultNodes := make([]topology.Node, 0, len(nodes))
	for _, node := range nodes {
		resultNodes = append(resultNodes, node)
	}
	sort.Slice(resultNodes, func(i, j int) bool { return resultNodes[i].ID < resultNodes[j].ID })
	return resultNodes, edges, truncated, nil
}
