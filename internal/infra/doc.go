// Package infra implements Layer 12 — cross-cutting infrastructure:
// OpenTelemetry traces, metrics, structured logs, Sentry, secrets redaction,
// permissions, rate limiting, and the cost ledger (File 13).
//
// Architectural invariant (File 13 §13.1.2): infra is read-only with respect
// to agent behavior — it may import `event` (and observability SDKs) but NO
// other layer package. It never drives the agent except via `cost.*` events.
package infra
