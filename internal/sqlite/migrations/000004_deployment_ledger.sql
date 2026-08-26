CREATE TABLE deployment_ingestions (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id)=36 AND substr(id,9,1)='-' AND substr(id,14,1)='-' AND substr(id,19,1)='-' AND substr(id,24,1)='-' AND replace(id,'-','') NOT GLOB '*[^0-9a-f]*' AND substr(id,15,1)='7' AND substr(id,20,1) GLOB '[89ab]'),
    source_id TEXT NOT NULL REFERENCES sources(id),
    source_hash TEXT NOT NULL CHECK (source_hash GLOB 'sha256:*' AND length(source_hash)=71 AND substr(source_hash,8) NOT GLOB '*[^0-9a-f]*'),
    format TEXT NOT NULL CHECK (format='deployment-ledger-jsonl/v1'),
    records_seen INTEGER NOT NULL CHECK (records_seen >= 0),
    deployments_inserted INTEGER NOT NULL CHECK (deployments_inserted >= 0),
    deployments_existing INTEGER NOT NULL CHECK (deployments_existing >= 0),
    warnings_json TEXT NOT NULL CHECK (json_valid(warnings_json) AND json_type(warnings_json)='array'),
    observed_at TEXT NOT NULL,
    ingested_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(source_id, source_hash),
    CHECK(deployments_inserted + deployments_existing = records_seen),
    CHECK(length(observed_at)=30 AND observed_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',observed_at) IS NOT NULL AND strftime('%Y-%m-%dT%H:%M:%S',observed_at)=substr(observed_at,1,19) AND substr(observed_at,12,2) BETWEEN '00' AND '23' AND substr(observed_at,15,2) BETWEEN '00' AND '59' AND substr(observed_at,18,2) BETWEEN '00' AND '59'),
    CHECK(length(ingested_at)=30 AND ingested_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',ingested_at) IS NOT NULL AND strftime('%Y-%m-%dT%H:%M:%S',ingested_at)=substr(ingested_at,1,19) AND substr(ingested_at,12,2) BETWEEN '00' AND '23' AND substr(ingested_at,15,2) BETWEEN '00' AND '59' AND substr(ingested_at,18,2) BETWEEN '00' AND '59'),
    CHECK(length(created_at)=30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',created_at) IS NOT NULL AND strftime('%Y-%m-%dT%H:%M:%S',created_at)=substr(created_at,1,19) AND substr(created_at,12,2) BETWEEN '00' AND '23' AND substr(created_at,15,2) BETWEEN '00' AND '59' AND substr(created_at,18,2) BETWEEN '00' AND '59')
);
CREATE INDEX deployment_ingestions_source_hash_idx ON deployment_ingestions(source_id, source_hash);

CREATE TABLE deployments (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id)=36 AND substr(id,15,1)='7' AND replace(id,'-','') NOT GLOB '*[^0-9a-f]*'),
    source_id TEXT NOT NULL REFERENCES sources(id),
    first_ingestion_id TEXT NOT NULL REFERENCES deployment_ingestions(id),
    external_id TEXT NOT NULL CHECK(length(external_id) BETWEEN 1 AND 2048),
    environment TEXT NOT NULL CHECK(length(environment) BETWEEN 1 AND 128),
    service_key TEXT NOT NULL CHECK(length(service_key) BETWEEN 1 AND 255),
    service_id TEXT REFERENCES services(id),
    artifact_id TEXT REFERENCES artifacts(id),
    commit_id TEXT REFERENCES commits(id),
    status TEXT NOT NULL CHECK(status IN ('pending','running','succeeded','failed','cancelled','rolled_back','unknown')),
    strategy TEXT NOT NULL CHECK(strategy='unknown'),
    started_at TEXT NOT NULL,
    finished_at TEXT,
    artifact_repo_digest TEXT,
    artifact_image_id TEXT NOT NULL,
    commit_sha TEXT,
    commit_verified INTEGER NOT NULL CHECK(commit_verified IN (0,1)),
    record_fingerprint TEXT NOT NULL CHECK(record_fingerprint GLOB 'sha256-v1:*' AND length(record_fingerprint)=74),
    provenance_status TEXT NOT NULL CHECK(provenance_status IN ('MATCHED','PARTIAL','UNKNOWN','CONTRADICTED')),
    confidence_level TEXT NOT NULL CHECK(confidence_level IN ('HIGH','MEDIUM','LOW','UNKNOWN')),
    confidence_basis TEXT NOT NULL CHECK(length(confidence_basis) BETWEEN 1 AND 512),
    limitations_json TEXT NOT NULL CHECK(json_valid(limitations_json) AND json_type(limitations_json)='array'),
    algorithm_version TEXT NOT NULL CHECK(algorithm_version='deployment-ledger/v1'),
    observed_at TEXT NOT NULL,
    ingested_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(source_id, external_id, service_key),
    CHECK(length(started_at)=30 AND started_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',started_at) IS NOT NULL AND strftime('%Y-%m-%dT%H:%M:%S',started_at)=substr(started_at,1,19) AND substr(started_at,12,2) BETWEEN '00' AND '23' AND substr(started_at,15,2) BETWEEN '00' AND '59' AND substr(started_at,18,2) BETWEEN '00' AND '59'),
    CHECK(finished_at IS NULL OR (length(finished_at)=30 AND finished_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',finished_at) IS NOT NULL AND strftime('%Y-%m-%dT%H:%M:%S',finished_at)=substr(finished_at,1,19) AND substr(finished_at,12,2) BETWEEN '00' AND '23' AND substr(finished_at,15,2) BETWEEN '00' AND '59' AND substr(finished_at,18,2) BETWEEN '00' AND '59')),
    CHECK(length(observed_at)=30 AND observed_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',observed_at) IS NOT NULL AND strftime('%Y-%m-%dT%H:%M:%S',observed_at)=substr(observed_at,1,19) AND substr(observed_at,12,2) BETWEEN '00' AND '23' AND substr(observed_at,15,2) BETWEEN '00' AND '59' AND substr(observed_at,18,2) BETWEEN '00' AND '59'),
    CHECK(length(ingested_at)=30 AND ingested_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',ingested_at) IS NOT NULL AND strftime('%Y-%m-%dT%H:%M:%S',ingested_at)=substr(ingested_at,1,19) AND substr(ingested_at,12,2) BETWEEN '00' AND '23' AND substr(ingested_at,15,2) BETWEEN '00' AND '59' AND substr(ingested_at,18,2) BETWEEN '00' AND '59'),
    CHECK(length(created_at)=30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',created_at) IS NOT NULL AND strftime('%Y-%m-%dT%H:%M:%S',created_at)=substr(created_at,1,19) AND substr(created_at,12,2) BETWEEN '00' AND '23' AND substr(created_at,15,2) BETWEEN '00' AND '59' AND substr(created_at,18,2) BETWEEN '00' AND '59'),
    CHECK(length(updated_at)=30 AND updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',updated_at) IS NOT NULL AND strftime('%Y-%m-%dT%H:%M:%S',updated_at)=substr(updated_at,1,19) AND substr(updated_at,12,2) BETWEEN '00' AND '23' AND substr(updated_at,15,2) BETWEEN '00' AND '59' AND substr(updated_at,18,2) BETWEEN '00' AND '59')
);
CREATE INDEX deployments_environment_service_started_idx ON deployments(environment, service_key, started_at DESC, id DESC);
CREATE INDEX deployments_status_started_idx ON deployments(status, started_at DESC);
CREATE INDEX deployments_artifact_idx ON deployments(artifact_id);
CREATE INDEX deployments_commit_idx ON deployments(commit_id);
CREATE INDEX deployments_external_idx ON deployments(external_id);

CREATE TABLE deployment_evidence (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id)=36 AND substr(id,15,1)='7' AND replace(id,'-','') NOT GLOB '*[^0-9a-f]*'),
    deployment_id TEXT NOT NULL REFERENCES deployments(id),
    kind TEXT NOT NULL CHECK(kind IN ('identity','temporal','data_quality','contradiction')),
    subject TEXT NOT NULL CHECK(length(subject) BETWEEN 1 AND 2048),
    claim TEXT NOT NULL CHECK(length(claim) BETWEEN 1 AND 4096),
    polarity TEXT NOT NULL CHECK(polarity IN ('supports','contradicts','neutral')),
    source TEXT NOT NULL CHECK(length(source) BETWEEN 1 AND 64),
    observed_at TEXT NOT NULL,
    details_json TEXT NOT NULL CHECK(json_valid(details_json)),
    ordinal INTEGER NOT NULL CHECK(ordinal >= 0),
    created_at TEXT NOT NULL,
    UNIQUE(deployment_id, ordinal),
    CHECK(length(observed_at)=30 AND observed_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',observed_at) IS NOT NULL AND strftime('%Y-%m-%dT%H:%M:%S',observed_at)=substr(observed_at,1,19) AND substr(observed_at,12,2) BETWEEN '00' AND '23' AND substr(observed_at,15,2) BETWEEN '00' AND '59' AND substr(observed_at,18,2) BETWEEN '00' AND '59'),
    CHECK(length(created_at)=30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',created_at) IS NOT NULL AND strftime('%Y-%m-%dT%H:%M:%S',created_at)=substr(created_at,1,19) AND substr(created_at,12,2) BETWEEN '00' AND '23' AND substr(created_at,15,2) BETWEEN '00' AND '59' AND substr(created_at,18,2) BETWEEN '00' AND '59')
);
CREATE INDEX deployment_evidence_deployment_idx ON deployment_evidence(deployment_id, polarity, ordinal);
