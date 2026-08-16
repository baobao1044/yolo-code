// Package event — catalog registration (L3-006).
//
// catalog is the single source of truth for which topics the durability log
// (L3-004) can reconstruct on replay: init registers from it, and the tests
// iterate it instead of re-listing the topics. It used to be a bare sequence of
// Register calls with the topic set re-typed in the test, and the two drifted —
// five declared events were never registered, which made any log containing one
// unreadable. TestCatalogCoversEveryDeclaredEvent now derives the expected set
// from events.go itself, so the table cannot silently fall behind again.
//
// The 16 topic groups from File 05 §5.4.9 are all covered here.

package event

var catalog = map[Topic]func() Event{
	// L1: Session & task.
	"task.started":    func() Event { return &TaskStartedEvent{} },
	"task.completed":  func() Event { return &TaskCompletedEvent{} },
	"task.cancelled":  func() Event { return &TaskCancelledEvent{} },
	"task.failed":     func() Event { return &TaskFailedEvent{} },
	"task.paused":     func() Event { return &TaskPausedEvent{} },
	"task.checkpoint": func() Event { return &CheckpointEvent{} },
	"task.restored":   func() Event { return &RestoredEvent{} },
	"task.undone":     func() Event { return &UndoneEvent{} },

	// L2: Runtime FSM.
	"state.change":         func() Event { return &StateChangeEvent{} },
	"context.built":        func() Event { return &ContextBuiltEvent{} },
	"approval.request":     func() Event { return &ApprovalRequestEvent{} },
	"observation.received": func() Event { return &ObservationEvent{} },
	"verification.failed":  func() Event { return &VerificationFailedEvent{} },
	"verification.stage":   func() Event { return &VerificationStageEvent{} },
	"reflection.note":      func() Event { return &ReflectionEvent{} },
	"patch.applied":        func() Event { return &PatchAppliedEvent{} },

	// L5: Prompt Compiler.
	"prompt.budget": func() Event { return &TokenBudgetEvent{} },

	// L6: Cognitive.
	"llm.token":         func() Event { return &TokenEvent{} },
	"llm.thinking":      func() Event { return &ThinkingEvent{} },
	"assistant.message": func() Event { return &AssistantMessageEvent{} },
	"tool.call":         func() Event { return &ToolCallEvent{} },
	"cost.degraded":     func() Event { return &CostDegradedEvent{} },
	"cost.abort":        func() Event { return &CostAbortEvent{} },
	"cost.incurred":     func() Event { return &CostIncurredEvent{} },

	// L7: Execution.
	"tool.result": func() Event { return &ToolResultEvent{} },

	// L10: Memory.
	"memory.update":   func() Event { return &MemoryUpdateEvent{} },
	"user.preference": func() Event { return &UserPreferenceEvent{} },

	// User (published by TUI, subscribed by L2).
	"user.submit":  func() Event { return &UserSubmitEvent{} },
	"user.cancel":  func() Event { return &UserCancelEvent{} },
	"user.approve": func() Event { return &UserApproveEvent{} },
	"user.reject":  func() Event { return &UserRejectEvent{} },
	"user.pause":   func() Event { return &UserPauseEvent{} },
	"user.resume":  func() Event { return &UserResumeEvent{} },
	"user.quit":    func() Event { return &UserQuitEvent{} },

	// L-user: slash commands (TUI ⇄ driver).
	"user.command":     func() Event { return &UserCommandEvent{} },
	"command.response": func() Event { return &CommandResponseEvent{} },

	// L11: Coordination.
	"coord.task.assign":    func() Event { return &TaskAssignEvent{} },
	"coord.plan.ready":     func() Event { return &PlanReadyEvent{} },
	"coord.code.ready":     func() Event { return &CodeReadyEvent{} },
	"coord.review.verdict": func() Event { return &ReviewVerdictEvent{} },
	"coord.test.report":    func() Event { return &TestReportEvent{} },
	"coord.plan.done":      func() Event { return &PlanDoneEvent{} },

	// L-scope: Scope.
	"scope.enter":      func() Event { return &ScopeEnterEvent{} },
	"scope.transition": func() Event { return &ScopeTransitionEvent{} },

	// L-workflow: Dynamic Workflow.
	"workflow.selected": func() Event { return &WorkflowSelectedEvent{} },

	// Error (any layer).
	"error": func() Event { return &ErrorEvent{} },
}

// legacyTopics maps retired topic names to the event that replaced them, so a
// durability log written before a rename stays replayable. Replay resolves a
// topic through the same registry the catalog fills, and an unknown one is a
// hard error ("no factory registered") that makes the whole log unreadable —
// so a rename without an entry here silently orphans every transcript on disk.
//
// These are deliberately NOT in catalog: that table is the forward wire
// contract, and TestRoundTripAllCatalogEvents rightly asserts every entry's
// Type() equals its key. A legacy name is a name nothing emits any more, which
// is exactly why it fails that check and belongs in its own table.
var legacyTopics = map[Topic]func() Event{
	// Renamed to coord.plan.done. Its siblings were all coord.* and it was
	// not, so a coord.> subscriber got the whole conversation except its
	// conclusion — every subscriber had to spell the odd name out separately.
	"plan.done": func() Event { return &PlanDoneEvent{} },
}

func init() {
	for topic, factory := range catalog {
		Register(topic, factory)
	}
	for topic, factory := range legacyTopics {
		Register(topic, factory)
	}
}
