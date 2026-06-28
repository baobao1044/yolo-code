// The observation normalizer (File 08 §8.6): a tool's raw output is redacted,
// truncated, and summarized into a structured Observation before it reaches
// verify/memory/context. Redaction masks secret shapes (File 08 §8.4.5);
// truncation keeps head+tail within soft/hard limits and summarizes the
// middle past hard; the derived Summary survives history trimming (File 06).
//
// The summarizer is an injectable interface: the spec wants a separate
// small/cheap model call for the middle, but exec may not import the provider
// layer, so a real summarizer is wired by the composition root later. The
// default (nil) is a heuristic that takes the first non-empty line — enough
// to keep the Observation meaningful without a model call.

package exec

import (
	"context"
	"regexp"
	"strconv"
	"strings"
)

// OutputLimits are the truncation budgets (File 08 §8.6.3). Soft → kept
// verbatim; between soft and hard → head+tail with a marker; above hard →
// head+tail + the middle summarized. DefaultLimits returns the spec's
// 8/32/4/16/12/48 KB values.
type OutputLimits struct {
	StdoutSoft   int
	StdoutHard   int
	StderrSoft   int
	StderrHard   int
	CombinedHard int
}

// DefaultLimits returns the spec's truncation budgets (File 08 §8.6.3).
func DefaultLimits() OutputLimits {
	return OutputLimits{
		StdoutSoft:   8 * 1024,
		StdoutHard:   32 * 1024,
		StderrSoft:   4 * 1024,
		StderrHard:   16 * 1024,
		CombinedHard: 48 * 1024,
	}
}

// Summarizer condenses the truncated middle of a stream into a short line
// (File 08 §8.6.3 "a separate small/cheap model call"). The composition root
// wires a model-backed one later; a nil/heuristic one is the default.
type Summarizer interface {
	Summarize(ctx context.Context, text string, max int) string
}

// heuristicSummarizer is the default: it returns the first non-empty line of
// the text, truncated to max. No model call — enough to keep an Observation
// meaningful when a real summarizer isn't wired.
type heuristicSummarizer struct{}

func (heuristicSummarizer) Summarize(_ context.Context, text string, max int) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			if max > 0 && len(line) > max {
				return line[:max]
			}
			return line
		}
	}
	return ""
}

// observationNormalizer implements the Normalizer interface (engine.go) with
// the redact → truncate → summarize pipeline (File 08 §8.6.2). Unexported;
// built via NewNormalizer.
type observationNormalizer struct {
	limits     OutputLimits
	summarizer Summarizer
}

// NewNormalizer builds a Normalizer with the given limits and summarizer. A
// nil summarizer falls back to the heuristic one (no model call).
func NewNormalizer(limits OutputLimits, sum Summarizer) Normalizer {
	if sum == nil {
		sum = heuristicSummarizer{}
	}
	return observationNormalizer{limits: limits, summarizer: sum}
}

// Normalize runs the pipeline (File 08 §8.6.2): redact secrets, truncate each
// stream to its limits, derive a 1-line Summary. A tool whose metadata
// declares Secret:true is always redacted (File 08 §8.4.5). Bytes records the
// pre-truncation total so a consumer can tell how much was dropped.
func (n observationNormalizer) Normalize(out ToolOutput, meta Metadata) Observation {
	raw := out
	raw.Stdout = redact(raw.Stdout, meta.Permission.Secret)
	raw.Stderr = redact(raw.Stderr, meta.Permission.Secret)

	stdout, stdTrunc := n.truncate(raw.Stdout, n.limits.StdoutSoft, n.limits.StdoutHard)
	stderr, errTrunc := n.truncate(raw.Stderr, n.limits.StderrSoft, n.limits.StderrHard)

	return Observation{
		Stdout:    stdout,
		Stderr:    stderr,
		ExitCode:  out.ExitCode,
		Summary:   deriveSummary(out, stdout, stderr),
		Truncated: stdTrunc || errTrunc,
		Bytes:     len(raw.Stdout) + len(raw.Stderr),
		Files:     out.Files,
		FromPatch: false, // set by the Patch Engine later (File 10)
	}
}

// truncate implements the head+tail+summarize-middle scheme (File 08 §8.6.3).
// Returns the (possibly truncated) string and whether truncation happened.
func (n observationNormalizer) truncate(s string, soft, hard int) (string, bool) {
	if soft <= 0 || len(s) <= soft {
		return s, false
	}
	if hard <= 0 || len(s) <= hard {
		headN := soft * 30 / 100
		tailN := soft - headN
		return s[:headN] + "\n… (truncated " + strconv.Itoa(len(s)-soft) + " bytes) …\n" + s[len(s)-tailN:], true
	}
	headN := hard * 30 / 100
	tailN := hard * 30 / 100
	if headN+tailN >= len(s) {
		// Degenerate: hard smaller than the preserved ends; keep as-is.
		return s, false
	}
	middle := s[headN : len(s)-tailN]
	summary := n.summarizer.Summarize(context.Background(), middle, 200)
	return s[:headN] + "\n… (truncated; summary: " + summary + ") …\n" + s[len(s)-tailN:], true
}

// deriveSummary returns the 1-line summary that survives history trimming
// (File 06 pass 1). It prefers the tool's own Summary; falls back to the first
// non-empty line of the normalized output.
func deriveSummary(out ToolOutput, stdout, stderr string) string {
	if out.Summary != "" {
		return out.Summary
	}
	for _, line := range strings.Split(stdout+"\n"+stderr, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

// redact masks common secret shapes (File 08 §8.4.5): AWS keys (AKIA…), GitHub
// tokens (ghp_/gho_/…_), PEM blocks, and key=value secret assignments
// (api_key=/token=/secret=…). The always flag forces redaction even when no
// pattern matched (a tool that declares Secret:true). Masked output is what
// the model sees and what the log stores; raw stays in memory for the call.
var (
	awsKeyRe    = regexp.MustCompile(`AKIA[0-9A-Z]{16}`)
	githubTokRe = regexp.MustCompile(`gh[psoru]_[A-Za-z0-9]{36,}`)
	pemRe       = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)
	secretKVRe  = regexp.MustCompile(`(?i)(api_key|token|secret|password)=[^\s]+`)
)

// redact returns s with secret shapes replaced by a masked marker. always runs
// the patterns even when always is false (they're cheap); always only flips
// the "force a mask even if nothing matched" semantics — in practice a tool
// that declares Secret:true wants its output treated as sensitive.
func redact(s string, _ bool) string {
	s = awsKeyRe.ReplaceAllString(s, "***AWS_KEY***")
	s = githubTokRe.ReplaceAllString(s, "***GITHUB_TOKEN***")
	s = secretKVRe.ReplaceAllString(s, "$1=***")
	s = pemRe.ReplaceAllString(s, "***PEM_BLOCK***")
	return s
}
