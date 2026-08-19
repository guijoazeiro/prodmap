package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/guijoazeiro/prodmap/internal/correlation"
	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/identity"
	"github.com/guijoazeiro/prodmap/internal/inventory"
)

func (s *Store) SaveRuntimeSnapshot(ctx context.Context, snapshot inventory.Snapshot) (inventory.RefreshResult, error) {
	if snapshot.ObservedAt.IsZero() {
		return inventory.RefreshResult{}, fmt.Errorf("%w: snapshot observed_at is required", errs.ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return inventory.RefreshResult{}, fmt.Errorf("%w: begin runtime snapshot: %w", errs.ErrUnavailable, err)
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	dockerSourceID, err := ensureSource(ctx, tx, "docker", "local", snapshot.ObservedAt, now)
	if err != nil {
		return inventory.RefreshResult{}, err
	}
	var repositoryID string
	if snapshot.Repository != nil {
		gitSourceID, sourceErr := ensureSource(ctx, tx, "git", snapshot.Repository.ExternalID, snapshot.ObservedAt, now)
		if sourceErr != nil {
			return inventory.RefreshResult{}, sourceErr
		}
		repositoryID, err = ensureRepository(ctx, tx, gitSourceID, *snapshot.Repository, snapshot.ObservedAt, now)
		if err != nil {
			return inventory.RefreshResult{}, err
		}
	}

	services := make(map[string]struct{})
	for _, item := range snapshot.Items {
		if err := ctx.Err(); err != nil {
			return inventory.RefreshResult{}, err
		}
		serviceID, serviceErr := ensureService(ctx, tx, item, now)
		if serviceErr != nil {
			return inventory.RefreshResult{}, serviceErr
		}
		services[item.Environment+"\x00"+item.ServiceLogicalKey] = struct{}{}
		artifactID, artifactErr := ensureArtifact(ctx, tx, dockerSourceID, item, now)
		if artifactErr != nil {
			return inventory.RefreshResult{}, artifactErr
		}
		var commitID string
		if item.Commit != nil && repositoryID != "" {
			commitID, err = ensureCommit(ctx, tx, repositoryID, *item.Commit, item.Runtime.ObservedAt, now)
			if err != nil {
				return inventory.RefreshResult{}, err
			}
		}
		runtimeID, created, runtimeErr := ensureRuntime(ctx, tx, dockerSourceID, serviceID, artifactID, item, now)
		if runtimeErr != nil {
			return inventory.RefreshResult{}, runtimeErr
		}
		if created {
			if err := insertCorrelation(ctx, tx, runtimeID, artifactID, commitID, item.Correlation, item.Runtime.ObservedAt, now); err != nil {
				return inventory.RefreshResult{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return inventory.RefreshResult{}, fmt.Errorf("%w: commit runtime snapshot: %w", errs.ErrUnavailable, err)
	}
	return inventory.RefreshResult{
		OperationID: snapshot.OperationID,
		ObservedAt:  snapshot.ObservedAt, Processed: len(snapshot.Items), Rejected: snapshot.Rejected,
		Services: len(services), RuntimeInstances: len(snapshot.Items), Warnings: append([]string{}, snapshot.Warnings...),
	}, nil
}

func ensureSource(ctx context.Context, tx *sql.Tx, kind, instanceKey string, observedAt, now time.Time) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, "SELECT id FROM sources WHERE kind = ? AND instance_key = ?", kind, instanceKey).Scan(&id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: find %s source: %w", errs.ErrUnavailable, kind, err)
	}
	stamp := formatTime(now)
	observed := formatTime(observedAt)
	if errors.Is(err, sql.ErrNoRows) {
		id, err = identity.NewV7(now)
		if err != nil {
			return "", err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO sources(id, kind, name, instance_key, last_sync_at, last_status, created_at, updated_at) VALUES(?,?,?,?,?,'success',?,?)`, id, kind, kind+" local", instanceKey, observed, stamp, stamp)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE sources SET last_sync_at = CASE WHEN last_sync_at IS NULL OR last_sync_at < ? THEN ? ELSE last_sync_at END, last_status='success', updated_at=? WHERE id=?`, observed, observed, stamp, id)
	}
	if err != nil {
		return "", fmt.Errorf("%w: save %s source: %w", errs.ErrUnavailable, kind, err)
	}
	return id, nil
}

func ensureRepository(ctx context.Context, tx *sql.Tx, sourceID string, repository inventory.Repository, observedAt, now time.Time) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, "SELECT id FROM repositories WHERE source_id=? AND external_id=?", sourceID, repository.ExternalID).Scan(&id)
	stamp := formatTime(now)
	if errors.Is(err, sql.ErrNoRows) {
		id, err = identity.NewV7(now)
		if err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO repositories(id,source_id,external_id,name,canonical_url,root_path_hash,observed_at,ingested_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, sourceID, repository.ExternalID, repository.Name, nullString(repository.CanonicalURL), nullString(repository.RootPathHash), formatTime(observedAt), stamp, stamp, stamp)
		}
	} else if err == nil {
		observed := formatTime(observedAt)
		_, err = tx.ExecContext(ctx, `UPDATE repositories SET name=CASE WHEN observed_at<=? THEN ? ELSE name END, canonical_url=CASE WHEN observed_at<=? THEN ? ELSE canonical_url END, root_path_hash=CASE WHEN observed_at<=? THEN ? ELSE root_path_hash END, observed_at=CASE WHEN observed_at<=? THEN ? ELSE observed_at END, ingested_at=?, updated_at=? WHERE id=?`, observed, repository.Name, observed, nullString(repository.CanonicalURL), observed, nullString(repository.RootPathHash), observed, observed, stamp, stamp, id)
	}
	if err != nil {
		return "", fmt.Errorf("%w: save repository: %w", errs.ErrUnavailable, err)
	}
	return id, nil
}

