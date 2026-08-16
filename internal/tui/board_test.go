// Tests for TUI-009 — Multi-agent board skeleton (File 14 §14.7.6). The board
// is hidden in single-agent mode; it appears on the first coord.plan.ready.
// A column per todo, with the assigned agent role + a status badge
// (assigned/coded/reviewed/tested/done). Updated by the coord.* events. This is
// the one place the TUI renders aggregation, but the aggregation is over
// published events, not runtime inspection.
//
// Skeleton: PlanReadyEvent.Plan is a json.RawMessage (no schema to unpack in
// the TUI — parsing doesn't belong here), so the board opens with planID and
// fills its todos from the subsequent coord.task.assign events. The full plan
// body is an integration-sprint fill (spec gap, documented).

package tui

import (
	"strings"
	"testing"

	"github.com/baobao1044/yolo-code/internal/event"
)

// TestFoldPlanReadyOpensBoard pins §14.7.6: the first coord.plan.ready opens
// the board with the planID. The board is hidden until this event (single-agent
// mode shows nothing). The mutation guard: if the board isn't opened, the user
// never sees the multi-agent decomposition.
func TestFoldPlanReadyOpensBoard(t *testing.T) {
	m := newModelForTest()
	m, _ = fold(m, env(&event.PlanReadyEvent{PlanID: "p_7", Plan: []byte(`{"todos":[]}`)}))

	if m.board == nil {
		t.Fatal("m.board = nil, want a boardView (coord.plan.ready must open the board — mutation guard)")
	}
	if m.board.planID != "p_7" {
		t.Errorf("board.planID = %q, want p_7", m.board.planID)
	}
}

// TestFoldTaskAssignAddsTodo pins §14.7.6: coord.task.assign appends a todo
// column with the agent role + status "assigned". The todo carries the TodoID
// so later coord.* events can update it (looked up by ID).
func TestFoldTaskAssignAddsTodo(t *testing.T) {
	m := newModelForTest()
	m.board = &boardView{planID: "p_7"} // board already open
	m, _ = fold(m, env(&event.TaskAssignEvent{
		PlanID: "p_7", TodoID: "todo_1", Agent: "coder", Brief: "fix auth",
	}))

	if len(m.board.todos) != 1 {
		t.Fatalf("board.todos = %d, want 1 (assign appends a todo)", len(m.board.todos))
	}
	td := m.board.todos[0]
	if td.todoID != "todo_1" {
		t.Errorf("todo.todoID = %q, want todo_1", td.todoID)
	}
	if td.agent != "coder" {
		t.Errorf("todo.agent = %q, want coder (the assigned role)", td.agent)
	}
	if td.status != "assigned" {
		t.Errorf("todo.status = %q, want assigned", td.status)
	}
}

// TestFoldTaskAssignWithoutBoardIsIgnored pins robustness: a coord.task.assign
// arriving before any coord.plan.ready (board not open) is ignored — the TUI
// doesn't fabricate a board. The board opens only on plan.ready.
func TestFoldTaskAssignWithoutBoardIsIgnored(t *testing.T) {
	m := newModelForTest()
	m, _ = fold(m, env(&event.TaskAssignEvent{PlanID: "p_7", TodoID: "todo_1", Agent: "coder"}))

	if m.board != nil {
		t.Error("m.board = non-nil after assign-without-plan.ready, want nil (the board opens only on coord.plan.ready)")
	}
}

// TestFoldCodeReadyUpdatesTodoStatus pins §14.7.6: coord.code.ready marks the
// todo "coded" (looked up by TodoID). The todo must already exist (from assign);
// a code.ready for an unknown todo is ignored (robustness — no fabrication).
func TestFoldCodeReadyUpdatesTodoStatus(t *testing.T) {
	m := newModelForTest()
	m.board = &boardView{planID: "p_7", todos: []todoView{{todoID: "todo_1", agent: "coder", status: "assigned"}}}
	m, _ = fold(m, env(&event.CodeReadyEvent{PlanID: "p_7", TodoID: "todo_1", Diff: "@@ …", SelfReport: "done"}))

	if m.board.todos[0].status != "coded" {
		t.Errorf("todo.status = %q, want coded (coord.code.ready advances the todo)", m.board.todos[0].status)
	}
}

