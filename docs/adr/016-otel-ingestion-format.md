# ADR-016 — Frozen OTel ingestion format

Status: accepted for Phase 2A

The only Phase 2A input is canonical OTLP/JSON with exactly one traces export request per line in an immutable local snapshot. The format name is `otlp-jsonl`; it contains traces only and is compatible with the OpenTelemetry Collector `file` exporter using `format: json`, `compression: none`, and `append: true`.

Canonical OTLP/JSON uses lower-camel-case field names, 32-character hexadecimal trace IDs, 16-character hexadecimal span and parent-span IDs, numeric enum values, and decimal strings for 64-bit integers. The Protobuf JSON dialect that represents IDs as Base64 or enums as symbolic names is not accepted implicitly.

Prodmap uses `go.opentelemetry.io/collector/pdata` v1.42.0, aligned with the pinned `otel/opentelemetry-collector-contrib` 0.136.0. The official `ptrace.JSONUnmarshaler` decodes OTLP/JSON into pdata. An internal bridge serializes pdata as OTLP Protobuf binary and deserializes that binary into the existing `go.opentelemetry.io/proto/otlp` domain; no JSON is regenerated with `protojson`.

Unknown OTLP/JSON fields are ignored for forward compatibility, as required for receivers. They remain outside the Prodmap allowlist and are discarded before persistence. Structural violations of the accepted dialect, including Base64 identifiers, symbolic enums, malformed identifiers, non-decimal timestamp strings, and invalid known enum values, are rejected with sanitized errors.

Parsing, structural validation, allowlist sanitization, hashing, and bounded aggregation finish before SQLite starts a transaction. Existing file-size, line, span, link, cardinality, timeout, and cancellation limits remain unchanged. Phase 2A has no OTLP HTTP/gRPC receiver.
