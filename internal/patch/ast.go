// AST validation (File 10 §10.4): after a patch applies, the engine re-parses
// the new content to catch a correctly-located patch that still broke syntax —
// a half-deleted function, an unbalanced brace. The spec's validator is
// tree-sitter, but the stdlib-only build has no tree-sitter (zero external
// deps, File 15 §15.15.1); this ticket reimplements the same guarantee with
// stdlib: go/parser for Go (the canonical Go parser) and a brace-balance
// heuristic for other code, with unknown extensions skipped (§10.4: "don't
// block non-code" — a patch to README.md or notes.txt is never rejected for
// "syntax").
//
// The heuristic is conservative about false positives: it skips string and
// comment text (a `}` inside a comment or string literal is not a structural
// brace) so a valid file is never rejected. Its job is to catch the common
// breakage a half-applied patch leaves — a missing closer — not to be a full
// syntax checker. Known limitation: a regex literal containing brackets (JS
// `/\{/`) is counted; if that ever blocks a real patch the composition root can
// swap in a real tree-sitter Validator (NewValidator is the seam).

package patch

import (
	"fmt"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
)

// Validator re-parses a file's content after a patch to reject syntax-breaking
// edits (File 10 §10.4). Go files use go/parser; other code uses a
// brace-balance heuristic; unknown extensions are skipped (Validate returns
// nil).
type Validator struct{}

// NewValidator returns a Validator. It is stateless; the constructor exists so
// the composition root can swap a richer validator (one wired to real
// tree-sitter) without changing call sites.
func NewValidator() *Validator {
	return &Validator{}
}

// Validate parses content and returns an error if it does not parse (File 10
// §10.4). The path selects the strategy by extension. Unknown extensions
// (markdown, prose, config the heuristic can't judge) are skipped: Validate
// returns nil — per §10.4 the engine never blocks a non-code patch.
func (v *Validator) Validate(path, content string) error {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go":
		return validateGo(path, content)
	default:
		rules, ok := codeRules[ext]
		if !ok {
			return nil // unknown extension: skip, don't block non-code (§10.4)
		}
		return checkBalance(path, content, rules)
	}
}

// validateGo runs the real Go parser (stdlib go/parser) and returns an error
// if the content does not parse. go/parser returns errors rather than panicking
// on bad syntax, but a recover guards against pathological input. The error is
// wrapped so it contains "parse" (the engine surfaces a clear cause and the
// model retries).
func validateGo(path, content string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("patch: parse panic in %s: %v", path, r)
		}
	}()
	fset := token.NewFileSet()
	if _, e := parser.ParseFile(fset, path, content, parser.AllErrors); e != nil {
		return fmt.Errorf("patch: parse error in %s: %v", path, e)
	}
	return nil
}

// langRules configures which comment styles checkBalance should skip for a
// language. String literals are skipped for every code language.
type langRules struct {
	lineSlash  bool // `//` line comment (C family, JS/TS, Rust, ...)
	blockSlash bool // `/* */` block comment
	hashLine   bool // `#` line comment (Python, Ruby, shell, ...)
	dashLine   bool // `--` line comment (SQL, ...)
}

