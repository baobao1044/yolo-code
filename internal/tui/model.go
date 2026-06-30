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

// diffView is the diff viewer state (TUI-004). PatchAppliedEvent has no
// diff-hunks text (spec gap: only Files+counts), so this holds the file list +
// counts, not hunks.
type diffView struct {
	files      []event.PatchFile
	insertions int
	deletions  int
	reason     string // set by verification.failed
}

// costView is the cost-meter rail (TUI-005). Degraded+abort only (spec gap:
// no cost.spend/cost.loop events in the catalog).
type costView struct {
	level       string
	aborted     bool
	abortReason string
}

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

	// input (TUI-006): the current text in the input line. The production
	// handleInput reads this from a bubbles/textinput widget; the pure
	// transition stores it on the model so the test drives it without a widget.
	// Reset to "" on submit (optimistic echo → user.submit).
	inputText string

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
	return Model{
		focus:     paneChat,
		sub:       sub,
		publisher: pub,
	}
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