// TestFoldReviewVerdictUpdatesTodoStatus pins §14.7.6: coord.review.verdict
// marks the todo approved/rework by the Approved flag. Approved=true → "approved",
// Approved=false → "rework".
func TestFoldReviewVerdictUpdatesTodoStatus(t *testing.T) {
	m := newModelForTest()
	m.board = &boardView{planID: "p_7", todos: []todoView{
		{todoID: "todo_1", status: "coded"},
		{todoID: "todo_2", status: "coded"},
	}}
	m, _ = fold(m, env(&event.ReviewVerdictEvent{PlanID: "p_7", TodoID: "todo_1", Approved: true, Comments: []string{"lgtm"}}))
	m, _ = fold(m, env(&event.ReviewVerdictEvent{PlanID: "p_7", TodoID: "todo_2", Approved: false, Comments: []string{"redo"}}))

	if m.board.todos[0].status != "approved" {
		t.Errorf("todo_1 status = %q, want approved", m.board.todos[0].status)
	}
	if m.board.todos[1].status != "rework" {
		t.Errorf("todo_2 status = %q, want rework", m.board.todos[1].status)
	}
}

// boardLines returns the rendered rail's lines for a board-only model, so an
// assertion can count rows the way the user counts them: on screen.
func boardLines(m Model) []string {
	return strings.Split(railView(m, 80, 20), "\n")
}

// TestReworkReassignsTheSameRowInsteadOfAppendingADuplicate is the fix-3
// regression. coord re-publishes task.assign for the SAME TodoID on every
// rework cycle, and the board appended unconditionally — so one todo through
// one rework cycle grew a second row that boardUpdateTodo (which stops at the
// first match) then never touched again. A finished plan rendered with a
// permanently "[~] assigned" ghost row.
func TestReworkReassignsTheSameRowInsteadOfAppendingADuplicate(t *testing.T) {
	m := newModelForTest()
	m, _ = fold(m, env(&event.PlanReadyEvent{PlanID: "p_1"}))
	assign := &event.TaskAssignEvent{PlanID: "p_1", TodoID: "todo_A", Agent: "coder", Brief: "fix auth"}
	m, _ = fold(m, env(assign))
	m, _ = fold(m, env(&event.CodeReadyEvent{PlanID: "p_1", TodoID: "todo_A"}))
	m, _ = fold(m, env(&event.ReviewVerdictEvent{PlanID: "p_1", TodoID: "todo_A", Approved: false, Comments: []string{"redo"}}))
	// The rework re-assign: same todo, same agent, a brief carrying the
	// reviewer's comments.
	m, _ = fold(m, env(&event.TaskAssignEvent{
		PlanID: "p_1", TodoID: "todo_A", Agent: "coder",
		Brief: "fix auth\n\nReviewer comments:\nredo",
	}))
	m, _ = fold(m, env(&event.CodeReadyEvent{PlanID: "p_1", TodoID: "todo_A"}))
	m, _ = fold(m, env(&event.TestReportEvent{PlanID: "p_1", TodoID: "todo_A", Passed: true}))

	if n := len(m.board.todos); n != 1 {
		t.Fatalf("board.todos = %d, want 1 (one todo, one row — rework re-assigns, it doesn't add a todo): %+v", n, m.board.todos)
	}
	if got := m.board.todos[0].status; got != "tested:pass" {
		t.Errorf("row status = %q, want tested:pass (the surviving row must be the live one, not a stale copy)", got)
	}
	rows := boardLines(m)
	if n := len(rows); n != 2 {
		t.Errorf("board renders %d lines, want 2 (plan line + one row):\n%s", n, strings.Join(rows, "\n"))
	}
	if strings.Contains(strings.Join(rows[1:], "\n"), "assigned") {
		t.Errorf("board still renders a stale [~] assigned row after the todo passed:\n%s", strings.Join(rows, "\n"))
	}
}

