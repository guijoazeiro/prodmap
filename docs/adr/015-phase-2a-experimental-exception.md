# ADR-015 — Phase 2A as an experimental exception

Status: accepted for Experiment 001 preparation

Phase 2A is authorized only to make the topology component needed by Experiment 001 reproducible. It is not evidence for, and does not replace, the pre-registered `go`, `pivot`, or `stop` decision. Phase 2 as a whole remains blocked by that decision.

The permitted slice is limited to frozen offline traces, observed topology, bounded temporal aggregates, SQLite persistence, and `prodmap graph`. OTLP metrics, logs, a live receiver in Prodmap, deployments, baselines, regressions, generic export, MCP, and causal scoring remain outside scope.
