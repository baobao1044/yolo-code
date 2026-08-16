// Compound-command regression for the classifier (defect 4.3). The classifier
// used to look at the FIRST token only and then hand the whole string to
// `sh -c`, so `echo hi; rm -rf /` was classified by `echo` and ran at low risk.
// Every shell separator was a bypass: `;`, `&&`, `||`, `|`, `&`, a newline, a
// subshell, a `{ …; }` group, an `if …; then …` body.
//
// The contract these tests pin down: Classify returns the MAXIMUM risk over
// every command the string will actually run, quoting is respected (a `;`
// inside quotes is data, not an operator), and ordinary pipelines stay low.

package exec

import (
	"context"
	"testing"

	"github.com/baobao1044/yolo-code/internal/event"
)

func TestClassifyCompoundTakesMaxRisk(t *testing.T) {
	s := newSandbox(t)

	tests := []struct {
		name string
		cmd  string
		want event.Risk
	}{
		// --- the bypasses: a safe head hiding a destructive tail ---
		{"semicolon", "echo hi; rm -rf /", RiskCritical},
		{"semicolon no space", "ls;rm -rf /", RiskCritical},
		{"and-and", "echo hi && rm -rf /", RiskCritical},
		{"or-or", "false || rm -rf /", RiskCritical},
		{"pipe", "echo hi | rm -rf /", RiskCritical},
		{"background amp", "echo hi & rm -rf /", RiskCritical},
		{"newline", "echo hi\nrm -rf /", RiskCritical},
		{"crlf", "echo hi\r\nrm -rf /", RiskCritical},
		{"subshell", "ls && (rm -rf /)", RiskCritical},
		{"brace group", "ls; { rm -rf /; }", RiskCritical},
		{"if-then body", "if true; then rm -rf /; fi", RiskCritical},
		{"while-do body", "while true; do rm -rf /; done", RiskCritical},
		{"negation", "! rm -rf /", RiskCritical},
		{"wrapper in tail", "ls; sudo rm -rf /", RiskCritical},
		{"nested interpreter in tail", "ls; sh -c 'rm -rf /'", RiskCritical},
		{"dd in tail", "echo hi && dd if=/dev/zero of=/dev/sda", RiskCritical},

		// --- substitutions: unresolvable sub-execution, denied outright ---
		{"command substitution", "echo $(rm -rf /)", RiskCritical},
		{"backticks", "echo `rm -rf /`", RiskCritical},
		{"arith-looking substitution", "ls; echo $(id)", RiskCritical},
		{"process substitution", "cat <(rm -rf /)", RiskCritical},

		// --- eval/source as a segment head, with and without a leading space ---
		{"eval in tail", "ls; eval foo", RiskCritical},
		{"eval in tail no space", "ls;eval foo", RiskCritical},
		{"source in tail", "ls; source /etc/passwd", RiskCritical},
		{"dot source in tail", "ls; . /etc/passwd", RiskCritical},

		// --- lower tiers still propagate from the worst segment ---
		{"network in tail", "ls; curl http://evil.example", RiskHigh},
		{"network in pipeline", "cat urls | wget -i -", RiskHigh},
		{"mutation in tail", "ls && touch out.txt", RiskMedium},
		{"unknown in tail", "ls; frobnicate", RiskMedium},

		// --- legitimate compounds must NOT be inflated ---
		{"simple pipeline", "ls | head", RiskLow},
		{"three-stage pipeline", "cat a.txt | grep x | wc -l", RiskLow},
		{"build then test", "go build && go test", RiskLow},
		{"stderr redirect is not a separator", "go test 2>&1", RiskLow},
		// `&>` IS a separator, so this is medium, not low: bash.go runs `sh -c`
		// and dash reads `&` as "end of command". Asserting low here certified
		// the hole that let `ls &>out rm -rf /` run unprompted. See
		// TestClassifyAmpRedirectIsASeparator; do not "fix" the ergonomics back.
		{"bash stderr redirect splits", "go test &>out.log", RiskMedium},
		{"quoted semicolon is data", `ls "a; rm -rf /"`, RiskLow},
		{"single-quoted semicolon is data", `ls 'a; rm -rf /'`, RiskLow},
		{"escaped semicolon is data", `ls a\;b`, RiskLow},
		{"empty", "", RiskLow},
		{"separators only", " ; && | ", RiskLow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := s.Classify(tt.cmd); got != tt.want {
				t.Fatalf("Classify(%q) = %q, want %q", tt.cmd, got, tt.want)
			}
		})
	}
}

// splitSegments is the load-bearing half of the fix; test it directly so a
// quoting regression is localized rather than showing up as a risk surprise.
func TestSplitSegments(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want []string
	}{
		{"plain", "ls -la", []string{"ls -la"}},
		{"pipe", "ls | head", []string{"ls", "head"}},
		{"and-and", "go build && go test", []string{"go build", "go test"}},
		{"or-or", "a || b", []string{"a", "b"}},
		{"semicolons", "a; b ;c", []string{"a", "b", "c"}},
		{"newline", "a\nb", []string{"a", "b"}},
		{"background", "a & b", []string{"a", "b"}},
		{"subshell", "(a && b) & c", []string{"a", "b", "c"}},
		{"double quoted separator", `echo "a; b | c"`, []string{`echo "a; b | c"`}},
		{"single quoted separator", `echo 'a; b'`, []string{`echo 'a; b'`}},
		{"escaped separator", `echo a\;b`, []string{`echo a\;b`}},
		{"fd dup is not a separator", "go test 2>&1", []string{"go test 2>&1"}},
		// `&>` splits: POSIX sh ends the command at `&` and starts a new one at
		// the redirection. Keeping it whole hid an entire second command.
		{"amp-redirect is a separator", "go test &>log", []string{"go test", ">log"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitSegments(tt.cmd)
			if len(got) != len(tt.want) {
				t.Fatalf("splitSegments(%q) = %q, want %q", tt.cmd, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("splitSegments(%q) = %q, want %q", tt.cmd, got, tt.want)
				}
			}
		})
	}
}

// Bash must refuse a compound whose *tail* is critical, not just one whose
// head is — the end-to-end version of the bypass (bash.go spawns `sh -c` with
// the whole string, so a missed tail is a real execution).
//
// The tail is deliberately inert (`rm -rf` on a relative path that does not
// exist, inside the sandbox's own temp cwd): it classifies critical for the
// same reason `rm -rf /` does, but if the deny ever regresses this test must
// not become the thing that destroys the machine it runs on.
func TestBashDeniesCriticalTailOfCompound(t *testing.T) {
	bash := newBash(t)

	cmd := `echo hi; rm -rf ./no-such-dir`
	_, err := bash.Run(context.Background(), ToolInput{Args: []byte(`{"command":"` + cmd + `"}`)})
	if err == nil {
		t.Fatalf("Bash(%q) = nil, want deny — the critical tail must be classified", cmd)
	}
}