// codeRules maps a code extension to its comment styles. An extension not
// listed here is treated as non-code and skipped (§10.4). `.go` is handled by
// validateGo and intentionally absent.
var codeRules = map[string]langRules{
	// C family and descendants: `//` and `/* */`.
	".c":     {lineSlash: true, blockSlash: true},
	".h":     {lineSlash: true, blockSlash: true},
	".cpp":   {lineSlash: true, blockSlash: true},
	".cc":    {lineSlash: true, blockSlash: true},
	".cxx":   {lineSlash: true, blockSlash: true},
	".hpp":   {lineSlash: true, blockSlash: true},
	".hxx":   {lineSlash: true, blockSlash: true},
	".java":  {lineSlash: true, blockSlash: true},
	".kt":    {lineSlash: true, blockSlash: true},
	".scala": {lineSlash: true, blockSlash: true},
	".cs":    {lineSlash: true, blockSlash: true},
	".rs":    {lineSlash: true, blockSlash: true},
	".swift": {lineSlash: true, blockSlash: true},
	".php":   {lineSlash: true, blockSlash: true},
	".css":   {blockSlash: true}, // standard CSS has `/* */` only
	".scss":  {lineSlash: true, blockSlash: true},
	".less":  {lineSlash: true, blockSlash: true},
	// JS family.
	".js":  {lineSlash: true, blockSlash: true},
	".mjs": {lineSlash: true, blockSlash: true},
	".cjs": {lineSlash: true, blockSlash: true},
	".jsx": {lineSlash: true, blockSlash: true},
	".ts":  {lineSlash: true, blockSlash: true},
	".tsx": {lineSlash: true, blockSlash: true},
	// Hash-comment languages.
	".py":   {hashLine: true},
	".rb":   {hashLine: true},
	".sh":   {hashLine: true},
	".bash": {hashLine: true},
	".zsh":  {hashLine: true},
	".pl":   {hashLine: true},
	".r":    {hashLine: true},
	// SQL: `--` and `/* */`.
	".sql": {dashLine: true, blockSlash: true},
	// JSON: no comments, just strings.
	".json": {},
}

// checkBalance is the brace-balance heuristic for non-Go code (File 10 §10.4).
// It scans content skipping string and comment text, tracking a stack of
// openers; a closer with no matching opener, a mismatched closer, or a stack
// left non-empty at EOF is reported as unbalanced. The message names the file
// and contains "unbalanced"/"brace" so the engine can surface a clear cause.
//
// It catches the breakage a half-applied patch typically leaves — a missing
// closer — without a full grammar. False positives (rejecting valid code) are
// avoided by skipping strings/comments; false negatives (missing a real syntax
// error) are accepted for a heuristic and never corrupt data.
func checkBalance(path, content string, rules langRules) error {
	var stack []byte
	i := 0
	n := len(content)
	for i < n {
		c := content[i]

		// `//` line comment.
		if rules.lineSlash && c == '/' && i+1 < n && content[i+1] == '/' {
			i += 2
			for i < n && content[i] != '\n' {
				i++
			}
			continue
		}
		// `/* */` block comment.
		if rules.blockSlash && c == '/' && i+1 < n && content[i+1] == '*' {
			i += 2
			for i+1 < n && !(content[i] == '*' && content[i+1] == '/') {
				i++
			}
			i += 2
			continue
		}
		// `#` line comment.
		if rules.hashLine && c == '#' {
			for i < n && content[i] != '\n' {
				i++
			}
			continue
		}
		// `--` line comment.
		if rules.dashLine && c == '-' && i+1 < n && content[i+1] == '-' {
			i += 2
			for i < n && content[i] != '\n' {
				i++
			}
			continue
		}
		// String literal (single, double, or backtick). Backtick strings may
		// span lines (JS template literals); single/double close on the line.
		if c == '"' || c == '\'' || c == '`' {
			quote := c
			i++
			for i < n && content[i] != quote {
				if content[i] == '\\' && i+1 < n {
					i += 2
					continue
				}
				if content[i] == '\n' && quote != '`' {
					break // unterminated on the line; resume scanning
				}
				i++
			}
			i++ // consume the closing quote (or move past the newline)
			continue
		}
		switch c {
		case '(', '{', '[':
			stack = append(stack, c)
		case ')', '}', ']':
			if len(stack) == 0 {
				return fmt.Errorf("patch: unbalanced braces in %s: unexpected '%c'", path, c)
			}
			open := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if !bracketsMatch(open, c) {
				return fmt.Errorf("patch: unbalanced braces in %s: '%c' does not match '%c'", path, c, open)
			}
		}
		i++
	}
	if len(stack) > 0 {
		return fmt.Errorf("patch: unbalanced braces in %s: %d unclosed bracket(s)", path, len(stack))
	}
	return nil
}

// bracketsMatch reports whether close is the matching closer for open.
func bracketsMatch(open, close byte) bool {
	switch open {
	case '(':
		return close == ')'
	case '{':
		return close == '}'
	case '[':
		return close == ']'
	}
	return false
}