func ensureCommit(ctx context.Context, tx *sql.Tx, repositoryID string, commit inventory.Commit, observedAt, now time.Time) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, "SELECT id FROM commits WHERE repository_id=? AND sha=?", repositoryID, strings.ToLower(commit.SHA)).Scan(&id)
	stamp := formatTime(now)
	var author any
	if commit.AuthorTime != nil {
		author = formatTime(*commit.AuthorTime)
	}
	if errors.Is(err, sql.ErrNoRows) {
		id, err = identity.NewV7(now)
		if err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO commits(id,repository_id,sha,author_time,commit_time,subject,tree_sha,observed_at,ingested_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, repositoryID, strings.ToLower(commit.SHA), author, formatTime(commit.CommitTime), commit.Subject, nullString(commit.TreeSHA), formatTime(observedAt), stamp, stamp, stamp)
		}
	}
	if err != nil {
		return "", fmt.Errorf("%w: save commit: %w", errs.ErrUnavailable, err)
	}
	return id, nil
}

func ensureService(ctx context.Context, tx *sql.Tx, item inventory.SnapshotItem, now time.Time) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, "SELECT id FROM services WHERE environment=? AND logical_key=?", item.Environment, item.ServiceLogicalKey).Scan(&id)
	observed := formatTime(item.Runtime.ObservedAt)
	stamp := formatTime(now)
	if errors.Is(err, sql.ErrNoRows) {
		id, err = identity.NewV7(now)
		if err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO services(id,logical_key,environment,display_name,first_seen_at,last_seen_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, id, item.ServiceLogicalKey, item.Environment, item.ServiceDisplayName, observed, observed, stamp, stamp)
		}
	} else if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE services SET display_name=CASE WHEN last_seen_at<=? THEN ? ELSE display_name END, first_seen_at=CASE WHEN first_seen_at>? THEN ? ELSE first_seen_at END, last_seen_at=CASE WHEN last_seen_at<? THEN ? ELSE last_seen_at END, updated_at=? WHERE id=?`, observed, item.ServiceDisplayName, observed, observed, observed, observed, stamp, id)
	}
	if err != nil {
		return "", fmt.Errorf("%w: save service: %w", errs.ErrUnavailable, err)
	}
	return id, nil
}

func ensureArtifact(ctx context.Context, tx *sql.Tx, sourceID string, item inventory.SnapshotItem, now time.Time) (string, error) {
	artifact := item.Artifact
	labels := artifact.OCILabels
	if labels == nil {
		labels = map[string]string{}
	}
	encodedLabels, err := json.Marshal(labels)
	if err != nil {
		return "", fmt.Errorf("encode OCI labels: %w", err)
	}
	if len(encodedLabels) > 8192 {
		return "", fmt.Errorf("%w: OCI label metadata exceeds limit", errs.ErrInvalid)
	}
	observed := formatTime(item.Runtime.ObservedAt)
	var id, existingObserved, existingReference, existingRevision, existingLabels string
	err = tx.QueryRowContext(ctx, "SELECT id,observed_at,COALESCE(observed_reference,''),COALESCE(oci_revision,''),oci_labels_json FROM artifacts WHERE source_id=? AND identity_kind=? AND identity=?", sourceID, artifact.IdentityKind, artifact.Identity).Scan(&id, &existingObserved, &existingReference, &existingRevision, &existingLabels)
	stamp := formatTime(now)
	if errors.Is(err, sql.ErrNoRows) {
		id, err = identity.NewV7(now)
		if err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO artifacts(id,source_id,kind,name,identity_kind,identity,digest_algorithm,digest,image_id,observed_reference,oci_revision,oci_labels_json,observed_at,ingested_at,created_at,updated_at) VALUES(?,?,'container_image',?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, sourceID, artifact.Name, artifact.IdentityKind, artifact.Identity, nullString(artifact.DigestAlgorithm), nullString(artifact.Digest), nullString(artifact.ImageID), nullString(artifact.ObservedReference), nullString(artifact.OCIRevision), string(encodedLabels), observed, stamp, stamp, stamp)
		}
	} else if err == nil {
		if existingObserved == observed && (existingReference != artifact.ObservedReference || existingRevision != artifact.OCIRevision || existingLabels != string(encodedLabels)) {
			return "", fmt.Errorf("%w: artifact metadata changed for the same identity and observed_at", errs.ErrConflict)
		}
		_, err = tx.ExecContext(ctx, `UPDATE artifacts SET observed_reference=CASE WHEN observed_at<=? THEN ? ELSE observed_reference END, oci_revision=CASE WHEN observed_at<=? THEN ? ELSE oci_revision END, oci_labels_json=CASE WHEN observed_at<=? THEN ? ELSE oci_labels_json END, observed_at=CASE WHEN observed_at<=? THEN ? ELSE observed_at END, ingested_at=?, updated_at=? WHERE id=?`, observed, nullString(artifact.ObservedReference), observed, nullString(artifact.OCIRevision), observed, string(encodedLabels), observed, observed, stamp, stamp, id)
	}
	if err != nil {
		return "", fmt.Errorf("%w: save artifact: %w", errs.ErrUnavailable, err)
	}
	for _, alias := range artifact.Aliases {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO artifact_aliases(artifact_id,alias,valid_from) VALUES(?,?,?)`, id, alias, observed); err != nil {
			return "", fmt.Errorf("%w: save artifact alias: %w", errs.ErrUnavailable, err)
		}
	}
	return id, nil
}

