// Package verify implements Layer 8 — the verification pipeline
// (AST→Format→Lint→TypeCheck→Build→Tests→PolicyCheck) that blocks bad
// patches before a task is marked done (File 09).
//
// Allowed imports: event, patch.
package verify
