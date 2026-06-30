// Package scope implements the Scope layer — a sibling of the runtime layer
// (Layer 2) in the import matrix (File 15 §15.15.2). It owns Scope Loop
// Engineering: managing a task's scope through a ladder of levels —
// Task → Repo → File → Function → Edit → Verify — expanding or contracting
// based on verify feedback. The Controller gates tool access per level (the W2
// table) and suggests expansions and contractions per the W3 rules, while a
// Memory keeps the loop from revisiting dead ends. The package depends only on
// the event bus and the standard library.
package scope
