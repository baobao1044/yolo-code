// Tests for AST validation (File 10 §10.4): after applying, the engine
// validates the new content parses — a correctly-located patch can still
// produce broken code (half-deleted function, unbalanced brace). The spec's
// tree-sitter validator isn't in stdlib; this ticket uses go/parser for Go
// files (the canonical stdlib parser) and a brace-balance heuristic for other
// languages, with unknown extensions skipped (§10.4: don't block non-code).

package patch

import (
	"strings"
	"testing"
)

func TestValidateGoFileAcceptsValidCode(t *testing.T) {
	v := NewValidator()
	content := "package main\n\nfunc hello() {}\n"

	if err := v.Validate("main.go", content); err != nil {
		t.Fatalf("Validate(valid Go) = %v, want nil", err)
	}
}

func TestValidateGoFileRejectsSyntaxBreak(t *testing.T) {
	// A patch that left a half-deleted function → parser error.
	v := NewValidator()
	content := "package main\n\nfunc hello() {\n" // missing close brace

	err := v.Validate("main.go", content)
	if err == nil {
		t.Fatal("Validate(half-deleted Go func) = nil, want parse error")
	}
	if !strings.Contains(err.Error(), "ast") && !strings.Contains(err.Error(), "syntax") && !strings.Contains(err.Error(), "parse") {
		t.Errorf("err = %q, want it to flag a parse/syntax/ast problem", err.Error())
	}
}

func TestValidateRejectsUnbalancedBracesNonGo(t *testing.T) {
	// A non-Go file the heuristic handles: unbalanced braces → reject.
	v := NewValidator()
	content := "function hello() {\n  return 1;\n" // missing close brace

	err := v.Validate("app.js", content)
	if err == nil {
		t.Fatal("Validate(unbalanced braces .js) = nil, want brace-balance error")
	}
	if !strings.Contains(err.Error(), "brace") && !strings.Contains(err.Error(), "balance") && !strings.Contains(err.Error(), "unbalanced") {
		t.Errorf("err = %q, want a brace-balance message", err.Error())
	}
}

func TestValidateAcceptsBalancedNonGo(t *testing.T) {
	v := NewValidator()
	content := "function hello() {\n  return 1;\n}\n"

	if err := v.Validate("app.js", content); err != nil {
		t.Fatalf("Validate(balanced .js) = %v, want nil", err)
	}
}

func TestValidateSkipsUnknownExtension(t *testing.T) {
	// Unknown extension (not code) → skip, don't block (File 10 §10.4).
	v := NewValidator()

	if err := v.Validate("README.md", "# Title\nsome **broken markdown"); err != nil {
		t.Fatalf("Validate(.md) = %v, want nil (unknown language skipped)", err)
	}
	if err := v.Validate("notes.txt", "arbitrary text with ) unmatched ( paren"); err != nil {
		t.Fatalf("Validate(.txt) = %v, want nil (unknown language skipped)", err)
	}
}

func TestValidateGoFileRejectsBareExpression(t *testing.T) {
	// A Go file that's not a valid top-level decl → parser error.
	v := NewValidator()
	content := "package main\n\nfunc \n" // incomplete func decl

	if err := v.Validate("main.go", content); err == nil {
		t.Fatal("Validate(incomplete Go decl) = nil, want parse error")
	}
}