func ensureRuntime(ctx context.Context, tx *sql.Tx, sourceID, serviceID, artifactID string, item inventory.SnapshotItem, now time.Time) (string, bool, error) {
	observed := formatTime(item.Runtime.ObservedAt)
	startedValue := ""
	var started any
	if item.Runtime.StartedAt != nil {
		startedValue = formatTime(*item.Runtime.StartedAt)
		started = startedValue
	}
	var id, existingServiceID, existingArtifactID, existingContainerName, existingState, existingHealth, existingReference, existingImageID, existingStarted string
	var existingRestartCount int64
	err := tx.QueryRowContext(ctx, `SELECT id,service_id,artifact_id,container_name,state,health,restart_count,image_reference,COALESCE(image_id,''),COALESCE(started_at,'') FROM runtime_instances WHERE source_id=? AND external_id=? AND observed_at=?`, sourceID, item.Runtime.ExternalID, observed).Scan(&id, &existingServiceID, &existingArtifactID, &existingContainerName, &existingState, &existingHealth, &existingRestartCount, &existingReference, &existingImageID, &existingStarted)
	if err == nil {
		if existingServiceID != serviceID || existingArtifactID != artifactID || existingContainerName != item.Runtime.ContainerName ||
			existingState != item.Runtime.State || existingHealth != item.Runtime.Health || existingRestartCount != item.Runtime.RestartCount ||
			existingReference != item.Runtime.ImageReference || existingImageID != item.Runtime.ImageID || existingStarted != startedValue {
			return "", false, fmt.Errorf("%w: runtime snapshot identity or metadata changed for the same source, external ID, and observed_at", errs.ErrConflict)
		}
		return id, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("%w: find runtime snapshot: %w", errs.ErrUnavailable, err)
	}
	id, err = identity.NewV7(now)
	if err != nil {
		return "", false, err
	}
	var successor sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT MIN(valid_from) FROM runtime_instances WHERE source_id=? AND external_id=? AND valid_from>?`, sourceID, item.Runtime.ExternalID, observed).Scan(&successor); err != nil {
		return "", false, fmt.Errorf("%w: find runtime successor: %w", errs.ErrUnavailable, err)
	}
	var imageDigest any
	if item.Artifact.IdentityKind == "repo_digest" {
		imageDigest = item.Artifact.Identity
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_instances(id,source_id,external_id,container_name,service_id,artifact_id,runtime_kind,state,health,restart_count,started_at,valid_from,valid_to,observed_at,ingested_at,image_reference,image_id,image_digest,created_at,updated_at) VALUES(?,?,?,?,?,?,'docker_container',?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, sourceID, item.Runtime.ExternalID, item.Runtime.ContainerName, serviceID, artifactID, item.Runtime.State, item.Runtime.Health, item.Runtime.RestartCount, started, observed, nullSQLString(successor), observed, formatTime(now), item.Runtime.ImageReference, nullString(item.Runtime.ImageID), imageDigest, formatTime(now), formatTime(now))
	if err != nil {
		return "", false, fmt.Errorf("%w: insert runtime snapshot: %w", errs.ErrUnavailable, err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runtime_instances SET valid_to=?, updated_at=? WHERE source_id=? AND external_id=? AND valid_from=(SELECT MAX(valid_from) FROM runtime_instances WHERE source_id=? AND external_id=? AND valid_from<?)`, observed, formatTime(now), sourceID, item.Runtime.ExternalID, sourceID, item.Runtime.ExternalID, observed); err != nil {
		return "", false, fmt.Errorf("%w: close previous runtime interval: %w", errs.ErrUnavailable, err)
	}
	return id, true, nil
}