// TestBoardRowKeepsAMultilineBriefOnOneLine pins the row layout: the rework
// brief embeds "\n\nReviewer comments:\n…" (and the test-failure variant
// embeds "\n\nTest output:\n…"), and a raw newline in a single-line row splits
// it across the rail, pushing the rest of the board down.
func TestBoardRowKeepsAMultilineBriefOnOneLine(t *testing.T) {
	m := newModelForTest()
	m, _ = fold(m, env(&event.PlanReadyEvent{PlanID: "p_1"}))
	m, _ = fold(m, env(&event.TaskAssignEvent{
		PlanID: "p_1", TodoID: "todo_A", Agent: "coder",
		Brief: "fix auth\n\nReviewer comments:\nredo the token check",
	}))

	rows := boardLines(m)
	if n := len(rows); n != 2 {
		t.Errorf("a multi-line brief split the row into %d lines, want 2 (plan line + one row):\n%s",
			n, strings.Join(rows, "\n"))
	}
}

// TestBannerKeepsAMultilineReasonOnOneLine pins the same rule for the other
// free-text field that reaches a one-line region: the banner renders an error
// message, a cancel reason, a cost-abort reason or a plan summary, any of
// which can be multi-line (a wrapped error from the merge verifier is).
func TestBannerKeepsAMultilineReasonOnOneLine(t *testing.T) {
	m := newModelForTest()
	m.width = 80
	m, _ = fold(m, env(&event.ErrorEvent{Task: "t_1", Layer: "coord", Msg: "merge failed:\n  conflict in auth.go\n  conflict in main.go"}))
	if n := len(strings.Split(bannerView(m), "\n")); n != 1 {
		t.Errorf("banner rendered %d lines, want 1:\n%s", n, bannerView(m))
	}
}

// --- plan.done: the multi-agent run's only terminal signal ---

// TestFoldPlanDoneSuccessSetsTerminalState is half of fix 2. PlanDoneEvent's
// topic used to be the bare "plan.done" (its siblings are coord.plan.ready /
// coord.task.assign — the odd one out), so the TUI's coord.> subscription
// missed it AND fold had no case for it: a multi-agent run never told the user
// it had finished. The topic is coord.plan.done now; this pins the fold half.
func TestFoldPlanDoneSuccessSetsTerminalState(t *testing.T) {
	m := newModelForTest()
	m.width = 120
	m, _ = fold(m, env(&event.PlanReadyEvent{PlanID: "p_1"}))
	m, _ = fold(m, env(&event.PlanDoneEvent{PlanID: "p_1", Done: true, Merged: true, Summary: "3 todos merged"}))

	head := headerView(m)
	if !strings.Contains(head, "DONE") {
		t.Errorf("plan.done(Done:true) left the header without a terminal state:\n%s", head)
	}
	if !strings.Contains(bannerView(m), "3 todos merged") {
		t.Errorf("plan.done summary never reached the banner:\n%s", bannerView(m))
	}
}

