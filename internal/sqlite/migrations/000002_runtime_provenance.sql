CREATE TABLE sources (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    name TEXT NOT NULL,
    instance_key TEXT NOT NULL,
    last_sync_at TEXT,
    last_status TEXT NOT NULL CHECK (last_status IN ('never', 'success', 'partial', 'failed')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (kind, instance_key)
);

CREATE TABLE repositories (
    id TEXT PRIMARY KEY,
    source_id TEXT NOT NULL REFERENCES sources(id),
    external_id TEXT NOT NULL,
    name TEXT NOT NULL,
    canonical_url TEXT,
    root_path_hash TEXT,
    observed_at TEXT NOT NULL,
    ingested_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (source_id, external_id)
);

CREATE TABLE commits (
    id TEXT PRIMARY KEY,
    repository_id TEXT NOT NULL REFERENCES repositories(id),
    sha TEXT NOT NULL CHECK (length(sha) IN (40, 64)),
    author_time TEXT,
    commit_time TEXT NOT NULL,
    subject TEXT NOT NULL,
    tree_sha TEXT,
    observed_at TEXT NOT NULL,
    ingested_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (repository_id, sha)
);

CREATE INDEX commits_repository_time_idx ON commits(repository_id, commit_time DESC);

CREATE TABLE artifacts (
    id TEXT PRIMARY KEY,
    source_id TEXT NOT NULL REFERENCES sources(id),
    kind TEXT NOT NULL CHECK (kind = 'container_image'),
    name TEXT NOT NULL,
    identity_kind TEXT NOT NULL CHECK (identity_kind IN ('repo_digest', 'image_id', 'mutable_tag')),
    identity TEXT NOT NULL,
    digest_algorithm TEXT,
    digest TEXT,
    image_id TEXT,
    observed_reference TEXT,
    oci_revision TEXT,
    oci_labels_json TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    ingested_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (source_id, identity_kind, identity)
);

CREATE TABLE artifact_aliases (
    artifact_id TEXT NOT NULL REFERENCES artifacts(id),
    alias TEXT NOT NULL,
    valid_from TEXT NOT NULL,
    valid_to TEXT,
    PRIMARY KEY (artifact_id, alias, valid_from),
    CHECK (valid_to IS NULL OR valid_to > valid_from)
);

CREATE TABLE services (
    id TEXT PRIMARY KEY,
    logical_key TEXT NOT NULL,
    environment TEXT NOT NULL,
    display_name TEXT NOT NULL,
    first_seen_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (environment, logical_key)
);

CREATE TABLE runtime_instances (
    id TEXT PRIMARY KEY,
    source_id TEXT NOT NULL REFERENCES sources(id),
    external_id TEXT NOT NULL,
    container_name TEXT NOT NULL,
    service_id TEXT NOT NULL REFERENCES services(id),
    artifact_id TEXT NOT NULL REFERENCES artifacts(id),
    runtime_kind TEXT NOT NULL CHECK (runtime_kind = 'docker_container'),
    state TEXT NOT NULL,
    health TEXT NOT NULL,
    restart_count INTEGER NOT NULL CHECK (restart_count >= 0),
    started_at TEXT,
    valid_from TEXT NOT NULL,
    valid_to TEXT,
    observed_at TEXT NOT NULL,
    ingested_at TEXT NOT NULL,
    image_reference TEXT NOT NULL,
    image_id TEXT,
    image_digest TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (source_id, external_id, observed_at),
    CHECK (valid_to IS NULL OR valid_to > valid_from)
);

CREATE INDEX runtime_service_validity_idx ON runtime_instances(service_id, valid_from DESC);
CREATE INDEX runtime_external_validity_idx ON runtime_instances(source_id, external_id, valid_from DESC);

CREATE TABLE correlations (
    id TEXT PRIMARY KEY,
    runtime_id TEXT NOT NULL REFERENCES runtime_instances(id),
    artifact_id TEXT NOT NULL REFERENCES artifacts(id),
    commit_id TEXT REFERENCES commits(id),
    relation_type TEXT NOT NULL CHECK (relation_type IN ('exact', 'inferred')),
    score REAL NOT NULL CHECK (score >= 0 AND score <= 1),
    level TEXT NOT NULL CHECK (level IN ('EXACT', 'HIGH', 'MEDIUM', 'LOW', 'UNKNOWN')),
    algorithm_version TEXT NOT NULL,
    conclusion TEXT NOT NULL,
    missing_json TEXT NOT NULL,
    warnings_json TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (runtime_id, algorithm_version)
);

CREATE TABLE evidence (
    id TEXT PRIMARY KEY,
    correlation_id TEXT NOT NULL REFERENCES correlations(id),
    kind TEXT NOT NULL CHECK (kind IN ('identity', 'temporal', 'data_quality', 'contradiction')),
    subject TEXT NOT NULL CHECK (length(subject) BETWEEN 1 AND 2048),
    claim TEXT NOT NULL CHECK (length(claim) BETWEEN 1 AND 4096),
    polarity TEXT NOT NULL CHECK (polarity IN ('supports', 'contradicts', 'neutral')),
    strength REAL NOT NULL CHECK (strength >= 0 AND strength <= 1),
    source TEXT NOT NULL CHECK (length(source) BETWEEN 1 AND 64),
    observed_at TEXT NOT NULL,
    details_json TEXT NOT NULL,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    created_at TEXT NOT NULL,
    UNIQUE (correlation_id, ordinal)
);

CREATE INDEX correlations_artifact_idx ON correlations(artifact_id, observed_at DESC);
CREATE INDEX correlations_commit_idx ON correlations(commit_id, observed_at DESC);
CREATE INDEX evidence_correlation_idx ON evidence(correlation_id, polarity, strength DESC);
