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
	"bytes"
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/prompt"
	"github.com/baobao1044/yolo-code/internal/session"
)

// PatchOp is a corrective patch reflection may propose (File 07 §7.3.1). The
// Patch Engine (File 10) gives it meaning.
//
// Path names the file to patch. It exists because without it the op could not
// be applied at all: the composition root's patch adapter needs a target, takes
// it from the tool-call JSON or from the runtime PatchOp's Path, and had
// neither — so every corrective patch was refused with "missing target path"
// before it reached the engine. An empty Path is still valid and means "the
// files the failing verdict covered"; the runtime resolves it that way.
type PatchOp struct {
	Path string
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

// PatchCandidate is one generated corrective patch plus a rerank score (File 07
// §7.3.4). ReflectMulti produces several of these for a single verify failure;
// RerankCandidates scores and sorts them best-first. The Score is set by the
// reranker (higher is better) and is zero until reranked. Reason carries the
// reflection note that produced the candidate, so the caller can surface why a
// patch was proposed (and, in the cost-degraded single-candidate case, carry an
// abort note when no patch was proposed).
type PatchCandidate struct {
	Patch  PatchOp
	Score  float64 // higher is better; set by RerankCandidates
	Reason string  // the reflection note that produced this candidate
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
	// If the answer is "patch", the two things the patch engine needs must be
	// asked for, or the note is all there is to apply — which is what used to
	// be handed to it.
	b.WriteString("\nIf the decision is patch, also give a line 'PATH: <file>' naming the" +
		" single file to change, and put the SEARCH/REPLACE blocks in a fenced code block.")
	return []prompt.Message{{Role: "user", Content: b.String()}}
}

// decisionNoise is the markdown emphasis and punctuation real models wrap the
// decision keyword in — `DECISION: **abort**`, "DECISION: abort.", DECISION:
// `patch`. Matching the keyword exactly rejected all of those and fell through
// to the default replan, so a model that clearly said abort got another retry.
const decisionNoise = "*_`~'\"“”‘’.,;:!?()[]{}<>"

// pathFromNote pulls the target file out of a "PATH: <file>" line, matched the
// same tolerant way as the decision marker: case-insensitive, anywhere on its
// line, with the emphasis and punctuation models wrap values in trimmed off.
// Returns "" when the note names no file — a valid answer, meaning "the files
// the failing verdict covered", which is how the runtime resolves an empty
// target.
func pathFromNote(note string) string {
	const marker = "path:"
	for _, line := range strings.Split(note, "\n") {
		idx := strings.Index(strings.ToLower(line), marker)
		if idx < 0 {
			continue
		}
		p := strings.TrimSpace(line[idx+len(marker):])
		// One path, first token — a trailing clause ("PATH: x.go (the caller)")
		// is prose, not part of the name.
		if sp := strings.IndexAny(p, " \t"); sp >= 0 {
			p = p[:sp]
		}
		if p = strings.Trim(p, decisionNoise); p != "" {
			return p
		}
	}
	return ""
}

// patchBodyFromNote extracts the fenced code block a patch decision is supposed
// to carry. The patch engine's validator expects SEARCH/REPLACE blocks, and
// what it used to get was the entire reflection — root-cause prose, the
// DECISION line and all — because the note was the body. That could only be
// rejected.
//
// A note with no fence falls back to the whole note, which is the old
// behaviour: the mock provider and the scripted reflections in the tests emit
// bare notes, and a fenced-only rule would turn their patch decisions into
// empty bodies. The fallback still cannot validate, but it fails where it
// failed before rather than somewhere new.
func patchBodyFromNote(note string) string {
	const fence = "```"
	start := strings.Index(note, fence)
	if start < 0 {
		return note
	}
	// Skip the opening fence and its info string ("```diff") to the newline
	// that ends that line; a fence with no newline after it opens nothing.
	nl := strings.IndexByte(note[start:], '\n')
	if nl < 0 {
		return note
	}
	rest := note[start+nl+1:]
	end := strings.Index(rest, fence)
	if end < 0 {
		return note // unterminated fence: take the note rather than guess
	}
	if body := strings.TrimSpace(rest[:end]); body != "" {
		return body
	}
	return note
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
		choice = strings.Trim(choice, decisionNoise)
		switch choice {
		case "replan":
			dec.Replan = true
		case "patch":
			dec.Patch = PatchOp{Path: pathFromNote(note), Body: []byte(patchBodyFromNote(note))}
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

// ReflectMulti generates up to maxN candidate corrective patches for a verify
// failure, then returns them for reranking (File 07 §7.3.4). It reuses the
// single-path reflection's prompt-building and provider streaming: each
// candidate is one reflection turn with a varied instruction ("propose an
// alternative fix, distinct from previous"), so the model explores more than
// one corrective hypothesis before the runtime commits to one.
//
// Degenerate and cost-degraded cases collapse to a single candidate — the same
// decision a single Reflect would produce, wrapped as a 1-element slice:
//   - maxN <= 1 is the degenerate multi case (one candidate requested);
//   - allowMulti false is the cost-degraded mode (the Cost Controller's
//     MultiCandidateAllowed returned false, File 07 §7.6.2);
//   - a retry cap already reached collapses to the single-path abort decision.
//
// ReflectMulti is additive: it does not change Reflect's signature or the
// shape of its decision. The actual LLM call is the Core's provider seam, so a
// Core wired with the stub/mock provider yields deterministic candidates (S5).
// It returns an error only when the provider or stream fails (then no
// candidates are returned); retry-cap and abort outcomes are surfaced as
// candidates, not errors, mirroring Reflect's non-error abort.
func (c *Core) ReflectMulti(ctx context.Context, task *session.Task, v Verdict, obs Observation, maxN int, allowMulti bool) ([]PatchCandidate, error) {
	// Degenerate / cost-degraded: exactly one candidate, the same decision a
	// single Reflect would produce (File 07 §7.3.4). The retry-cap check lives
	// inside Reflect, so a task already at its cap surfaces the abort here too.
	if !allowMulti || maxN <= 1 || task.Retry >= task.RetryMax {
		dec := c.Reflect(ctx, task, v, obs)
		return []PatchCandidate{{Patch: dec.Patch, Reason: dec.Note}}, nil
	}

	// Multi-candidate path: issue up to maxN reflection turns with a varied
	// instruction, collecting each turn's proposed patch as a candidate.
	var cands []PatchCandidate
	for i := 0; i < maxN; i++ {
		// Re-check the retry cap each turn (each turn consumes one retry, mirroring
		// Reflect's per-call increment) so a tight cap stops multi-candidate
		// generation mid-loop rather than blowing past it.
		if task.Retry >= task.RetryMax {
			break
		}
		msgs := c.multiCandidatePrompt(task, v, obs, i, cands)
		stream, err := c.provider.Stream(ctx, Request{Messages: msgs})
		if err != nil {
			return nil, fmt.Errorf("reflection: provider error: %w", err)
		}
		var buf strings.Builder
		for chunk := range stream {
			if chunk.Err != nil {
				return nil, fmt.Errorf("reflection: stream error: %w", chunk.Err)
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

		dec := parseReflection(note)
		if dec.Abort {
			// Abort surfaces as a final, patch-less candidate (its Reason carries
			// the abort note) and stops multi-candidate generation.
			cands = append(cands, PatchCandidate{Reason: note})
			break
		}
		// The note is the model's proposed corrective action; wrap it as the
		// candidate patch body, mirroring the single-path patch decision.
		cands = append(cands, PatchCandidate{
			Patch:  PatchOp{Path: pathFromNote(note), Body: []byte(patchBodyFromNote(note))},
			Reason: note,
		})
	}
	return cands, nil
}

// multiCandidatePrompt builds the messages for one multi-candidate reflection
// turn. It reuses the single-path reflectionPrompt and appends a varied
// instruction so each turn proposes a distinct corrective hypothesis: the
// candidate index and the count of already-generated candidates nudge the
// model away from repeating itself.
func (c *Core) multiCandidatePrompt(task *session.Task, v Verdict, obs Observation, idx int, prior []PatchCandidate) []prompt.Message {
	msgs := c.reflectionPrompt(task, v, obs)
	var b strings.Builder
	b.WriteString("\n\nPropose corrective patch candidate #")
	b.WriteString(strconv.Itoa(idx + 1))
	b.WriteString(". Make it distinct from prior proposals")
	if len(prior) > 0 {
		b.WriteString(" (a different root cause or location); ")
		b.WriteString(strconv.Itoa(len(prior)))
		b.WriteString(" prior candidate(s) produced")
	} else {
		b.WriteString(" — this is the first candidate")
	}
	b.WriteString(". End with a line 'DECISION: patch' or 'DECISION: abort'.")
	msgs[0].Content += b.String()
	return msgs
}

// RerankCandidates scores and sorts patch candidates best-first (File 07
// §7.3.4). The base score favors earlier proposals (the first reflection turn is
// the model's primary fix): score = 1.0 - 0.1*index. A content heuristic then
// penalizes any candidate whose patch body repeats a previously failed patch
// body, so the reranker does not re-propose a fix that already failed
// verification. The returned slice is new (the input is not mutated), sorted by
// score descending with ties broken by original order (stable).
func RerankCandidates(cs []PatchCandidate, failedBodies [][]byte) []PatchCandidate {
	out := make([]PatchCandidate, len(cs))
	copy(out, cs)
	for i := range out {
		out[i].Score = 1.0 - 0.1*float64(i)
		for _, fb := range failedBodies {
			if bytes.Equal(out[i].Patch.Body, fb) {
				out[i].Score -= 0.5 // penalize a repeat of a failed patch
				break
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})
	return out
}
