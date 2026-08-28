package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/guijoazeiro/prodmap/internal/correlation"
	"github.com/guijoazeiro/prodmap/internal/deployment"
	"github.com/guijoazeiro/prodmap/internal/errs"
	"github.com/guijoazeiro/prodmap/internal/identity"
)

func (s *Store) SaveDeployment(ctx context.Context, snapshot deployment.Snapshot) (deployment.IngestResult, error) {
	if err := deployment.ValidateSnapshot(snapshot); err != nil {
		return deployment.IngestResult{}, err
	}
	var lastErr error
	for attempt := range 4 {
		result, err := s.saveDeployment(ctx, snapshot)
		if err == nil || !isSQLiteBusy(err) {
			return result, err
		}
		lastErr = err
		timer := time.NewTimer(time.Duration(attempt+1) * 10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return deployment.IngestResult{}, ctx.Err()
		case <-timer.C:
		}
	}
	return deployment.IngestResult{}, lastErr
}

func (s *Store) saveDeployment(ctx context.Context, snapshot deployment.Snapshot) (deployment.IngestResult, error) {
	tx, cleanup, err := s.beginTransaction(ctx)
	if err != nil {
		return deployment.IngestResult{}, fmt.Errorf("%w: begin deployment ingestion: %w", errs.ErrUnavailable, err)
	}
	defer cleanup()
	defer tx.Rollback()
	now := s.clockNow()
	result, _, err := s.saveDeploymentTx(ctx, tx, snapshot, now, "deployment_ledger", []string{})
	if err != nil {
		return deployment.IngestResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.IngestResult{}, fmt.Errorf("%w: commit deployment ingestion: %w", errs.ErrUnavailable, err)
	}
	return result, nil
}

