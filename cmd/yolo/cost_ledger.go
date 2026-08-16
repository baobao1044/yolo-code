// The runtime's cost ledger, built from the operator's knobs.
//
// This is the half of cost control that can actually stop a run. The cost
// publisher next door only observes the bus: it can print a warning when a
// ceiling is crossed and then watch the run sail past it. The ledger sits in
// the drive loop, so when a hard cap trips the task is cancelled.
//
// Everything here is opt-in and nothing has a made-up default. An unset knob
// means that cap does not exist, which is the behaviour every previous build
// had — the difference is that a knob you DO set now does something.
// *cognitive.Cost satisfies runtime.CostLedger method-for-method on purpose
// (see the port doc), so there is no adapter here to forget to update.

package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/baobao1044/yolo-code/internal/cognitive"
	"github.com/baobao1044/yolo-code/internal/runtime"
)

const (
	// tokenRateEnv prices one token, in dollars, for the spend cap. Token
	// pricing needs its own knob: YOLO_COST_RATES is dollars-per-tool-call and
	// cannot be converted into a per-token rate. Unset means tokens accrue as
	// counts with no dollar value, so maxCostEnv can never fire.
	tokenRateEnv = "YOLO_COST_PER_TOKEN"

	// maxTimeEnv caps a task's wall clock, as a Go duration ("10m", "90s").
	// Unset means no deadline. This is the only cap that bounds a task which is
	// looping productively-looking but getting nowhere, since a provider that
	// reports no token usage can never reach the spend cap.
	maxTimeEnv = "YOLO_MAX_TIME"
)

// newCostLedger builds the runtime's cost ledger from the environment, or
// returns nil when the operator configured no hard cap at all.
//
// Returning nil rather than a zero-cap ledger is deliberate: runtime.New swaps
// in its noop stub, so the uncapped path holds no per-task state and the
// registration on every task start costs nothing. A ledger that counts spend
// nobody set a limit on would be bookkeeping for its own sake.
//
// MaxLoops and MaxReflections come from DefaultCostConfig unchanged. They are
// degradation rungs, not kill switches — nothing in the shipped tree reads them
// yet — and the drive loop deliberately does not abort on them: see
// cognitive.Cost.HardCapExceeded for why treating MaxLoops as a stop condition
// would turn a tuning threshold tuned for reflection into a six-turn cap on
// every task.
func newCostLedger() runtime.CostLedger { return newCostLedgerTo(os.Stderr) }

// newCostLedgerTo is newCostLedger with the warning stream injected, so a test
// can read what the operator would have been told. Warnings go to stderr rather
// than the bus: they describe the configuration the process started with, and
// the bus subscriber that would render them is not attached yet.
func newCostLedgerTo(w io.Writer) runtime.CostLedger {
	maxDollars := envFloat(maxCostEnv)
	maxTime := envDuration(maxTimeEnv)
	perToken := envFloat(tokenRateEnv)

	// A knob that was set but did not parse is the loudest version of the
	// failure this whole function is about: the operator asked for a limit,
	// envFloat/envDuration folded the typo to 0, and 0 reads as "no limit"
	// everywhere downstream. Indistinguishable, in the log, from not having
	// asked at all.
	warnDiscardedKnob(w, maxCostEnv, maxDollars <= 0, "a positive number of dollars")
	warnDiscardedKnob(w, maxTimeEnv, maxTime <= 0, "a positive Go duration such as 10m")
	warnDiscardedKnob(w, tokenRateEnv, perToken <= 0, "a positive dollars-per-token rate")

	// A spend cap with no price per token is armed and inert: tokens accumulate
	// as counts, the pricer values them at $0, and MaxDollars is compared
	// against a number that never moves. Nothing downstream can tell that apart
	// from a run that stayed within budget, so it has to be said here — this is
	// the one configuration where the ledger exists, reports no problem, and
	// enforces nothing.
	if maxDollars > 0 && perToken <= 0 {
		// Diagnostics only. If the warning writer itself fails there is
		// nowhere left to say so, and the run must not abort over it.
		_, _ = fmt.Fprintf(w, "yolo: %s=%g cannot fire without %s: tokens have no dollar value, "+
			"so spend stays at $0 all run. Set %s to the dollars one token costs, "+
			"or use %s for a cap that works on its own.\n",
			maxCostEnv, maxDollars, tokenRateEnv, tokenRateEnv, maxTimeEnv)
	}

	if maxDollars <= 0 && maxTime <= 0 {
		return nil
	}

	// DefaultCostConfig supplies MaxLoops and MaxReflections and nothing else.
	// Its MaxDollars ($1) and MaxTime (10m) are deliberately replaced rather
	// than used as fallbacks, and that is worth being explicit about because
	// the assignment reads like an accident: inheriting them would mean that
	// setting a spend cap also imposes a ten-minute wall clock the operator
	// never asked for, and — since a config with any cap set is a config the
	// nil check above lets through — that every run would end up capped by
	// default. A limit nobody configured killing a long task is the same class
	// of surprise as a configured limit that never fires.
	cfg := cognitive.DefaultCostConfig()
	cfg.MaxDollars = maxDollars
	cfg.MaxTime = maxTime

	// The pricer converts the provider's reported tokens into the dollars the
	// spend cap compares against. With no rate configured this is a zero pricer
	// — see the warning above.
	pricer := cognitive.FixedPricer{PerToken: perToken}
	// nil bus: the ledger's own cost.abort publish is suppressed because the
	// runtime publishes its own on the same event, naming the cap that fired.
	// Two aborts for one cause would double-count in the transcript.
	return cognitive.NewCost(cfg, pricer, nil)
}

// warnDiscardedKnob reports a knob the operator set that this process is not
// going to honour. Silence when the knob is unset: not configuring a cap is a
// choice, and warning about it would train operators to ignore the line that
// matters.
func warnDiscardedKnob(w io.Writer, key string, discarded bool, want string) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" || !discarded {
		return
	}
	_, _ = fmt.Fprintf(w, "yolo: ignoring %s=%q: expected %s. This run has no %s limit.\n",
		key, raw, want, key)
}

// envDuration reads a Go duration knob ("10m", "1h30m"). Unset, unparseable, or
// negative is 0, which every caller reads as "no cap".
func envDuration(key string) time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(os.Getenv(key)))
	if err != nil || d < 0 {
		return 0
	}
	return d
}
