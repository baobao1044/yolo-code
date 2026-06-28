// Tests for the observation normalizer (File 08 §8.6): a tool's raw output is
// redacted, truncated, and summarized into a structured Observation before it
// reaches verify/memory/context. Redaction masks secret shapes; truncation
// keeps head+tail within soft/hard limits and summarizes the middle past hard;
// the derived Summary survives history trimming.

package exec

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yolo-code/yolo/internal/event"
)

// secretTool emits known secret shapes in stdout so the redactor must mask
// them before they're published or stored. It declares Secret:true so the
// redactor always runs even if no pattern matched (File 08 §8.4.5).
type secretTool struct{}

func (secretTool) Name() string               { return "secret" }
func (secretTool) Metadata() Metadata         { return Metadata{Permission: Permission{Secret: true}} }
func (secretTool) Schema() Schema             { return Schema{Type: "object"} }
func (secretTool) Risk(_ ToolCall) event.Risk { return RiskLow }
func (secretTool) Run(_ context.Context, _ ToolInput) (ToolOutput, error) {
	return ToolOutput{Stdout: "aws=AKIAIOSFODNN7EXAMPLE token=ghp_abcDEF1234567890123456789 api_key=sk_live_123456", Summary: "leaked"}, nil
}

func TestRedactMasksSecrets(t *testing.T) {
	n := NewNormalizer(DefaultLimits(), nil) // nil summarizer → heuristic
	out := ToolOutput{Stdout: "aws=AKIAIOSFODNN7EXAMPLE token=ghp_abcDEF1234567890123456789 api_key=sk_live_123456"}
	obs := n.Normalize(out, Metadata{Permission: Permission{Secret: true}})

	if strings.Contains(obs.Stdout, "AKIAIOSFODNN7EXAMPLE") {
		t.Error("stdout still contains the AWS key — redactor must mask it")
	}
	if strings.Contains(obs.Stdout, "ghp_abcDEF1234567890123456789") {
		t.Error("stdout still contains the GitHub token — redactor must mask it")
	}
	if strings.Contains(obs.Stdout, "sk_live_123456") {
		t.Error("stdout still contains the api_key value — redactor must mask it")
	}
	// The masked markers must be present so the model sees *something* ran.
	if !strings.Contains(obs.Stdout, "***") {
		t.Errorf("stdout = %q, want a masked marker (***…)", obs.Stdout)
	}
}

func TestRedactMasksPEMBlock(t *testing.T) {
	n := NewNormalizer(DefaultLimits(), nil)
	pem := "-----BEGIN PRIVATE KEY-----\nMIIEvAIBADANBgkqhkiG9w0BAQ\n-----END PRIVATE KEY-----\nrest"
	obs := n.Normalize(ToolOutput{Stdout: pem}, Metadata{})

	if strings.Contains(obs.Stdout, "MIIEvAIBADANBgkqhkiG9w0BAQ") {
		t.Error("stdout still contains the PEM body — redactor must mask it")
	}
	if !strings.Contains(obs.Stdout, "rest") {
		t.Errorf("stdout = %q, want the non-secret tail preserved", obs.Stdout)
	}
}

func TestTruncateHeadTailWithinSoftHard(t *testing.T) {
	n := NewNormalizer(DefaultLimits(), nil)
	// A 200-byte stdout is > soft (8 KB? no — make limits tiny for the test).
	n2 := NewNormalizer(OutputLimits{StdoutSoft: 50, StdoutHard: 200, StderrSoft: 50, StderrHard: 200}, nil)
	body := strings.Repeat("x", 100) // 100 > soft(50), < hard(200)
	obs := n2.Normalize(ToolOutput{Stdout: body}, Metadata{})
	_ = n
	if !obs.Truncated {
		t.Error("Truncated = false, want true (stdout exceeded soft)")
	}
	if !strings.Contains(obs.Stdout, "truncated") {
		t.Errorf("stdout = %q, want a truncation marker", obs.Stdout)
	}
	if obs.Bytes == 0 {
		t.Error("Bytes = 0, want the original output size recorded")
	}
	// Head + tail preserved: starts and ends with 'x'.
	if !strings.HasPrefix(obs.Stdout, "x") {
		t.Errorf("stdout = %q, want it to start with the head", obs.Stdout)
	}
	if !strings.HasSuffix(obs.Stdout, "x") {
		t.Errorf("stdout = %q, want it to end with the tail", obs.Stdout)
	}
}

func TestTruncateAboveHardSummarizes(t *testing.T) {
	// A stubbed summarizer records its input and returns a fixed line, so the
	// test asserts the middle was sent to Summarize and the summary landed in
	// the output.
	var got string
	sum := stubSummarizer{fn: func(_ context.Context, mid string, _ int) string {
		got = mid
		return "build failed: 3 errors"
	}}
	n := NewNormalizer(OutputLimits{StdoutSoft: 50, StdoutHard: 100, StderrSoft: 50, StderrHard: 100}, sum)
	body := strings.Repeat("y", 250) // 250 > hard(100)
	obs := n.Normalize(ToolOutput{Stdout: body}, Metadata{})

	if got == "" {
		t.Fatal("summarizer was not called for output above hard")
	}
	if !strings.Contains(obs.Stdout, "build failed: 3 errors") {
		t.Errorf("stdout = %q, want the summarizer's line embedded", obs.Stdout)
	}
	if !strings.Contains(obs.Stdout, "summary:") {
		t.Errorf("stdout = %q, want the summary clearly labeled (not mistaken for verbatim)", obs.Stdout)
	}
}

func TestNormalizePublishesRedactedObservation(t *testing.T) {
	// End-to-end through Dispatch: the tool emits a secret, the published
	// ToolResultEvent.Obs must carry the redacted stdout (not the raw).
	r := new(Registry)
	r.Register(secretTool{})
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	eng := New(Deps{
		Registry:   r,
		Bus:        bus,
		Normalizer: NewNormalizer(DefaultLimits(), nil),
	})
	ch := bus.Subscribe("tool.result")

	eng.Dispatch(context.Background(), ToolCall{Tool: "secret", Args: []byte(`{}`), Task: "t1"})

	env := drainOne(t, ch, "tool.result")
	res := env.Evt.(*event.ToolResultEvent)
	var obs Observation
	if err := json.Unmarshal(res.Obs, &obs); err != nil {
		t.Fatalf("unmarshal obs: %v", err)
	}
	if strings.Contains(obs.Stdout, "AKIAIOSFODNN7EXAMPLE") {
		t.Error("published ToolResultEvent.Obs carries the raw AWS key — must be redacted before publish")
	}
}

// stubSummarizer implements Summarizer with a fixed function.
type stubSummarizer struct {
	fn func(ctx context.Context, text string, max int) string
}

func (s stubSummarizer) Summarize(ctx context.Context, text string, max int) string {
	return s.fn(ctx, text, max)
}