// saveDeploymentTx writes the immutable deployment ledger in the caller's
// transaction. Remote source observations use this exact path so the ledger
// and its authenticated source metadata are committed atomically.
func (s *Store) saveDeploymentTx(ctx context.Context, tx *sql.Tx, snapshot deployment.Snapshot, now time.Time, sourceKind string, warnings []string) (deployment.IngestResult, string, error) {
	sourceID, err := ensureSource(ctx, tx, sourceKind, snapshot.SourceKey, "success", snapshot.ObservedAt, now)
	if err != nil {
		return deployment.IngestResult{}, "", err
	}
	if result, found, err := existingDeploymentIngestion(ctx, tx, sourceID, snapshot.SourceHash); err != nil {
		return deployment.IngestResult{}, "", err
	} else if found {
		result.IdempotentReplay = true
		return result, sourceID, nil
	}

	existing := 0
	for _, record := range snapshot.Records {
		var stored string
		err := tx.QueryRowContext(ctx, `SELECT record_fingerprint FROM deployments WHERE source_id=? AND external_id=? AND service_key=?`, sourceID, record.DeploymentID, record.Service).Scan(&stored)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return deployment.IngestResult{}, "", fmt.Errorf("%w: inspect deployment identity: %w", errs.ErrUnavailable, err)
		case stored != record.Fingerprint:
			return deployment.IngestResult{}, "", fmt.Errorf("%w: deployment identity changed", errs.ErrConflict)
		default:
			existing++
		}
	}
	ingestionID, err := identity.NewV7(now)
	if err != nil {
		return deployment.IngestResult{}, "", err
	}
	stamp := formatTime(now)
	warningsJSON, err := sanitizedJSON(warnings, 8192)
	if err != nil {
		return deployment.IngestResult{}, "", err
	}
	inserted := len(snapshot.Records) - existing
	if _, err := tx.ExecContext(ctx, `INSERT INTO deployment_ingestions(id,source_id,source_hash,format,records_seen,deployments_inserted,deployments_existing,warnings_json,observed_at,ingested_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, ingestionID, sourceID, snapshot.SourceHash, deployment.Format, len(snapshot.Records), inserted, existing, warningsJSON, formatTime(snapshot.ObservedAt), stamp, stamp); err != nil {
		return deployment.IngestResult{}, "", fmt.Errorf("%w: create deployment ingestion: %w", errs.ErrUnavailable, err)
	}
	for _, record := range snapshot.Records {
		var known string
		err := tx.QueryRowContext(ctx, `SELECT id FROM deployments WHERE source_id=? AND external_id=? AND service_key=?`, sourceID, record.DeploymentID, record.Service).Scan(&known)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return deployment.IngestResult{}, "", fmt.Errorf("%w: inspect deployment: %w", errs.ErrUnavailable, err)
		}
		resolved, err := resolveDeployment(ctx, tx, record, now)
		if err != nil {
			return deployment.IngestResult{}, "", err
		}
		id, err := identity.NewV7(now)
		if err != nil {
			return deployment.IngestResult{}, "", err
		}
		limitationsJSON, err := sanitizedJSON(resolved.limitations, 8192)
		if err != nil {
			return deployment.IngestResult{}, "", err
		}
		verified := 0
		if record.VCSRevisionVerified {
			verified = 1
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO deployments(id,source_id,first_ingestion_id,external_id,environment,service_key,service_id,artifact_id,commit_id,status,strategy,started_at,finished_at,artifact_repo_digest,artifact_image_id,commit_sha,commit_verified,record_fingerprint,provenance_status,confidence_level,confidence_basis,limitations_json,algorithm_version,observed_at,ingested_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, sourceID, ingestionID, record.DeploymentID, record.Environment, record.Service, nullable(resolved.serviceID), nullable(resolved.artifactID), nullable(resolved.commitID), record.Status, "unknown", formatTime(record.DeployedAt), nil, nullablePointer(record.RepoDigest), record.ImageID, nullable(resolved.commitSHA), verified, record.Fingerprint, resolved.status, resolved.confidence, resolved.basis, limitationsJSON, deployment.AlgorithmVersion, formatTime(snapshot.ObservedAt), stamp, stamp, stamp); err != nil {
			return deployment.IngestResult{}, "", fmt.Errorf("%w: save deployment: %w", errs.ErrUnavailable, err)
		}
		for ordinal, evidence := range resolved.evidence {
			evidenceID, err := identity.NewV7(now)
			if err != nil {
				return deployment.IngestResult{}, "", err
			}
			details, err := sanitizedJSON(map[string]string{}, 512)
			if err != nil {
				return deployment.IngestResult{}, "", err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO deployment_evidence(id,deployment_id,kind,subject,claim,polarity,source,observed_at,details_json,ordinal,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, evidenceID, id, evidence.Kind, evidence.Subject, evidence.Claim, evidence.Polarity, evidence.Source, formatTime(snapshot.ObservedAt), details, ordinal, stamp); err != nil {
				return deployment.IngestResult{}, "", fmt.Errorf("%w: save deployment evidence: %w", errs.ErrUnavailable, err)
			}
		}
	}
	return deployment.IngestResult{IngestionID: ingestionID, SourceHash: snapshot.SourceHash, Format: deployment.Format, RecordsSeen: len(snapshot.Records), DeploymentsInserted: inserted, DeploymentsExisting: existing, Warnings: warnings}, sourceID, nil
}

// SaveDeploymentSource persists a validated remote ledger and its authenticated
// source observation in one transaction. A failed or contradictory source
// observation therefore cannot leave a deployment ledger behind.
func (s *Store) SaveDeploymentSource(ctx context.Context, source deployment.SourceResult) (deployment.SyncResult, error) {
	if err := deployment.ValidateSourceResult(source); err != nil {
		return deployment.SyncResult{}, err
	}
	var lastErr error
	for attempt := range 4 {
		result, err := s.saveDeploymentSource(ctx, source)
		if err == nil || !isSQLiteBusy(err) {
			return result, err
		}
		lastErr = err
		timer := time.NewTimer(time.Duration(attempt+1) * 10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return deployment.SyncResult{}, ctx.Err()
		case <-timer.C:
		}
	}
	return deployment.SyncResult{}, lastErr
}

func (s *Store) saveDeploymentSource(ctx context.Context, source deployment.SourceResult) (deployment.SyncResult, error) {
	tx, cleanup, err := s.beginTransaction(ctx)
	if err != nil {
		return deployment.SyncResult{}, fmt.Errorf("%w: begin GitHub Actions deployment sync: %w", errs.ErrUnavailable, err)
	}
	defer cleanup()
	defer tx.Rollback()

	now := s.clockNow()
	ingestion, sourceID, err := s.saveDeploymentTx(ctx, tx, source.Snapshot, now, "github_actions_deployment_ledger", source.Warnings)
	if err != nil {
		return deployment.SyncResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sources SET name=? WHERE id=?`, "GitHub Actions deployment ledger", sourceID); err != nil {
		return deployment.SyncResult{}, fmt.Errorf("%w: name GitHub Actions deployment source: %w", errs.ErrUnavailable, err)
	}

	observationID, existing, err := saveGitHubActionsDeploymentFetch(ctx, tx, sourceID, ingestion.IngestionID, source, now)
	if err != nil {
		return deployment.SyncResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.SyncResult{}, fmt.Errorf("%w: commit GitHub Actions deployment sync: %w", errs.ErrUnavailable, err)
	}
	return deployment.SyncResult{Ingestion: ingestion, SourceObservationID: observationID, SourceExisting: existing}, nil
}

