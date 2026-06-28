// Package event — catalog registration (L3-006).
//
// init registers every catalog event under its topic so the durability log
// (L3-004) can reconstruct concrete types on replay. The 16 topic groups from
// File 05 §5.4.9 are all covered here; the test in events_test.go asserts none
// is missing.

package event

func init() {
	// L1: Session & task.
	Register("task.started", func() Event { return &TaskStartedEvent{} })
	Register("task.completed", func() Event { return &TaskCompletedEvent{} })
	Register("task.cancelled", func() Event { return &TaskCancelledEvent{} })
	Register("task.paused", func() Event { return &TaskPausedEvent{} })
	Register("task.checkpoint", func() Event { return &CheckpointEvent{} })
	Register("task.restored", func() Event { return &RestoredEvent{} })
	Register("task.undone", func() Event { return &UndoneEvent{} })

	// L2: Runtime FSM.
	Register("state.change", func() Event { return &StateChangeEvent{} })
	Register("context.built", func() Event { return &ContextBuiltEvent{} })
	Register("approval.request", func() Event { return &ApprovalRequestEvent{} })
	Register("observation.received", func() Event { return &ObservationEvent{} })
	Register("verification.failed", func() Event { return &VerificationFailedEvent{} })
	Register("reflection.note", func() Event { return &ReflectionEvent{} })
	Register("patch.applied", func() Event { return &PatchAppliedEvent{} })

	// L6: Cognitive.
	Register("llm.token", func() Event { return &TokenEvent{} })
	Register("llm.thinking", func() Event { return &ThinkingEvent{} })
	Register("assistant.message", func() Event { return &AssistantMessageEvent{} })
	Register("tool.call", func() Event { return &ToolCallEvent{} })

	// L7: Execution.
	Register("tool.result", func() Event { return &ToolResultEvent{} })

	// L10: Memory.
	Register("memory.update", func() Event { return &MemoryUpdateEvent{} })

	// User (published by TUI, subscribed by L2).
	Register("user.submit", func() Event { return &UserSubmitEvent{} })
	Register("user.cancel", func() Event { return &UserCancelEvent{} })
	Register("user.approve", func() Event { return &UserApproveEvent{} })
	Register("user.reject", func() Event { return &UserRejectEvent{} })
	Register("user.pause", func() Event { return &UserPauseEvent{} })
	Register("user.resume", func() Event { return &UserResumeEvent{} })
	Register("user.quit", func() Event { return &UserQuitEvent{} })

	// L11: Coordination.
	Register("coord.task.assign", func() Event { return &TaskAssignEvent{} })
	Register("coord.plan.ready", func() Event { return &PlanReadyEvent{} })
	Register("coord.code.ready", func() Event { return &CodeReadyEvent{} })
	Register("coord.review.verdict", func() Event { return &ReviewVerdictEvent{} })
	Register("coord.test.report", func() Event { return &TestReportEvent{} })

	// Error (any layer).
	Register("error", func() Event { return &ErrorEvent{} })
}
