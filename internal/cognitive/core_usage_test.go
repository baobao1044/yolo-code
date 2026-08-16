// Think's half of the usage contract.
//
// openai_compat_test.go pins the two ends of the wire — that the SSE parser
// reads the provider's counts, and that parseTurn invents none from text alone.
// This file pins the middle: Think must carry a count that arrived on the Chunk
// stream onto the Turn it returns, and must leave UsageKnown false when none
// did. That is the seam where the old "$0.00 for a run that cost money" bug
// lived, because a Turn is the only thing the cost ledger ever sees.

package cognitive

import (
	"errors"
	"testing"

	"github.com/baobao1044/yolo-code/internal/prompt"
)

// TestThinkCarriesProviderReportedUsage is the happy path: the provider put
// counts on the stream, so the returned Turn reports them as a measurement.
func TestThinkCarriesProviderReportedUsage(t *testing.T) {
	chunks := []Chunk{
		{Delta: "the answer"},
		{Usage: &Usage{TokensIn: 1234, TokensOut: 56}},
	}
	core, _ := newTestCore(t, chunks)

	turn, err := core.Think(ctxWithTask("t_usage"), []prompt.Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if !turn.UsageKnown {
		t.Fatal("turn.UsageKnown = false after the provider reported counts; the measurement was dropped between the stream and the Turn")
	}
	if turn.TokensIn != 1234 || turn.TokensOut != 56 {
		t.Errorf("turn tokens = %d/%d, want 1234/56 (the provider's own numbers, unmodified)", turn.TokensIn, turn.TokensOut)
	}
}

// TestThinkLeavesUsageUnknownWhenProviderReportsNone is the defect this whole
// pass exists for, so read the assertion literally: 0 tokens here means NOBODY
// COUNTED, not "the turn was free". A provider that never sends a usage chunk —
// most local runners, and any endpoint whose stream is cut short — must leave
// UsageKnown false so the ledger declines to charge and the UI renders
// "unknown". The two zeroes are asserted alongside the flag precisely because a
// future change that starts defaulting them to a real-looking 0 while flipping
// the flag would restore the confident-$0.00 bug.
func TestThinkLeavesUsageUnknownWhenProviderReportsNone(t *testing.T) {
	chunks := []Chunk{
		{Delta: "an answer nobody measured"},
	}
	core, _ := newTestCore(t, chunks)

	turn, err := core.Think(ctxWithTask("t_nousage"), nil)
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if turn.UsageKnown {
		t.Error("turn.UsageKnown = true with no usage chunk on the stream; an unmeasured turn must not claim to be measured")
	}
	if turn.TokensIn != 0 || turn.TokensOut != 0 {
		t.Errorf("turn tokens = %d/%d, want 0/0; unmeasured counts must stay zero so UsageKnown is the only thing distinguishing them", turn.TokensIn, turn.TokensOut)
	}
}

// TestThinkCarriesUsageOnToolCallTurn pins the branch that actually costs
// money. A tool-calling turn is the one that loops: the runtime executes the
// call, records the result, and calls Think again, so a run's whole bill is
// mostly non-final turns. Losing usage on the Final=false path would leave the
// cost ledger reading only the single closing turn.
func TestThinkCarriesUsageOnToolCallTurn(t *testing.T) {
	chunks := []Chunk{
		{ToolCall: &ToolCall{ID: "call_abc", Tool: "read_file", Args: []byte(`{"file":"a.go"}`)}},
		{Usage: &Usage{TokensIn: 900, TokensOut: 42}},
	}
	core, _ := newTestCore(t, chunks)

	turn, err := core.Think(ctxWithTask("t_toolusage"), nil)
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if turn.Final {
		t.Fatal("turn.Final = true on a tool-calling turn; this test is meaningless unless it exercises the looping branch")
	}
	if !turn.UsageKnown {
		t.Fatal("turn.UsageKnown = false on the tool-call path; every looping turn would then bill as free")
	}
	if turn.TokensIn != 900 || turn.TokensOut != 42 {
		t.Errorf("turn tokens = %d/%d, want 900/42", turn.TokensIn, turn.TokensOut)
	}
}

// TestThinkCarriesUsageWhenTheStreamErrorsAfterIt is the F2 half that lives
// here. A stream that breaks late — after the provider has already reported the
// counts — is the expensive case: the whole context was in the prompt and the
// provider charged for it. Returning a bare Turn{} alongside the error threw
// that measurement away, so a run that keeps losing connections bills at $0.00
// and MaxDollars can never fire. The Turn must still say what was measured.
func TestThinkCarriesUsageWhenTheStreamErrorsAfterIt(t *testing.T) {
	chunks := []Chunk{
		{Delta: "partial"},
		{Usage: &Usage{TokensIn: 50000, TokensOut: 800}},
		{Err: errors.New("connection reset")},
	}
	core, _ := newTestCore(t, chunks)

	turn, err := core.Think(ctxWithTask("t_errusage"), nil)
	if err == nil {
		t.Fatal("Think = nil error after an Err chunk; this test is meaningless unless it exercises the error path")
	}
	if !turn.UsageKnown {
		t.Fatal("turn.UsageKnown = false on the error path; the provider's 50000-token bill was dropped and the run prices at $0.00")
	}
	if turn.TokensIn != 50000 || turn.TokensOut != 800 {
		t.Errorf("turn tokens = %d/%d, want 50000/800 (the counts the provider reported before the stream broke)", turn.TokensIn, turn.TokensOut)
	}
	// The rest of the Turn must stay empty: it accompanies an error, so nothing
	// on it may read as a reply (see Think's contract).
	if turn.Final || turn.Text != "" || len(turn.ToolCalls) != 0 {
		t.Errorf("error-path Turn carries content (Final=%v Text=%q calls=%d); only usage is valid alongside an error",
			turn.Final, turn.Text, len(turn.ToolCalls))
	}
}

// TestThinkLeavesUsageUnknownWhenTheStreamErrorsBeforeIt is the other side of
// the same contract: an error with no usage chunk behind it must not invent a
// measurement. 0/0 with UsageKnown=true would charge a real turn nothing and
// restore the confident-$0.00 bug from the error path instead of the happy one.
func TestThinkLeavesUsageUnknownWhenTheStreamErrorsBeforeIt(t *testing.T) {
	chunks := []Chunk{
		{Delta: "partial"},
		{Err: errors.New("connection reset")},
	}
	core, _ := newTestCore(t, chunks)

	turn, err := core.Think(ctxWithTask("t_errnousage"), nil)
	if err == nil {
		t.Fatal("Think = nil error after an Err chunk")
	}
	if turn.UsageKnown {
		t.Errorf("turn.UsageKnown = true with no usage chunk on the stream; the error path must not claim a measurement (%d/%d)", turn.TokensIn, turn.TokensOut)
	}
}

// TestThinkReadsUsageFromAChunkWithNoDelta pins the shape the wire actually
// uses: OpenAI-compatible endpoints send usage on a trailing chunk whose
// choices array is empty, so it reaches the Core as a Chunk with no Delta, no
// Thinking and no ToolCall. An accumulator that only looked at chunks carrying
// text would skip the only chunk that ever carries counts.
func TestThinkReadsUsageFromAChunkWithNoDelta(t *testing.T) {
	chunks := []Chunk{
		{Delta: "hi"},
		{Usage: &Usage{TokensIn: 80, TokensOut: 9}}, // the trailing empty-choices chunk
	}
	core, _ := newTestCore(t, chunks)

	turn, err := core.Think(ctxWithTask("t_emptychunk"), nil)
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if !turn.UsageKnown || turn.TokensIn != 80 || turn.TokensOut != 9 {
		t.Errorf("turn usage = %d/%d known=%v, want 80/9 known=true from the text-free trailing chunk",
			turn.TokensIn, turn.TokensOut, turn.UsageKnown)
	}
	// The usage-only chunk must not leak into the answer: it carried no text, so
	// the accumulated Text is whatever the delta chunks said and nothing more.
	if turn.Text != "hi" {
		t.Errorf("turn.Text = %q, want %q; the usage chunk contributed text it does not have", turn.Text, "hi")
	}
}
