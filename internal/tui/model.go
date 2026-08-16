// TUI-001 model — render state (File 14 §14.4). The Model holds ONLY what's
// needed to paint the screen; every field is derived from events. It holds no
// state machine of its own — `state` is a string copied from the last
// state.change; the TUI does not model the FSM.
//
// Field growth is ticket-driven: TUI-001 carries the header + watcher
// plumbing; later tickets append fields (TUI-002 chat, TUI-003 status
// flashes, TUI-004 diff, TUI-005 cost, TUI-009 board). NewModel keeps the
// zero value coherent so a nil-seam model is safe to fold (the pure
// projection never dereferences sub/publisher — see fold_test's nil-safety
// pin).

package tui

import (
	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/baobao1044/yolo-code/internal/event"
)

// pane is the focused region (File 14 §14.4.1). chat is the default; diff
// takes focus when a patch.applied/verification.failed arrives (TUI-004).
type pane int

const (
	paneChat pane = iota
	paneDiff
	paneBoard
)

// messageView is one chat line (TUI-002). Roles: user, assistant, thinking,
// tool, observation, reflection.
type messageView struct {
	role string
	text string
}

// approvalView is a pending approval.request (TUI-006 — drives y/n handling).
type approvalView struct {
	id      string
	tool    string
	summary string
	preview string
	risk    string
}

// diffView is the diff viewer state (TUI-004). PatchAppliedEvent carries the
// file list + counts + the rendered unified-diff string (Phase D); the viewer
// shows real hunks when Diff is present, falling back to the file list + counts.
type diffView struct {
	files      []event.PatchFile
	insertions int
	deletions  int
	reason     string // set by verification.failed
	diff       string // rendered unified-diff hunks (Phase D)
}

// costView is the cost-meter rail (TUI-005). It accumulates from cost.incurred
// and llm.token deltas. Only calls is a measured quantity: dollars is zero
// unless the operator supplies their own rates via YOLO_COST_RATES, and the
// token figure is chars/4 ≈ 4 chars per token. The rail must not present
// either as measured. level/aborted stay for the degradation ladder.
//
// The estimate is no longer there because the counts do not exist — the
// provider's usage is now parsed in cognitive.Core.Think and carried on the
// Turn, and the runtime bills it. It is there because no event carries those
// counts to this layer: llm.token has only a text Delta, and cost.incurred has
// only Dollars. Replacing the estimate with real numbers needs a bus event
// carrying TokensIn/TokensOut plus the UsageKnown flag — and it must carry the
// flag, because a turn nobody counted has to keep reading as an estimate here
// rather than silently resetting the rail to a measured-looking zero.
type costView struct {
	level       string
	aborted     bool
	abortReason string
	calls       int     // tool calls seen on cost.incurred — exact
	dollars     float64 // 0 unless the operator configured YOLO_COST_RATES
	tokenChars  int     // characters seen on llm.token deltas — the raw sum
}

// tokensEst is the rail's ≈4-chars-per-token estimate. The division happens
// HERE, once, over the accumulated character count — not per delta as it used
// to. One llm.token event is one provider chunk, i.e. roughly one token, so
// dividing each delta threw away a remainder per token instead of once at the
// end: a 127-character stream of 28 chunks read as 22 tokens instead of 31,
// and a provider that streams one character at a time truncated every single
// delta to zero and rendered a measured-looking "~0 tok" after the whole
// answer. The estimate is allowed to be rough; it is not allowed to be a
// number nobody computed.
func (c costView) tokensEst() int { return c.tokenChars / 4 }

// boardView is the multi-agent board (TUI-009). Hidden until coord.plan.ready.
type boardView struct {
	planID string
	todos  []todoView
}

// todoView is one board column (TUI-009).
type todoView struct {
	todoID string
	agent  string
	brief  string
	status string
}