func saveGitHubActionsDeploymentFetch(ctx context.Context, tx *sql.Tx, sourceID, ingestionID string, source deployment.SourceResult, now time.Time) (string, bool, error) {
	var stored struct {
		id, ingestionID, repository, artifactName, artifactDigest, workflowHeadSHA string
		workflowRunID                                                              int64
	}
	err := tx.QueryRowContext(ctx, `SELECT id,ingestion_id,repository,artifact_name,artifact_digest,workflow_run_id,workflow_head_sha FROM github_actions_deployment_fetches WHERE source_id=? AND artifact_id=?`, sourceID, source.ArtifactID).Scan(&stored.id, &stored.ingestionID, &stored.repository, &stored.artifactName, &stored.artifactDigest, &stored.workflowRunID, &stored.workflowHeadSHA)
	if err == nil {
		if stored.ingestionID != ingestionID || stored.repository != source.Repository || stored.artifactName != source.ArtifactName || stored.artifactDigest != source.ArtifactDigest || stored.workflowRunID != source.WorkflowRunID || stored.workflowHeadSHA != source.WorkflowHeadSHA {
			return "", false, fmt.Errorf("%w: GitHub Actions artifact metadata changed", errs.ErrConflict)
		}
		return stored.id, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("%w: inspect GitHub Actions deployment fetch: %w", errs.ErrUnavailable, err)
	}
	id, err := identity.NewV7(now)
	if err != nil {
		return "", false, err
	}
	stamp := formatTime(now)
	_, err = tx.ExecContext(ctx, `INSERT INTO github_actions_deployment_fetches(id,source_id,ingestion_id,repository,artifact_name,artifact_id,artifact_digest,workflow_run_id,workflow_head_sha,artifact_created_at,artifact_updated_at,artifact_expires_at,observed_at,fetched_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, sourceID, ingestionID, source.Repository, source.ArtifactName, source.ArtifactID, source.ArtifactDigest, source.WorkflowRunID, source.WorkflowHeadSHA, formatTime(source.CreatedAt), formatTime(source.UpdatedAt), formatTime(source.ExpiresAt), formatTime(source.Snapshot.ObservedAt), stamp, stamp)
	if err != nil {
		return "", false, fmt.Errorf("%w: save GitHub Actions deployment fetch: %w", errs.ErrUnavailable, err)
	}
	return id, false, nil
}

func existingDeploymentIngestion(ctx context.Context, tx *sql.Tx, sourceID, sourceHash string) (deployment.IngestResult, bool, error) {
	var result deployment.IngestResult
	var warningsJSON string
	err := tx.QueryRowContext(ctx, `SELECT id,source_hash,format,records_seen,deployments_inserted,deployments_existing,warnings_json FROM deployment_ingestions WHERE source_id=? AND source_hash=?`, sourceID, sourceHash).Scan(&result.IngestionID, &result.SourceHash, &result.Format, &result.RecordsSeen, &result.DeploymentsInserted, &result.DeploymentsExisting, &warningsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return result, false, nil
	}
	if err != nil {
		return result, false, fmt.Errorf("%w: find deployment ingestion: %w", errs.ErrUnavailable, err)
	}
	if err := json.Unmarshal([]byte(warningsJSON), &result.Warnings); err != nil || result.Warnings == nil {
		return result, false, fmt.Errorf("%w: decode deployment ingestion warnings", errs.ErrUnavailable)
	}
	return result, true, nil
}

type deploymentResolution struct {
	serviceID, artifactID, commitID, commitSHA, status, confidence, basis string
	limitations                                                           []string
	evidence                                                              []deployment.Evidence
}

func resolveDeployment(ctx context.Context, tx *sql.Tx, record deployment.Record, now time.Time) (deploymentResolution, error) {
	result := deploymentResolution{status: "UNKNOWN", confidence: "UNKNOWN", basis: "immutable artifact and verified commit unresolved", limitations: []string{}, evidence: []deployment.Evidence{}}
	_ = tx.QueryRowContext(ctx, `SELECT id FROM services WHERE environment=? AND logical_key=?`, record.Environment, record.Service).Scan(&result.serviceID)
	artifactID, artifactState, err := resolveArtifact(ctx, tx, record)
	if err != nil {
		return result, err
	}
	commitID, commitState, err := resolveCommit(ctx, tx, record)
	if err != nil {
		return result, err
	}
	if artifactState == "contradicted" || commitState == "contradicted" {
		result.status, result.basis = "CONTRADICTED", "immutable identity or verified commit claim is contradictory"
		result.limitations = append(result.limitations, "ambiguous or contradictory deployment provenance")
		result.evidence = append(result.evidence, deployment.Evidence{Kind: "contradiction", Subject: "deployment", Claim: "Immutable artifact or verified commit claims could not be selected unambiguously.", Polarity: "contradicts", Source: "deployment_ledger"})
		return result, nil
	}
	result.artifactID, result.commitID = artifactID, commitID
	if commitID != "" {
		result.commitSHA = record.VCSRevision
	}
	future := record.DeployedAt.After(now)
	if future {
		result.limitations = append(result.limitations, "deployment timestamp is in the future relative to ingestion")
		result.basis = "deployment timestamp is in the future relative to ingestion"
		result.status, result.confidence = "UNKNOWN", "UNKNOWN"
	}
	switch {
	case future:
		// A future deployment may remain registered and retain resolved links, but
		// it cannot carry a provenance conclusion from this ingestion instant.
	case artifactID != "" && commitID != "" && !future:
		result.status, result.confidence, result.basis = "MATCHED", "HIGH", "verified commit and immutable artifact resolved"
	case artifactID != "" || commitID != "":
		result.status, result.confidence, result.basis = "PARTIAL", "MEDIUM", "one immutable provenance element resolved"
		result.limitations = append(result.limitations, "other immutable provenance element unresolved")
	default:
		result.limitations = append(result.limitations, "immutable artifact and verified commit are unresolved")
	}
	if artifactID != "" {
		result.evidence = append(result.evidence, deployment.Evidence{Kind: "identity", Subject: "artifact", Claim: "Immutable image identity matched an existing artifact.", Polarity: "supports", Source: "deployment_ledger"})
	}
	if commitID != "" {
		result.evidence = append(result.evidence, deployment.Evidence{Kind: "identity", Subject: "commit", Claim: "Verified commit revision matched an existing commit.", Polarity: "supports", Source: "deployment_ledger"})
	}
	if result.serviceID != "" {
		result.evidence = append(result.evidence, deployment.Evidence{Kind: "identity", Subject: "service", Claim: "Deployment service matched an existing service in the same environment.", Polarity: "supports", Source: "deployment_ledger"})
	}
	if len(result.evidence) == 0 {
		result.evidence = append(result.evidence, deployment.Evidence{Kind: "data_quality", Subject: "deployment", Claim: "No existing immutable artifact or verified commit could be resolved.", Polarity: "neutral", Source: "deployment_ledger"})
	}
	return result, nil
}

func resolveArtifact(ctx context.Context, tx *sql.Tx, record deployment.Record) (string, string, error) {
	candidates := map[string]struct{}{}
	collect := func(identity string) error {
		if identity == "" {
			return nil
		}
		rows, err := tx.QueryContext(ctx, `SELECT id FROM artifacts WHERE identity=? OR image_id=?`, identity, identity)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			candidates[id] = struct{}{}
		}
		return rows.Err()
	}
	if record.RepoDigest != nil {
		_, immutable, _ := strings.Cut(*record.RepoDigest, "@")
		if err := collect(immutable); err != nil {
			return "", "", fmt.Errorf("%w: resolve artifact digest: %w", errs.ErrUnavailable, err)
		}
	}
	if err := collect(record.ImageID); err != nil {
		return "", "", fmt.Errorf("%w: resolve artifact image: %w", errs.ErrUnavailable, err)
	}
	if len(candidates) > 1 {
		return "", "contradicted", nil
	}
	for id := range candidates {
		return id, "matched", nil
	}
	return "", "unresolved", nil
}

func resolveCommit(ctx context.Context, tx *sql.Tx, record deployment.Record) (string, string, error) {
	if !record.VCSRevisionVerified {
		return "", "unresolved", nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM commits WHERE sha=?`, record.VCSRevision)
	if err != nil {
		return "", "", fmt.Errorf("%w: resolve commit: %w", errs.ErrUnavailable, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", "", err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return "", "", err
	}
	if len(ids) > 1 {
		return "", "contradicted", nil
	}
	if len(ids) == 1 {
		return ids[0], "matched", nil
	}
	return "", "unresolved", nil
}

type deploymentCursor struct {
	Version   int    `json:"v"`
	StartedAt string `json:"t"`
	ID        string `json:"i"`
	Signature string `json:"s"`
}

func (s *Store) Deployments(ctx context.Context, query deployment.Query) (deployment.QueryResult, error) {
	if query.Limit < 1 || query.Limit > 1000 || !query.Until.After(query.Since) {
		return deployment.QueryResult{}, fmt.Errorf("%w: invalid deployment query", errs.ErrInvalid)
	}
	environment, err := identity.ValidEnvironment(query.Environment)
	if err != nil || environment != query.Environment {
		return deployment.QueryResult{}, fmt.Errorf("%w: invalid deployment environment", errs.ErrInvalid)
	}
	if query.Status != "" && !strings.Contains(" pending running succeeded failed cancelled rolled_back unknown ", " "+query.Status+" ") {
		return deployment.QueryResult{}, fmt.Errorf("%w: invalid deployment status", errs.ErrInvalid)
	}
	cursor, err := decodeDeploymentCursor(query)
	if err != nil {
		return deployment.QueryResult{}, err
	}
	result := deployment.QueryResult{Since: query.Since.UTC(), Until: query.Until.UTC(), Environment: environment, Items: []deployment.Deployment{}}
	statement := `SELECT d.id,d.external_id,d.environment,d.service_key,d.status,d.strategy,d.started_at,d.finished_at,d.artifact_id,d.artifact_repo_digest,d.artifact_image_id,d.commit_id,d.commit_sha,d.commit_verified,d.provenance_status,d.confidence_level,d.confidence_basis,d.limitations_json,(SELECT MIN(n.started_at) FROM deployments n WHERE n.environment=d.environment AND n.service_key=d.service_key AND n.started_at>d.started_at),EXISTS(SELECT 1 FROM deployments c WHERE c.environment=d.environment AND c.service_key=d.service_key AND c.started_at=d.started_at AND c.id<>d.id) FROM deployments d WHERE environment=? AND started_at>=? AND started_at<? AND (?='' OR service_key=?) AND (?='' OR status=?) AND (started_at<? OR (started_at=? AND id<?)) ORDER BY started_at DESC,id DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, statement, environment, formatTime(query.Since), formatTime(query.Until), query.Service, query.Service, query.Status, query.Status, cursor.StartedAt, cursor.StartedAt, cursor.ID, query.Limit+1)
	if err != nil {
		return result, fmt.Errorf("%w: query deployments: %w", errs.ErrUnavailable, err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var item deployment.Deployment
		var finished, artifactID, repoDigest, commitID, commitSHA, nextStarted sql.NullString
		var verified int
		var concurrent int
		var started, limitations string
		if err := rows.Scan(&item.ID, &item.ExternalID, &item.Environment, &item.Service, &item.Status, &item.Strategy, &started, &finished, &artifactID, &repoDigest, &item.Provenance.ImageID, &commitID, &commitSHA, &verified, &item.Provenance.Status, &item.Provenance.Confidence, &item.Provenance.Basis, &limitations, &nextStarted, &concurrent); err != nil {
			return result, fmt.Errorf("%w: scan deployment: %w", errs.ErrIncompatible, err)
		}
		item.StartedAt, err = parseTime(started)
		if err != nil {
			return result, err
		}
		if finished.Valid {
			value, e := parseTime(finished.String)
			if e != nil {
				return result, e
			}
			item.FinishedAt = &value
		}
		item.Provenance.ArtifactID = stringPointer(artifactID)
		item.Provenance.RepoDigest = stringPointer(repoDigest)
		item.Provenance.CommitID = stringPointer(commitID)
		item.Provenance.CommitSHA = stringPointer(commitSHA)
		item.Provenance.Verified = verified != 0
		item.Concurrent = concurrent != 0
		if nextStarted.Valid {
			value, parseErr := parseTime(nextStarted.String)
			if parseErr != nil {
				return result, parseErr
			}
			item.NextDeploymentAt = new(value)
		}
		if err := json.Unmarshal([]byte(limitations), &item.Provenance.Limitations); err != nil {
			return result, fmt.Errorf("%w: decode deployment limitations", errs.ErrIncompatible)
		}
		item.Provenance.Evidence = []deployment.Evidence{}
		result.Items = append(result.Items, item)
		ids = append(ids, item.ID)
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("%w: iterate deployments: %w", errs.ErrUnavailable, err)
	}
	if len(result.Items) > query.Limit {
		result.Items = result.Items[:query.Limit]
		ids = ids[:query.Limit]
		last := result.Items[len(result.Items)-1]
		result.NextCursor = encodeDeploymentCursor(query, last)
	}
	if len(ids) == 0 {
		return result, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	evidenceRows, err := s.db.QueryContext(ctx, `SELECT id,deployment_id,kind,subject,claim,polarity,source FROM deployment_evidence WHERE deployment_id IN (`+placeholders+`) ORDER BY deployment_id,ordinal`, stringSliceArgs(ids)...)
	if err != nil {
		return result, fmt.Errorf("%w: query deployment evidence: %w", errs.ErrUnavailable, err)
	}
	defer evidenceRows.Close()
	byID := map[string]int{}
	for index, item := range result.Items {
		byID[item.ID] = index
	}
	for evidenceRows.Next() {
		var parent string
		var evidence deployment.Evidence
		if err := evidenceRows.Scan(&evidence.ID, &parent, &evidence.Kind, &evidence.Subject, &evidence.Claim, &evidence.Polarity, &evidence.Source); err != nil {
			return result, err
		}
		result.Items[byID[parent]].Provenance.Evidence = append(result.Items[byID[parent]].Provenance.Evidence, evidence)
	}
	if err := evidenceRows.Err(); err != nil {
		return result, err
	}
	if err := s.attachRuntimeAssociations(ctx, &result); err != nil {
		return result, err
	}
	return result, nil
}

func (s *Store) attachRuntimeAssociations(ctx context.Context, result *deployment.QueryResult) error {
	if len(result.Items) == 0 {
		return nil
	}
	services := make([]string, 0, len(result.Items))
	seen := map[string]struct{}{}
	for _, item := range result.Items {
		if _, ok := seen[item.Service]; !ok {
			seen[item.Service] = struct{}{}
			services = append(services, item.Service)
		}
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(services)), ",")
	earliestStart := result.Items[0].StartedAt
	for _, item := range result.Items[1:] {
		if item.StartedAt.Before(earliestStart) {
			earliestStart = item.StartedAt
		}
	}
	args := []any{correlation.AlgorithmVersion, result.Environment, formatTime(result.Until), formatTime(earliestStart)}
	args = append(args, stringSliceArgs(services)...)
	args = append(args, deployment.MaxRuntimeCandidatesPerPage+1)
	rows, err := s.db.QueryContext(ctx, `SELECT r.id,r.source_id,r.external_id,s.logical_key,r.observed_at,r.valid_from,COALESCE(r.valid_to,''),r.state,r.health,COALESCE(r.image_id,''),COALESCE(r.image_digest,''),a.identity,COALESCE(cm.sha,'') FROM runtime_instances r JOIN services s ON s.id=r.service_id JOIN artifacts a ON a.id=r.artifact_id LEFT JOIN correlations c ON c.runtime_id=r.id AND c.is_current=1 AND c.algorithm_version=? LEFT JOIN commits cm ON cm.id=c.commit_id WHERE s.environment=? AND r.valid_from<? AND (r.valid_to IS NULL OR r.valid_to>?) AND s.logical_key IN (`+placeholders+`) ORDER BY s.logical_key,r.observed_at,r.id LIMIT ?`, args...)
	if err != nil {
		return fmt.Errorf("%w: query runtime association candidates", errs.ErrUnavailable)
	}
	defer rows.Close()
	byService := map[string][]deployment.RuntimeCandidate{}
	pageCandidateCount := 0
	for rows.Next() {
		var candidate deployment.RuntimeCandidate
		var service, observed, validFrom, validTo string
		if err := rows.Scan(&candidate.ID, &candidate.SourceID, &candidate.ExternalID, &service, &observed, &validFrom, &validTo, &candidate.State, &candidate.Health, &candidate.ImageID, &candidate.ImageDigest, &candidate.ArtifactIdentity, &candidate.CommitSHA); err != nil {
			return fmt.Errorf("%w: scan runtime association candidate", errs.ErrIncompatible)
		}
		candidate.ObservedAt, err = parseTime(observed)
		if err != nil {
			return err
		}
		candidate.ValidFrom, err = parseTime(validFrom)
		if err != nil {
			return err
		}
		if validTo != "" {
			value, parseErr := parseTime(validTo)
			if parseErr != nil {
				return parseErr
			}
			candidate.ValidTo = new(value)
		}
		byService[service] = append(byService[service], candidate)
		pageCandidateCount++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%w: iterate runtime association candidates", errs.ErrUnavailable)
	}
	pageCandidateLimitExceeded := pageCandidateCount > deployment.MaxRuntimeCandidatesPerPage
	for index := range result.Items {
		item := &result.Items[index]
		end, reason := runtimeConfirmationWindow(item.StartedAt, item.NextDeploymentAt, result.Until)
		candidates := []deployment.RuntimeCandidate{}
		for _, candidate := range byService[item.Service] {
			if !candidate.ObservedAt.Before(item.StartedAt) && candidate.ObservedAt.Before(end) {
				candidates = append(candidates, candidate)
			}
		}
		preexisting := false
		deploymentIDs := runtimeImmutableIDs(item.Provenance.RepoDigest, item.Provenance.ImageID)
		for _, candidate := range byService[item.Service] {
			if candidate.ObservedAt.Before(item.StartedAt) && runtimeCandidateValidAt(candidate, item.StartedAt) && runtimeIDsIntersect(deploymentIDs, runtimeImmutableIDs(nil, candidate.ImageID, candidate.ImageDigest, candidate.ArtifactIdentity)) {
				preexisting = true
			}
		}
		item.RuntimeAssociation = deployment.EvaluateRuntimeAssociation(deployment.RuntimeAssociationInput{DeploymentID: item.ID, ArtifactRepoDigest: item.Provenance.RepoDigest, ArtifactImageID: item.Provenance.ImageID, CommitSHA: item.Provenance.CommitSHA, CommitVerified: item.Provenance.Verified, Start: item.StartedAt, End: end, EndReason: reason, Concurrent: item.Concurrent, Future: item.StartedAt.After(result.Until), Preexisting: preexisting, CandidateLimitExceeded: pageCandidateLimitExceeded || len(candidates) > deployment.MaxRuntimeCandidatesPerDeployment, Candidates: candidates})
	}
	return nil
}

func runtimeConfirmationWindow(start time.Time, next *time.Time, until time.Time) (time.Time, string) {
	end, reason := start.Add(deployment.MaxRuntimeConfirmationDelay), "max_confirmation_delay"
	if !until.After(end) {
		end, reason = until, "query_until"
	}
	if next != nil && !next.After(end) {
		end, reason = *next, "next_deployment"
	}
	return end, reason
}

func runtimeCandidateValidAt(candidate deployment.RuntimeCandidate, at time.Time) bool {
	return !candidate.ValidFrom.After(at) && (candidate.ValidTo == nil || candidate.ValidTo.After(at))
}

func runtimeImmutableIDs(repo *string, values ...string) map[string]struct{} {
	result := map[string]struct{}{}
	if repo != nil {
		_, digest, ok := strings.Cut(*repo, "@")
		if ok {
			result[digest] = struct{}{}
		}
	}
	for _, value := range values {
		if value != "" {
			result[value] = struct{}{}
		}
	}
	return result
}
func runtimeIDsIntersect(left, right map[string]struct{}) bool {
	for value := range left {
		if _, ok := right[value]; ok {
			return true
		}
	}
	return false
}

func decodeDeploymentCursor(query deployment.Query) (deploymentCursor, error) {
	cursor := deploymentCursor{StartedAt: "9999-12-31T23:59:59.999999999Z", ID: "~"}
	if query.Cursor == "" {
		return cursor, nil
	}
	encoded, err := base64.RawURLEncoding.DecodeString(query.Cursor)
	if err != nil {
		return cursor, fmt.Errorf("%w: malformed deployment cursor", errs.ErrInvalid)
	}
	if err := json.Unmarshal(encoded, &cursor); err != nil || cursor.Version != 1 || cursor.Signature != deploymentSignature(query) {
		return cursor, fmt.Errorf("%w: incompatible deployment cursor", errs.ErrInvalid)
	}
	if _, err := parseTime(cursor.StartedAt); err != nil || cursor.ID == "" {
		return cursor, fmt.Errorf("%w: malformed deployment cursor", errs.ErrInvalid)
	}
	return cursor, nil
}
func deploymentSignature(query deployment.Query) string {
	value := query.Environment + "\x00" + query.Service + "\x00" + query.Status + "\x00" + formatTime(query.Since) + "\x00" + formatTime(query.Until)
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:])
}
func encodeDeploymentCursor(query deployment.Query, item deployment.Deployment) string {
	value, _ := json.Marshal(deploymentCursor{Version: 1, StartedAt: formatTime(item.StartedAt), ID: item.ID, Signature: deploymentSignature(query)})
	return base64.RawURLEncoding.EncodeToString(value)
}
func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullablePointer(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
func stringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return new(value.String)
}
func stringSliceArgs(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

var _ deployment.Store = (*Store)(nil)
