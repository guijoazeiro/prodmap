# ADR-019 — Temporal endpoint context

Status: accepted for bounded Phase 2 continue-for-learning

## Decision

`prodmap endpoints` is a separate command. `services` remains the current inventory and runtime-association view; endpoint context is trace-derived, temporal, paginated, and therefore requires a distinct contract.

The source of truth is the materialized `endpoints` and `telemetry_windows` data. A result includes an endpoint only when it has at least one window active at `at`, with `[window_start, window_end)` semantics. Overlapping windows are returned separately and are never summed because they may describe overlapping source snapshots. Unknown coverage remains unknown.

No active window does not prove that a service, endpoint, or traffic is absent. Endpoint presence is observed evidence, not causality, and OTel never produces `EXACT` identity. Windows whose endpoint is null remain service-level aggregates and never become synthetic endpoints.

Pagination is deterministic by endpoint protocol, operation, and stable endpoint ID; windows are ordered by start, end, ingestion ID, and window ID. The cursor is opaque, versioned, and bound to service, environment, instant, and ordering. The query limits endpoints and returns all active windows for every endpoint on a page to contain cardinality.

Endpoint identity continues to use only safe OTel semantic conventions: HTTP method plus route template, or RPC service plus method. Concrete paths, URL values, identifiers, credentials, and arbitrary span names are not endpoint identities.

This decision adds neither baseline, regression, deployment intelligence, nor any Phase 3 capability.
