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
