# ADR-015 — Phase 2A as an experimental exception

Status: accepted for Experiment 001 preparation

Phase 2A is authorized to make the topology component needed by Experiment 001 reproducible. [`Decision 001`](../decisions/001-directional-pilot-continuation.md) additionally authorizes a bounded `continue-for-learning` completion of Phase 2 for product learning with the reference application. Neither authorization is evidence for, or replaces, the pre-registered `go`, `pivot`, or `stop` decision. The authorization ends at the Phase 2 gate; Phase 3 remains blocked pending a new explicit decision.

The permitted work is limited to frozen offline traces, observed topology, bounded temporal aggregates, SQLite persistence, `prodmap graph`, evidence-based runtime-inventory/service integration, temporal queries, cardinality controls, and reference-application validation. OTLP metrics, logs, a live receiver in Prodmap, deployments, baselines, regressions, generic export, MCP, and causal scoring remain outside scope.
