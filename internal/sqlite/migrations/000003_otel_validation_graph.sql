CREATE TABLE telemetry_ingestions (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 36 AND substr(id,9,1)='-' AND substr(id,14,1)='-' AND substr(id,19,1)='-' AND substr(id,24,1)='-' AND length(replace(id,'-',''))=32 AND replace(id,'-','') NOT GLOB '*[^0-9a-f]*' AND substr(id, 15, 1) = '7' AND substr(id, 20, 1) GLOB '[89ab]'),
    source_id TEXT NOT NULL REFERENCES sources(id),
    source_hash TEXT NOT NULL CHECK (source_hash GLOB 'sha256:*' AND length(source_hash) = 71 AND substr(source_hash, 8) NOT GLOB '*[^0-9a-f]*'),
    environment TEXT NOT NULL CHECK (length(environment) BETWEEN 1 AND 128),
    window_start TEXT NOT NULL CHECK (length(window_start)=30 AND window_start GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',window_start)=substr(window_start,1,19)),
    window_end TEXT NOT NULL CHECK (length(window_end)=30 AND window_end GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',window_end)=substr(window_end,1,19) AND window_end > window_start),
    observed_at TEXT NOT NULL CHECK (length(observed_at)=30 AND observed_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',observed_at)=substr(observed_at,1,19)),
    ingested_at TEXT NOT NULL CHECK (length(ingested_at)=30 AND ingested_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',ingested_at)=substr(ingested_at,1,19)),
    lines INTEGER NOT NULL CHECK (lines >= 0),
    resource_spans INTEGER NOT NULL CHECK (resource_spans >= 0),
    spans_seen INTEGER NOT NULL CHECK (spans_seen >= 0),
    spans_accepted INTEGER NOT NULL CHECK (spans_accepted >= 0),
    spans_ignored INTEGER NOT NULL CHECK (spans_ignored >= 0),
    services_count INTEGER NOT NULL CHECK (services_count >= 0),
    endpoints_count INTEGER NOT NULL CHECK (endpoints_count >= 0),
    dependencies_count INTEGER NOT NULL CHECK (dependencies_count >= 0),
    observations_count INTEGER NOT NULL CHECK (observations_count >= 0),
    telemetry_windows_count INTEGER NOT NULL CHECK (telemetry_windows_count >= 0),
    warnings_json TEXT NOT NULL CHECK (json_valid(warnings_json) AND json_type(warnings_json) = 'array'),
    created_at TEXT NOT NULL CHECK (length(created_at)=30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',created_at)=substr(created_at,1,19)),
    UNIQUE (source_id, environment, window_start, window_end),
    CHECK (strftime('%Y-%m-%dT%H:%M:%S',window_start) IS NOT NULL AND substr(window_start,12,2) BETWEEN '00' AND '23' AND substr(window_start,15,2) BETWEEN '00' AND '59' AND substr(window_start,18,2) BETWEEN '00' AND '59'),
    CHECK (strftime('%Y-%m-%dT%H:%M:%S',window_end) IS NOT NULL AND substr(window_end,12,2) BETWEEN '00' AND '23' AND substr(window_end,15,2) BETWEEN '00' AND '59' AND substr(window_end,18,2) BETWEEN '00' AND '59'),
    CHECK (strftime('%Y-%m-%dT%H:%M:%S',observed_at) IS NOT NULL AND substr(observed_at,12,2) BETWEEN '00' AND '23' AND substr(observed_at,15,2) BETWEEN '00' AND '59' AND substr(observed_at,18,2) BETWEEN '00' AND '59'),
    CHECK (strftime('%Y-%m-%dT%H:%M:%S',ingested_at) IS NOT NULL AND substr(ingested_at,12,2) BETWEEN '00' AND '23' AND substr(ingested_at,15,2) BETWEEN '00' AND '59' AND substr(ingested_at,18,2) BETWEEN '00' AND '59'),
    CHECK (strftime('%Y-%m-%dT%H:%M:%S',created_at) IS NOT NULL AND substr(created_at,12,2) BETWEEN '00' AND '23' AND substr(created_at,15,2) BETWEEN '00' AND '59' AND substr(created_at,18,2) BETWEEN '00' AND '59')
);

CREATE INDEX telemetry_ingestions_window_idx ON telemetry_ingestions(environment, window_start, window_end);
CREATE INDEX telemetry_ingestions_hash_idx ON telemetry_ingestions(source_id, source_hash);

CREATE TABLE endpoints (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 36 AND substr(id,9,1)='-' AND substr(id,14,1)='-' AND substr(id,19,1)='-' AND substr(id,24,1)='-' AND length(replace(id,'-',''))=32 AND replace(id,'-','') NOT GLOB '*[^0-9a-f]*' AND substr(id, 15, 1) = '7' AND substr(id, 20, 1) GLOB '[89ab]'),
    service_id TEXT NOT NULL REFERENCES services(id),
    protocol TEXT NOT NULL CHECK (protocol IN ('http', 'grpc', 'rpc')),
    operation TEXT NOT NULL CHECK (length(operation) BETWEEN 1 AND 1024 AND instr(operation, char(0)) = 0),
    route_template TEXT CHECK (route_template IS NULL OR instr(route_template, char(0)) = 0),
    first_seen_at TEXT NOT NULL CHECK (length(first_seen_at)=30 AND first_seen_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',first_seen_at)=substr(first_seen_at,1,19)),
    last_seen_at TEXT NOT NULL CHECK (length(last_seen_at)=30 AND last_seen_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',last_seen_at)=substr(last_seen_at,1,19)),
    created_at TEXT NOT NULL CHECK (length(created_at)=30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',created_at)=substr(created_at,1,19)),
    updated_at TEXT NOT NULL CHECK (length(updated_at)=30 AND updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',updated_at)=substr(updated_at,1,19)),
    UNIQUE (service_id, protocol, operation),
    CHECK (last_seen_at >= first_seen_at),
    CHECK (strftime('%Y-%m-%dT%H:%M:%S',first_seen_at) IS NOT NULL AND substr(first_seen_at,12,2) BETWEEN '00' AND '23' AND substr(first_seen_at,15,2) BETWEEN '00' AND '59' AND substr(first_seen_at,18,2) BETWEEN '00' AND '59'),
    CHECK (strftime('%Y-%m-%dT%H:%M:%S',last_seen_at) IS NOT NULL AND substr(last_seen_at,12,2) BETWEEN '00' AND '23' AND substr(last_seen_at,15,2) BETWEEN '00' AND '59' AND substr(last_seen_at,18,2) BETWEEN '00' AND '59'),
    CHECK (strftime('%Y-%m-%dT%H:%M:%S',created_at) IS NOT NULL AND substr(created_at,12,2) BETWEEN '00' AND '23' AND substr(created_at,15,2) BETWEEN '00' AND '59' AND substr(created_at,18,2) BETWEEN '00' AND '59'),
    CHECK (strftime('%Y-%m-%dT%H:%M:%S',updated_at) IS NOT NULL AND substr(updated_at,12,2) BETWEEN '00' AND '23' AND substr(updated_at,15,2) BETWEEN '00' AND '59' AND substr(updated_at,18,2) BETWEEN '00' AND '59')
);

CREATE INDEX endpoints_service_idx ON endpoints(service_id, last_seen_at DESC);

CREATE TABLE dependencies (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 36 AND substr(id,9,1)='-' AND substr(id,14,1)='-' AND substr(id,19,1)='-' AND substr(id,24,1)='-' AND length(replace(id,'-',''))=32 AND replace(id,'-','') NOT GLOB '*[^0-9a-f]*' AND substr(id, 15, 1) = '7' AND substr(id, 20, 1) GLOB '[89ab]'),
    environment TEXT NOT NULL CHECK (length(environment) BETWEEN 1 AND 128),
    kind TEXT NOT NULL CHECK (kind IN ('service', 'database', 'cache', 'queue', 'external_api', 'other')),
    logical_key TEXT NOT NULL CHECK (length(logical_key) BETWEEN 1 AND 1024 AND instr(logical_key, char(0)) = 0),
    display_name TEXT NOT NULL CHECK (length(display_name) BETWEEN 1 AND 1024 AND instr(display_name, char(0)) = 0),
    first_seen_at TEXT NOT NULL CHECK (length(first_seen_at)=30 AND first_seen_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',first_seen_at)=substr(first_seen_at,1,19)),
    last_seen_at TEXT NOT NULL CHECK (length(last_seen_at)=30 AND last_seen_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',last_seen_at)=substr(last_seen_at,1,19)),
    created_at TEXT NOT NULL CHECK (length(created_at)=30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',created_at)=substr(created_at,1,19)),
    updated_at TEXT NOT NULL CHECK (length(updated_at)=30 AND updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',updated_at)=substr(updated_at,1,19)),
    UNIQUE (environment, kind, logical_key),
    CHECK (last_seen_at >= first_seen_at),
    CHECK (strftime('%Y-%m-%dT%H:%M:%S',first_seen_at) IS NOT NULL AND substr(first_seen_at,12,2) BETWEEN '00' AND '23' AND substr(first_seen_at,15,2) BETWEEN '00' AND '59' AND substr(first_seen_at,18,2) BETWEEN '00' AND '59'),
    CHECK (strftime('%Y-%m-%dT%H:%M:%S',last_seen_at) IS NOT NULL AND substr(last_seen_at,12,2) BETWEEN '00' AND '23' AND substr(last_seen_at,15,2) BETWEEN '00' AND '59' AND substr(last_seen_at,18,2) BETWEEN '00' AND '59'),
    CHECK (strftime('%Y-%m-%dT%H:%M:%S',created_at) IS NOT NULL AND substr(created_at,12,2) BETWEEN '00' AND '23' AND substr(created_at,15,2) BETWEEN '00' AND '59' AND substr(created_at,18,2) BETWEEN '00' AND '59'),
    CHECK (strftime('%Y-%m-%dT%H:%M:%S',updated_at) IS NOT NULL AND substr(updated_at,12,2) BETWEEN '00' AND '23' AND substr(updated_at,15,2) BETWEEN '00' AND '59' AND substr(updated_at,18,2) BETWEEN '00' AND '59')
);

CREATE TABLE topology_evidence (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 36 AND substr(id,9,1)='-' AND substr(id,14,1)='-' AND substr(id,19,1)='-' AND substr(id,24,1)='-' AND length(replace(id,'-',''))=32 AND replace(id,'-','') NOT GLOB '*[^0-9a-f]*' AND substr(id, 15, 1) = '7' AND substr(id, 20, 1) GLOB '[89ab]'),
    ingestion_id TEXT NOT NULL REFERENCES telemetry_ingestions(id),
    fingerprint_version TEXT NOT NULL CHECK (fingerprint_version = 'sha256-v1'),
    fingerprint TEXT NOT NULL CHECK (fingerprint GLOB 'sha256-v1:*' AND length(fingerprint) = 74 AND substr(fingerprint, 11) NOT GLOB '*[^0-9a-f]*'),
    claim TEXT NOT NULL CHECK (length(claim) BETWEEN 1 AND 512),
    observed_at TEXT NOT NULL CHECK (length(observed_at)=30 AND observed_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',observed_at)=substr(observed_at,1,19)),
    created_at TEXT NOT NULL CHECK (length(created_at)=30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',created_at)=substr(created_at,1,19)),
    UNIQUE (ingestion_id, fingerprint),
    CHECK (strftime('%Y-%m-%dT%H:%M:%S',observed_at) IS NOT NULL AND substr(observed_at,12,2) BETWEEN '00' AND '23' AND substr(observed_at,15,2) BETWEEN '00' AND '59' AND substr(observed_at,18,2) BETWEEN '00' AND '59'),
    CHECK (strftime('%Y-%m-%dT%H:%M:%S',created_at) IS NOT NULL AND substr(created_at,12,2) BETWEEN '00' AND '23' AND substr(created_at,15,2) BETWEEN '00' AND '59' AND substr(created_at,18,2) BETWEEN '00' AND '59')
);

CREATE INDEX topology_evidence_ingestion_idx ON topology_evidence(ingestion_id, observed_at);

CREATE TABLE service_dependency_observations (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 36 AND substr(id,9,1)='-' AND substr(id,14,1)='-' AND substr(id,19,1)='-' AND substr(id,24,1)='-' AND length(replace(id,'-',''))=32 AND replace(id,'-','') NOT GLOB '*[^0-9a-f]*' AND substr(id, 15, 1) = '7' AND substr(id, 20, 1) GLOB '[89ab]'),
    ingestion_id TEXT NOT NULL REFERENCES telemetry_ingestions(id),
    from_service_id TEXT NOT NULL REFERENCES services(id),
    origin_endpoint_id TEXT REFERENCES endpoints(id),
    dependency_id TEXT NOT NULL REFERENCES dependencies(id),
    target_service_id TEXT REFERENCES services(id),
    window_start TEXT NOT NULL CHECK (length(window_start)=30 AND window_start GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',window_start)=substr(window_start,1,19)),
    window_end TEXT NOT NULL CHECK (length(window_end)=30 AND window_end GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',window_end)=substr(window_end,1,19) AND window_end > window_start),
    request_count INTEGER NOT NULL CHECK (request_count >= 0),
    error_count INTEGER NOT NULL CHECK (error_count >= 0 AND error_count <= request_count),
    duration_sum_ns INTEGER NOT NULL CHECK (duration_sum_ns >= 0),
    relation_type TEXT NOT NULL CHECK (relation_type = 'OBSERVED'),
    confidence_level TEXT NOT NULL CHECK (confidence_level IN ('HIGH', 'MEDIUM', 'LOW')),
    confidence_basis TEXT NOT NULL CHECK (length(confidence_basis) BETWEEN 1 AND 512),
    algorithm_version TEXT NOT NULL CHECK (algorithm_version = 'otel-topology/v1'),
    evidence_ids_json TEXT NOT NULL CHECK (json_valid(evidence_ids_json) AND json_type(evidence_ids_json) = 'array'),
    limitations_json TEXT NOT NULL CHECK (json_valid(limitations_json) AND json_type(limitations_json) = 'array'),
    observed_at TEXT NOT NULL CHECK (length(observed_at)=30 AND observed_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',observed_at)=substr(observed_at,1,19)),
    ingested_at TEXT NOT NULL CHECK (length(ingested_at)=30 AND ingested_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',ingested_at)=substr(ingested_at,1,19)),
    created_at TEXT NOT NULL CHECK (length(created_at)=30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',created_at)=substr(created_at,1,19)),
    UNIQUE (ingestion_id, from_service_id, origin_endpoint_id, dependency_id),
    CHECK (strftime('%Y-%m-%dT%H:%M:%S',window_start) IS NOT NULL AND substr(window_start,12,2) BETWEEN '00' AND '23' AND substr(window_start,15,2) BETWEEN '00' AND '59' AND substr(window_start,18,2) BETWEEN '00' AND '59'),
    CHECK (strftime('%Y-%m-%dT%H:%M:%S',window_end) IS NOT NULL AND substr(window_end,12,2) BETWEEN '00' AND '23' AND substr(window_end,15,2) BETWEEN '00' AND '59' AND substr(window_end,18,2) BETWEEN '00' AND '59'),
    CHECK (strftime('%Y-%m-%dT%H:%M:%S',observed_at) IS NOT NULL AND substr(observed_at,12,2) BETWEEN '00' AND '23' AND substr(observed_at,15,2) BETWEEN '00' AND '59' AND substr(observed_at,18,2) BETWEEN '00' AND '59'),
    CHECK (strftime('%Y-%m-%dT%H:%M:%S',ingested_at) IS NOT NULL AND substr(ingested_at,12,2) BETWEEN '00' AND '23' AND substr(ingested_at,15,2) BETWEEN '00' AND '59' AND substr(ingested_at,18,2) BETWEEN '00' AND '59'),
    CHECK (strftime('%Y-%m-%dT%H:%M:%S',created_at) IS NOT NULL AND substr(created_at,12,2) BETWEEN '00' AND '23' AND substr(created_at,15,2) BETWEEN '00' AND '59' AND substr(created_at,18,2) BETWEEN '00' AND '59')
);

CREATE UNIQUE INDEX service_dependency_observations_identity_idx
ON service_dependency_observations(ingestion_id, from_service_id, COALESCE(origin_endpoint_id, ''), dependency_id);
CREATE INDEX service_dependency_observations_from_window_idx
ON service_dependency_observations(from_service_id, window_start, window_end);
CREATE INDEX service_dependency_observations_dependency_idx
ON service_dependency_observations(dependency_id, window_start, window_end);
CREATE INDEX service_dependency_observations_target_idx
ON service_dependency_observations(target_service_id, window_start, window_end);

CREATE TABLE telemetry_windows (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 36 AND substr(id,9,1)='-' AND substr(id,14,1)='-' AND substr(id,19,1)='-' AND substr(id,24,1)='-' AND length(replace(id,'-',''))=32 AND replace(id,'-','') NOT GLOB '*[^0-9a-f]*' AND substr(id, 15, 1) = '7' AND substr(id, 20, 1) GLOB '[89ab]'),
    ingestion_id TEXT NOT NULL REFERENCES telemetry_ingestions(id),
    service_id TEXT NOT NULL REFERENCES services(id),
    endpoint_id TEXT REFERENCES endpoints(id),
    window_start TEXT NOT NULL CHECK (length(window_start)=30 AND window_start GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',window_start)=substr(window_start,1,19)),
    window_end TEXT NOT NULL CHECK (length(window_end)=30 AND window_end GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',window_end)=substr(window_end,1,19) AND window_end > window_start),
    request_count INTEGER NOT NULL CHECK (request_count >= 0),
    error_count INTEGER NOT NULL CHECK (error_count >= 0 AND error_count <= request_count),
    duration_sum_ns INTEGER NOT NULL CHECK (duration_sum_ns >= 0),
    p50_ns INTEGER NOT NULL CHECK (p50_ns >= 0),
    p95_ns INTEGER NOT NULL CHECK (p95_ns >= p50_ns),
    p99_ns INTEGER NOT NULL CHECK (p99_ns >= p95_ns),
    observed_at TEXT NOT NULL CHECK (length(observed_at)=30 AND observed_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',observed_at)=substr(observed_at,1,19)),
    ingested_at TEXT NOT NULL CHECK (length(ingested_at)=30 AND ingested_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',ingested_at)=substr(ingested_at,1,19)),
    is_complete INTEGER NOT NULL CHECK (is_complete IN (0, 1)),
    coverage_ratio REAL CHECK (coverage_ratio >= 0 AND coverage_ratio <= 1),
    algorithm_version TEXT NOT NULL CHECK (algorithm_version = 'otel-window/v1'),
    created_at TEXT NOT NULL CHECK (length(created_at)=30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%dT%H:%M:%S',created_at)=substr(created_at,1,19)),
    UNIQUE (ingestion_id, service_id, endpoint_id),
    CHECK (strftime('%Y-%m-%dT%H:%M:%S',window_start) IS NOT NULL AND substr(window_start,12,2) BETWEEN '00' AND '23' AND substr(window_start,15,2) BETWEEN '00' AND '59' AND substr(window_start,18,2) BETWEEN '00' AND '59'),
    CHECK (strftime('%Y-%m-%dT%H:%M:%S',window_end) IS NOT NULL AND substr(window_end,12,2) BETWEEN '00' AND '23' AND substr(window_end,15,2) BETWEEN '00' AND '59' AND substr(window_end,18,2) BETWEEN '00' AND '59'),
    CHECK (strftime('%Y-%m-%dT%H:%M:%S',observed_at) IS NOT NULL AND substr(observed_at,12,2) BETWEEN '00' AND '23' AND substr(observed_at,15,2) BETWEEN '00' AND '59' AND substr(observed_at,18,2) BETWEEN '00' AND '59'),
    CHECK (strftime('%Y-%m-%dT%H:%M:%S',ingested_at) IS NOT NULL AND substr(ingested_at,12,2) BETWEEN '00' AND '23' AND substr(ingested_at,15,2) BETWEEN '00' AND '59' AND substr(ingested_at,18,2) BETWEEN '00' AND '59'),
    CHECK (strftime('%Y-%m-%dT%H:%M:%S',created_at) IS NOT NULL AND substr(created_at,12,2) BETWEEN '00' AND '23' AND substr(created_at,15,2) BETWEEN '00' AND '59' AND substr(created_at,18,2) BETWEEN '00' AND '59')
);

CREATE UNIQUE INDEX telemetry_windows_identity_idx
ON telemetry_windows(ingestion_id, service_id, COALESCE(endpoint_id, ''));
CREATE INDEX telemetry_windows_service_interval_idx ON telemetry_windows(service_id, window_start, window_end);
CREATE INDEX telemetry_windows_endpoint_interval_idx ON telemetry_windows(endpoint_id, window_start, window_end);
