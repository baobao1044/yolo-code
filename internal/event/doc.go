// Package event implements Layer 3 — the durable, FIFO, at-least-once event
// bus that the whole agent hangs off of (File 05).
//
// Architectural invariant (File 02 §2.2, File 15 §15.15.2): this package may
// import ONLY the standard library. It must not import any other layer.
package event