func insertCorrelation(ctx context.Context, tx *sql.Tx, runtimeID, artifactID, commitID string, result correlation.Result, observedAt, now time.Time) error {
	correlationID, err := identity.NewV7(now)
	if err != nil {
		return err
	}
	missing, err := json.Marshal(result.Missing)
	if err != nil {
		return fmt.Errorf("encode missing data: %w", err)
	}
	warnings, err := json.Marshal(result.Warnings)
	if err != nil {
		return fmt.Errorf("encode correlation warnings: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO correlations(id,runtime_id,artifact_id,commit_id,relation_type,score,level,algorithm_version,conclusion,missing_json,warnings_json,observed_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, correlationID, runtimeID, artifactID, nullString(commitID), result.RelationType, result.Score, result.Level, result.Algorithm, result.Conclusion, string(missing), string(warnings), formatTime(observedAt), formatTime(now), formatTime(now))
	if err != nil {
		return fmt.Errorf("%w: save correlation: %w", errs.ErrUnavailable, err)
	}
	for ordinal, evidence := range result.Evidence {
		evidenceID, idErr := identity.NewV7(now)
		if idErr != nil {
			return idErr
		}
		details, marshalErr := json.Marshal(evidence.Details)
		if marshalErr != nil {
			return fmt.Errorf("encode evidence details: %w", marshalErr)
		}
		if len(details) > 4096 {
			return fmt.Errorf("%w: evidence details exceed limit", errs.ErrInvalid)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO evidence(id,correlation_id,kind,subject,claim,polarity,strength,source,observed_at,details_json,ordinal,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, evidenceID, correlationID, evidence.Kind, evidence.Subject, evidence.Claim, evidence.Polarity, evidence.Strength, evidence.Source, formatTime(evidence.ObservedAt), string(details), ordinal, formatTime(now))
		if err != nil {
			return fmt.Errorf("%w: save evidence: %w", errs.ErrUnavailable, err)
		}
	}
	return nil
}

func (s *Store) Status(ctx context.Context) (inventory.Status, error) {
	var result inventory.Status
	var last sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(last_sync_at) FROM sources WHERE kind='docker'`).Scan(&last); err != nil {
		return result, fmt.Errorf("%w: read runtime freshness: %w", errs.ErrUnavailable, err)
	}
	if last.Valid {
		parsed, err := parseTime(last.String)
		if err != nil {
			return result, err
		}
		result.LastSnapshot = &parsed
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM services`).Scan(&result.Services); err != nil {
		return result, fmt.Errorf("%w: count services: %w", errs.ErrUnavailable, err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runtime_instances WHERE valid_to IS NULL`).Scan(&result.RuntimeInstances); err != nil {
		return result, fmt.Errorf("%w: count runtime instances: %w", errs.ErrUnavailable, err)
	}
	return result, nil
}

func (s *Store) Services(ctx context.Context) ([]inventory.ServiceRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT s.id,s.logical_key,s.environment,s.display_name,COUNT(r.id),CASE WHEN COUNT(DISTINCT r.state)=0 THEN 'unknown' WHEN COUNT(DISTINCT r.state)=1 THEN MIN(r.state) ELSE 'mixed' END,CASE WHEN COUNT(DISTINCT r.health)=0 THEN 'unknown' WHEN COUNT(DISTINCT r.health)=1 THEN MIN(r.health) ELSE 'mixed' END,CASE WHEN COUNT(DISTINCT a.identity)=1 THEN MIN(a.identity) ELSE '' END,COALESCE(MIN(CASE c.level WHEN 'UNKNOWN' THEN 0 WHEN 'LOW' THEN 1 WHEN 'MEDIUM' THEN 2 WHEN 'HIGH' THEN 3 WHEN 'EXACT' THEN 4 ELSE 0 END),0),s.last_seen_at FROM services s LEFT JOIN runtime_instances r ON r.service_id=s.id AND r.valid_to IS NULL LEFT JOIN artifacts a ON a.id=r.artifact_id LEFT JOIN correlations c ON c.runtime_id=r.id GROUP BY s.id ORDER BY s.environment,s.logical_key`)
	if err != nil {
		return nil, fmt.Errorf("%w: list services: %w", errs.ErrUnavailable, err)
	}
	defer rows.Close()
	var result []inventory.ServiceRecord
	for rows.Next() {
		var item inventory.ServiceRecord
		var confidenceRank int
		var freshness string
		if err := rows.Scan(&item.ID, &item.LogicalKey, &item.Environment, &item.DisplayName, &item.RuntimeInstances, &item.State, &item.Health, &item.ArtifactIdentity, &confidenceRank, &freshness); err != nil {
			return nil, fmt.Errorf("%w: scan service: %w", errs.ErrUnavailable, err)
		}
		item.CommitConfidence = confidenceFromRank(confidenceRank)
		item.Freshness, err = parseTime(freshness)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func confidenceFromRank(rank int) correlation.Level {
	switch rank {
	case 4:
		return correlation.LevelExact
	case 3:
		return correlation.LevelHigh
	case 2:
		return correlation.LevelMedium
	case 1:
		return correlation.LevelLow
	default:
		return correlation.LevelUnknown
	}
}

func (s *Store) Runtime(ctx context.Context, query inventory.RuntimeQuery) ([]inventory.RuntimeRecord, string, error) {
	if query.Limit < 1 || query.Limit > 1000 || query.At.IsZero() {
		return nil, "", fmt.Errorf("%w: invalid runtime query", errs.ErrInvalid)
	}
	offset, err := decodeCursor(query)
	if err != nil {
		return nil, "", err
	}
	args := []any{formatTime(query.At)}
	where := `r.valid_from<=? AND (r.valid_to IS NULL OR r.valid_to>?)`
	args = append(args, formatTime(query.At))
	if query.Service != "" {
		where += " AND s.logical_key=?"
		args = append(args, query.Service)
	}
	if query.Environment != "" {
		where += " AND s.environment=?"
		args = append(args, query.Environment)
	}
	args = append(args, query.Limit+1, offset)
	statement := `SELECT r.id,r.external_id,r.container_name,s.id,s.logical_key,s.environment,r.runtime_kind,r.state,r.health,r.restart_count,r.started_at,r.observed_at,r.image_reference,COALESCE(r.image_id,''),COALESCE(r.image_digest,''),a.id,a.identity,COALESCE(cm.id,''),COALESCE(cm.sha,''),c.id,c.level,c.score FROM runtime_instances r JOIN services s ON s.id=r.service_id JOIN artifacts a ON a.id=r.artifact_id JOIN correlations c ON c.runtime_id=r.id LEFT JOIN commits cm ON cm.id=c.commit_id WHERE ` + where + ` ORDER BY s.environment,s.logical_key,r.external_id,r.observed_at DESC LIMIT ? OFFSET ?`
	rows, err := s.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, "", fmt.Errorf("%w: query runtime: %w", errs.ErrUnavailable, err)
	}
	defer rows.Close()
	var result []inventory.RuntimeRecord
	for rows.Next() {
		var item inventory.RuntimeRecord
		var started sql.NullString
		var observed string
		if err := rows.Scan(&item.ID, &item.ExternalID, &item.ContainerName, &item.ServiceID, &item.ServiceKey, &item.Environment, &item.RuntimeKind, &item.State, &item.Health, &item.RestartCount, &started, &observed, &item.ImageReference, &item.ImageID, &item.ImageDigest, &item.ArtifactID, &item.ArtifactIdentity, &item.CommitID, &item.CommitSHA, &item.CorrelationID, &item.Confidence, &item.Score); err != nil {
			return nil, "", fmt.Errorf("%w: scan runtime: %w", errs.ErrUnavailable, err)
		}
		item.ObservedAt, err = parseTime(observed)
		if err != nil {
			return nil, "", err
		}
		if started.Valid {
			parsed, parseErr := parseTime(started.String)
			if parseErr != nil {
				return nil, "", parseErr
			}
			item.StartedAt = &parsed
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if err := rows.Close(); err != nil {
		return nil, "", err
	}
	for index := range result {
		result[index].EvidenceIDs, err = s.evidenceIDs(ctx, result[index].CorrelationID)
		if err != nil {
			return nil, "", err
		}
	}
	next := ""
	if len(result) > query.Limit {
		result = result[:query.Limit]
		next = encodeCursor(query, offset+query.Limit)
	}
	return result, next, nil
}

func (s *Store) evidenceIDs(ctx context.Context, correlationID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM evidence WHERE correlation_id=? ORDER BY polarity, strength DESC, ordinal`, correlationID)
	if err != nil {
		return nil, fmt.Errorf("%w: list evidence IDs: %w", errs.ErrUnavailable, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) Explain(ctx context.Context, selector string) (inventory.Explanation, error) {
	return s.explainAt(ctx, selector, nil)
}

// ExplainAt resolves a selector against the runtime state active at the given instant.
func (s *Store) ExplainAt(ctx context.Context, selector string, at time.Time) (inventory.Explanation, error) {
	if at.IsZero() {
		return inventory.Explanation{}, fmt.Errorf("%w: explain timestamp is required", errs.ErrInvalid)
	}
	at = at.UTC()
	return s.explainAt(ctx, selector, &at)
}

func (s *Store) explainAt(ctx context.Context, selector string, at *time.Time) (inventory.Explanation, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return inventory.Explanation{}, fmt.Errorf("%w: explain selector is required", errs.ErrInvalid)
	}
	statement := `SELECT DISTINCT c.id FROM correlations c JOIN runtime_instances r ON r.id=c.runtime_id JOIN artifacts a ON a.id=c.artifact_id LEFT JOIN commits cm ON cm.id=c.commit_id WHERE (c.id=? OR r.id=? OR r.external_id=? OR r.container_name=? OR a.id=? OR cm.id=? OR cm.sha=?)`
	args := []any{selector, selector, selector, selector, selector, selector, strings.ToLower(selector)}
	if at != nil {
		statement += ` AND r.valid_from<=? AND (r.valid_to IS NULL OR r.valid_to>?)`
		args = append(args, formatTime(*at), formatTime(*at))
	}
	statement += ` ORDER BY c.id`
	rows, err := s.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return inventory.Explanation{}, fmt.Errorf("%w: resolve explanation selector: %w", errs.ErrUnavailable, err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return inventory.Explanation{}, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) == 0 {
		return inventory.Explanation{}, fmt.Errorf("%w: selector %q", errs.ErrNotFound, selector)
	}
	if len(ids) > 1 {
		return inventory.Explanation{}, fmt.Errorf("%w: selector %q matches %d correlations", errs.ErrConflict, selector, len(ids))
	}
	var result inventory.Explanation
	var missing, warnings, observed string
	var runtimeID, artifactID, commitID, commitSHA string
	err = s.db.QueryRowContext(ctx, `SELECT c.id,c.conclusion,c.relation_type,c.level,c.score,c.algorithm_version,c.missing_json,c.warnings_json,c.observed_at,r.id,a.id,COALESCE(cm.id,''),COALESCE(cm.sha,'') FROM correlations c JOIN runtime_instances r ON r.id=c.runtime_id JOIN artifacts a ON a.id=c.artifact_id LEFT JOIN commits cm ON cm.id=c.commit_id WHERE c.id=?`, ids[0]).Scan(&result.TargetID, &result.Conclusion, &result.RelationType, &result.Confidence, &result.Score, &result.Algorithm, &missing, &warnings, &observed, &runtimeID, &artifactID, &commitID, &commitSHA)
	if err != nil {
		return result, fmt.Errorf("%w: load explanation: %w", errs.ErrUnavailable, err)
	}
	result.TargetType = "correlation"
	result.Entities = map[string]string{"runtime": runtimeID, "artifact": artifactID}
	if commitID != "" {
		result.Entities["commit"] = commitID
		result.Entities["commit_sha"] = commitSHA
	}
	if err := json.Unmarshal([]byte(missing), &result.Missing); err != nil {
		return result, fmt.Errorf("%w: decode explanation missing data: %w", errs.ErrIncompatible, err)
	}
	if err := json.Unmarshal([]byte(warnings), &result.Warnings); err != nil {
		return result, fmt.Errorf("%w: decode explanation warnings: %w", errs.ErrIncompatible, err)
	}
	result.ObservedAt, err = parseTime(observed)
	if err != nil {
		return result, err
	}
	result.Limitations = []string{"This is a provenance correlation, not proof of causality."}
	if result.Confidence == correlation.LevelUnknown {
		result.Limitations = append(result.Limitations, "Required provenance data is missing or contradictory.")
	}
	if err := s.loadEvidence(ctx, ids[0], &result); err != nil {
		return result, err
	}
	sourceSet := map[string]struct{}{}
	for _, group := range [][]inventory.PersistedEvidence{result.Supporting, result.Contradicting, result.Neutral} {
		for _, evidence := range group {
			sourceSet[evidence.Source] = struct{}{}
		}
	}
	for source := range sourceSet {
		result.Sources = append(result.Sources, source)
	}
	sort.Strings(result.Sources)
	return result, nil
}

func (s *Store) loadEvidence(ctx context.Context, correlationID string, result *inventory.Explanation) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id,kind,subject,claim,polarity,strength,source,observed_at FROM evidence WHERE correlation_id=? ORDER BY polarity,strength DESC,ordinal`, correlationID)
	if err != nil {
		return fmt.Errorf("%w: load evidence: %w", errs.ErrUnavailable, err)
	}
	defer rows.Close()
	for rows.Next() {
		var evidence inventory.PersistedEvidence
		var observed string
		if err := rows.Scan(&evidence.ID, &evidence.Kind, &evidence.Subject, &evidence.Claim, &evidence.Polarity, &evidence.Strength, &evidence.Source, &observed); err != nil {
			return err
		}
		evidence.ObservedAt, err = parseTime(observed)
		if err != nil {
			return err
		}
		switch evidence.Polarity {
		case correlation.PolaritySupports:
			result.Supporting = append(result.Supporting, evidence)
		case correlation.PolarityContradicts:
			result.Contradicting = append(result.Contradicting, evidence)
		default:
			result.Neutral = append(result.Neutral, evidence)
		}
	}
	return rows.Err()
}

type cursorPayload struct {
	Offset    int    `json:"offset"`
	Signature string `json:"signature"`
}

func encodeCursor(query inventory.RuntimeQuery, offset int) string {
	payload := cursorPayload{Offset: offset, Signature: querySignature(query)}
	raw, _ := json.Marshal(payload)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(query inventory.RuntimeQuery) (int, error) {
	if query.Cursor == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(query.Cursor)
	if err != nil {
		return 0, fmt.Errorf("%w: invalid cursor", errs.ErrInvalid)
	}
	var payload cursorPayload
	if err := json.Unmarshal(raw, &payload); err != nil || payload.Offset < 0 || payload.Signature != querySignature(query) {
		return 0, fmt.Errorf("%w: cursor does not belong to this runtime query", errs.ErrInvalid)
	}
	return payload.Offset, nil
}

func querySignature(query inventory.RuntimeQuery) string {
	sum := sha256.Sum256([]byte(query.Service + "\x00" + query.Environment + "\x00" + formatTime(query.At) + "\x00" + fmt.Sprint(query.Limit)))
	return hex.EncodeToString(sum[:8])
}

const sqliteTimestampLayout = "2006-01-02T15:04:05.000000000Z07:00"

func formatTime(value time.Time) string { return value.UTC().Format(sqliteTimestampLayout) }

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: invalid stored timestamp: %w", errs.ErrIncompatible, err)
	}
	return parsed.UTC(), nil
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullSQLString(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}