// Model is the bubbletea model — render state, nothing more (File 14 §14.4.1).
// sub/cancel/publisher carry the bus bridge; fold never dereferences them
// (the pure projection only mutates render fields), so a nil-seam model is
// safe to fold — pinned by TestFoldTaskStartedDoesNotTouchRuntime.
type Model struct {
	// layout
	width, height int
	focus         pane
	ready         bool

	// task header (TUI-001)
	taskID   string
	goal     string // TaskStartedEvent has no Kind field — header shows the goal (spec gap)
	state    string // current FSM state label, from state.change (TUI-003); "" until then
	stateWhy string // transition reason, from StateChangeEvent.Why

	// header ownership. The header describes ONE task: the one this TUI
	// submitted. A multi-agent goal builds a runtime.Core per coder todo, and
	// each of those Cores publishes task.started/state.change/task.completed on
	// the same bus the TUI renders — folding those into the global header made
	// it read "✔ DONE" one todo into the plan, with the user's goal replaced by
	// a sub-agent's brief.
	//
	// awaitingTask is set the moment the user submits, so the next task.started
	// is recognised as the answer to that submit and everything after it is a
	// sub-agent's. nested counts sub-agent tasks that have started and not yet
	// finished; it exists because an ID filter alone is not enough — the two
	// session.Managers on this bus (the driver's and the agent runner's) each
	// start their counter at zero, so both mint "t_1" and a sub-agent's
	// lifecycle is indistinguishable from the user's by ID. Depth is.
	awaitingTask bool
	nested       int

	// chat (TUI-002)
	messages      []messageView
	thinking      string // accumulated llm.thinking deltas for the current turn
	liveAssistant string // accumulated llm.token deltas until assistant.message flushes
	streaming     bool

	// active tool (TUI-002)
	activeTool string

	// pending approval (TUI-006)
	approval *approvalView

	// diff viewer (TUI-004)
	diff *diffView

	// cost meter (TUI-005)
	cost costView

	// board (TUI-009)
	board *boardView

	// banner (last error / cost.abort)
	banner string

	// status-bar flashes (TUI-003)
	contextFlash string
	memoryFlash  string

	// spinner (TUI-007)
	spinnerFrame int

	// input (TUI-006): a bubbles/textinput widget (Phase B). It owns the cursor,
	// history, and ←/→/Home/End/Ctrl-A/E/word-delete — the hand-rolled char
	// append is gone. inputValue() reads the widget's text (backward-compat for
	// the pure test path); Reset() clears it on submit.
	input textinput.Model

	// scroll (D1): scrollOffset > 0 means scrolled up from the bottom;
	// 0 = auto-scroll to latest message.
	scrollOffset int

	// help overlay (D2): toggled by ? key.
	showHelp bool

	// bus bridge
	sub       <-chan event.Envelope
	cancel    chan struct{}
	publisher EventPublisher
}

// newModel builds a Model with the given seams. Both may be nil: a nil-seam
// model is safe to fold (the projection never dereferences them), which is
// how fold tests drive the pure projection without a bus. Init/Update/View
// (the bubbletea surface) live in run.go.
func newModel(sub <-chan event.Envelope, pub EventPublisher) Model {
	ti := textinput.New()
	ti.Prompt = "> "
	ti.Focus() // accept keyboard input + show cursor (Phase B)
	// Reduced-motion (Phase C): a steady cursor instead of a blinking one.
	if theme.noMotion {
		ti.Cursor.SetMode(cursor.CursorStatic)
	}
	return Model{
		focus:     paneChat,
		sub:       sub,
		publisher: pub,
		input:     ti,
	}
}

// inputValue returns the input line's current text (Phase B backward-compat).
// It reads from the bubbles/textinput widget; callers that used m.inputText
// now call m.inputValue() so the pure test path stays widget-agnostic.
func (m Model) inputValue() string {
	return m.input.Value()
}

// setInput replaces the input line's text and moves the cursor to the end
// (Phase B backward-compat for tests that drive the widget directly).
func (m *Model) setInput(s string) {
	m.input.SetValue(s)
}

// Init launches the first busWatcher + the first 60 Hz tick so the bridge
// starts pumping and the spinner animates the moment the program runs (File 14
// §14.11, §14.9.3). Returns nil (no watcher/tick) when there's no subscription
// channel — keeps the pure-projection model usable in tests without a bus.
// The tick re-arms itself in the tickMsg case (Update), so Init arms it once.
func (m Model) Init() tea.Cmd {
	if m.sub == nil {
		return nil
	}
	return tea.Batch(busWatcher(m.sub, m.cancel), nextTick)
}

// nextFocus cycles through non-empty panes: chat → diff (if present) →
// board (if present) → chat. Skips panes with no content.
func nextFocus(m Model) pane {
	switch m.focus {
	case paneChat:
		if m.diff != nil {
			return paneDiff
		}
		if m.board != nil {
			return paneBoard
		}
		return paneChat
	case paneDiff:
		if m.board != nil {
			return paneBoard
		}
		return paneChat
	case paneBoard:
		return paneChat
	}
	return paneChat
}