// TestFoldPlanDoneFailureNeverClaimsSuccess pins the unhappy half. coord is
// being changed to publish plan.done on failure and cancellation too, so the
// case must handle Done:false — and Merged:true must not be read as success:
// a merged patch that failed verification is exactly the case the orchestrator
// guards against.
func TestFoldPlanDoneFailureNeverClaimsSuccess(t *testing.T) {
	m := newModelForTest()
	m.width = 120
	m, _ = fold(m, env(&event.PlanReadyEvent{PlanID: "p_1"}))
	m, _ = fold(m, env(&event.PlanDoneEvent{PlanID: "p_1", Done: false, Merged: true, Summary: "merged patch not verified"}))

	head := headerView(m)
	if strings.Contains(head, "DONE") {
		t.Errorf("plan.done(Done:false, Merged:true) rendered as DONE:\n%s", head)
	}
	if strings.Contains(head, "✔") {
		t.Errorf("plan.done(Done:false) rendered the success glyph:\n%s", head)
	}
	banner := bannerView(m)
	if !strings.Contains(banner, "did not complete") {
		t.Errorf("banner does not say the plan failed to complete:\n%s", banner)
	}
	if !strings.Contains(banner, "merged patch not verified") {
		t.Errorf("banner drops the summary, the only detail the event carries:\n%s", banner)
	}
}

// TestFoldPlanDoneWithoutASummaryStillReportsTheOutcome pins the cancellation
// shape: a cancelled plan can arrive as Done:false with no summary at all, and
// silence is the bug being fixed — the board must not just stop moving.
func TestFoldPlanDoneWithoutASummaryStillReportsTheOutcome(t *testing.T) {
	m := newModelForTest()
	m.width = 120
	m, _ = fold(m, env(&event.PlanReadyEvent{PlanID: "p_1"}))
	m, _ = fold(m, env(&event.PlanDoneEvent{PlanID: "p_1", Done: false}))

	if b := bannerView(m); strings.TrimSpace(b) == "" {
		t.Error("a cancelled/failed plan with no summary rendered no banner at all — the run ends in silence")
	}
	if head := headerView(m); strings.Contains(head, "DONE") {
		t.Errorf("header claims DONE for a plan that did not complete:\n%s", head)
	}
}

// TestFoldPlanDoneCancelDoesNotRenderAsFailure pins the third terminal shape.
// Done is false for a cancellation and for a failure alike, so before coord
// carried a Canceled flag the user's own Ctrl-C was reported back to them as
// FAILED — the tool blaming the work for a decision the operator made.
func TestFoldPlanDoneCancelDoesNotRenderAsFailure(t *testing.T) {
	m := newModelForTest()
	m.width = 120
	m, _ = fold(m, env(&event.PlanReadyEvent{PlanID: "p_1"}))
	m, _ = fold(m, env(&event.PlanDoneEvent{
		PlanID: "p_1", Done: false, Canceled: true, Merged: true,
		Summary: "canceled: 2/4 todos done; combined without re-verification (canceled)",
	}))

	if m.state != "CANCELLED" {
		t.Errorf("state = %q for a cancelled plan, want CANCELLED", m.state)
	}
	head := headerView(m)
	if strings.Contains(head, "DONE") {
		t.Errorf("a cancelled plan rendered as DONE:\n%s", head)
	}
	if strings.Contains(head, "FAILED") {
		t.Errorf("the operator's own cancellation is reported as FAILED:\n%s", head)
	}
	banner := bannerView(m)
	if !strings.Contains(banner, "cancelled") {
		t.Errorf("banner never says the plan was cancelled:\n%s", banner)
	}
	if !strings.Contains(banner, "2/4 todos done") {
		t.Errorf("banner drops the summary, so the partial progress is lost:\n%s", banner)
	}
}

// TestFoldTestReportUpdatesTodoStatus pins §14.7.6: coord.test.report marks the
// todo tested, with pass/fail by the Passed flag. Tested=true → "tested:pass" or
// "tested:fail" (the badge distinguishes).
func TestFoldTestReportUpdatesTodoStatus(t *testing.T) {
	m := newModelForTest()
	m.board = &boardView{planID: "p_7", todos: []todoView{
		{todoID: "todo_1", status: "approved"},
	}}
	m, _ = fold(m, env(&event.TestReportEvent{PlanID: "p_7", TodoID: "todo_1", Passed: true, Output: "ok"}))

	if m.board.todos[0].status != "tested:pass" {
		t.Errorf("todo status = %q, want tested:pass", m.board.todos[0].status)
	}
}
