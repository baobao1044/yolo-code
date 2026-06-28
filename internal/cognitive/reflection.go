// Reflection (File 07 §7.3). The cardinal rule: reflection does not call
// tools — it only thinks. When verification fails, the runtime asks the Core
// to reflect: it receives the observation, the verification verdict, and the
// task history, and produces a ReflectionDecision — never a side effect. The
// root-cause note is published as a reflection.note event and feeds the next
// PLAN iteration as context (§7.3.3).
//
// Sprint 3 implements the non-acting decision + note publish; the Patch field
// is a placeholder (File 10's patch.Op is not built yet) and the LLM-driven
// reflection reasoning uses the mock provider's scripted response. The retry
// cap (sourced from the Cost Controller, File 07 §7.6.4) is enforced here via
// task.Retry/RetryMax.

package cognitive

import (
	"context"
	"strings"

	"github.com/yolo-code/yolo/internal/event"
	"github.com/yolo-code/yolo/internal/prompt"
	"github.com/yolo-code/yolo/internal/session"
)

// PatchOp is a corrective patch reflection may propose (File 07 §7.3.1). The
// Patch Engine (File 10) gives it meaning; Sprint 3 carries it as a body
// placeholder so the decision shape is preserved without that layer.
type PatchOp struct {
	Body []byte
}

// ReflectionDecision is what reflection produces (File 07 §7.3.1): a root-cause
// note plus exactly one of three outcomes — replan (re-enter PLAN with the
// note appended), propose a patch, or abort. It is the decision the runtime
// acts on; reflection itself mutates nothing.
type ReflectionDecision struct {
	Note   string  // root-cause analysis (published as reflection.note)
	Replan bool    // re-enter PLAN with the note appended
	Patch  PatchOp // if Replan is false and a corrective patch is proposed
	Abort  bool    // give up; surface to user
}

// Verdict is a minimal verification verdict shape reflection consumes (File 09
// owns the full one). Sprint 3 carries Pass + Reason so reflection can cite
// the failure; the real verify.Verdict is wired when File 09 lands.
type Verdict struct {
	Pass   bool
	Reason string
}

// Observation is a minimal observation shape reflection consumes (File 08 owns
// the full one). Sprint 3 carries the textual result; the real
// exec.Observation is wired when File 08 lands.
type Observation struct {
	Text string
}

// Reflect runs one reflection turn (File 07 §7.3.2): if the retry cap is
// reached, abort (cost-controlled); otherwise stream the reflection provider,
// publish the root-cause note as reflection.note, increment the retry counter,
// and parse the decision (replan | patch | abort). It calls no tools and
// mutates only the task's retry counter (the cost ledger's reflection count is
// the Cost Controller's, landed in L6-006).
//
// The reflect provider is the same as the plan provider in Sprint 3 (the spec
// allows a cheaper model for reflection; wiring a separate one is a config
// choice deferred to Sprint 4).
func (c *Core) Reflect(ctx context.Context, task *session.Task, v Verdict, obs Observation) ReflectionDecision {
	// Retry cap: when retries are exhausted, reflection aborts rather than
	// loop forever (File 07 §7.3.2, §7.6.4). task.RetryMax is sourced from the
	// Cost Controller's MaxReflections at task start.
	if task.Retry >= task.RetryMax {
		return ReflectionDecision{Abort: true, Note: "retry cap reached; cost-controlled abort"}
	}

	stream, err := c.provider.Stream(ctx, Request{Messages: c.reflectionPrompt(task, v, obs)})
	if err != nil {
		return ReflectionDecision{Abort: true, Note: "reflection provider error: " + err.Error()}
	}

	var buf strings.Builder
	for chunk := range stream {
		if chunk.Err != nil {
			return ReflectionDecision{Abort: true, Note: "reflection stream error: " + chunk.Err.Error()}
		}
		if chunk.Delta != "" {
			buf.WriteString(chunk.Delta)
		}
	}

	note := buf.String()
	if c.bus != nil {
		_ = c.bus.Publish(ctx, &event.ReflectionEvent{Task: event.TaskID(task.ID), Note: note})
	}
	task.Retry++

	return parseReflection(note)
}

// reflectionPrompt builds the messages the reflection provider consumes (File
// 07 §7.3): the verdict, the observation, and the task goal/history. Sprint 3
// packs them into a single user message so the mock provider's scripted
// response stands in for the model's reasoning.
func (c *Core) reflectionPrompt(task *session.Task, v Verdict, obs Observation) []prompt.Message {
	var b strings.Builder
	b.WriteString("Reflect on the failed verification. Do NOT call tools.\n\n")
	b.WriteString("Task goal: ")
	b.WriteString(task.Goal)
	b.WriteString("\nVerdict: ")
	if v.Pass {
		b.WriteString("pass")
	} else {
		b.WriteString("fail")
	}
	if v.Reason != "" {
		b.WriteString(" — ")
		b.WriteString(v.Reason)
	}
	b.WriteString("\nObservation: ")
	b.WriteString(obs.Text)
	b.WriteString("\n\nGive a root-cause analysis and end with a line 'DECISION: replan|patch|abort'.")
	return []prompt.Message{{Role: "user", Content: b.String()}}
}

// parseReflection parses the reflection note into a decision (File 07 §7.3.2):
// the model's text determines replan | patch | abort. Sprint 3 uses a simple
// marker convention — a "DECISION: replan|patch|abort" anywhere in the note
// selects the outcome; everything else is the note. The marker is matched
// case-insensitively and may appear anywhere on its line (the root-cause prose
// often precedes it). This keeps reflection deterministic for the mock/golden
// path; a richer parse arrives with the real provider.
func parseReflection(note string) ReflectionDecision {
	dec := ReflectionDecision{Note: note}
	marker := "decision:"
	for _, line := range strings.Split(note, "\n") {
		idx := strings.Index(strings.ToLower(line), marker)
		if idx < 0 {
			continue
		}
		choice := strings.TrimSpace(strings.ToLower(line[idx+len(marker):]))
		// Take the first token after the marker (e.g. "replan" out of
		// "replan the patch"). The decision is a single keyword.
		if sp := strings.IndexByte(choice, ' '); sp >= 0 {
			choice = choice[:sp]
		}
		switch choice {
		case "replan":
			dec.Replan = true
		case "patch":
			dec.Patch = PatchOp{Body: []byte(note)}
		case "abort":
			dec.Abort = true
		default:
			dec.Replan = true // unknown choice → default replan
		}
		return dec
	}
	// No explicit marker: default to replan with the note (the model's analysis
	// feeds the next PLAN iteration, §7.3.3).
	dec.Replan = true
	return dec
}
