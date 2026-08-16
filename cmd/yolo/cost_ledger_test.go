// Tests for the env → ledger mapping.
//
// As in cost_publisher_test.go the knob names are spelled as literals: they are
// the operator-facing contract, so renaming one should break these tests rather
// than silently disarm a cap somebody set in their shell.
//
// Every test clears both knobs explicitly with t.Setenv even when it only cares
// about one. The ledger reads the ambient process environment, so a knob left
// set by the operator running `go test` — or by a sibling test — would otherwise
// decide the outcome. t.Setenv cannot unset a variable, but "" is unparseable to
// both envFloat and envDuration and therefore means the same thing as unset.

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/baobao1044/yolo-code/internal/runtime"
	"github.com/baobao1044/yolo-code/internal/session"
)

// clearCostKnobs unsets every knob newCostLedger reads, so each test starts from
// a known-uncapped environment and states its own configuration.
func clearCostKnobs(t *testing.T) {
	t.Helper()
	t.Setenv(maxCostEnv, "")
	t.Setenv(maxTimeEnv, "")
	t.Setenv(tokenRateEnv, "")
}

// TestNewCostLedgerNilWhenUncapped pins the uncapped-by-default contract: with
// no hard cap configured there is no ledger at all, so runtime.New installs its
// noop stub and no per-task bookkeeping is armed.
//
// The assertion is on the interface value being nil, and it is load-bearing in a
// way that is easy to lose. A refactor that returned a zero-cap *cognitive.Cost
// instead would satisfy any "it does not trip" check while quietly registering,
// mutex-locking and accumulating state for every task on a run nobody asked to
// budget — and, because a non-nil interface holding a nil-ish value still reads
// as non-nil, runtime.New would wire it rather than the stub.
func TestNewCostLedgerNilWhenUncapped(t *testing.T) {
	clearCostKnobs(t)

	if ledger := newCostLedger(); ledger != nil {
		t.Errorf("newCostLedger() = %T (non-nil) with no cap configured, want nil so the runtime installs its noop stub", ledger)
	}
}

// TestNewCostLedgerArmedByMaxCost pins that the spend knob alone is enough to
// build a ledger. The rate knob is deliberately left unset: a spend cap with no
// price per token can never fire, but the operator asking for one is still a
// request for the ledger to exist.
func TestNewCostLedgerArmedByMaxCost(t *testing.T) {
	clearCostKnobs(t)
	t.Setenv(maxCostEnv, "5.00")

	if newCostLedger() == nil {
		t.Errorf("newCostLedger() = nil with %s set; the operator's spend cap was silently discarded", maxCostEnv)
	}
}

// TestNewCostLedgerArmedByMaxTime pins the same for the wall-clock knob. This is
// the cap that matters most in practice: a provider that reports no token usage
// can never reach the spend cap, so time is the only thing that bounds a task
// looping productively-looking but getting nowhere.
func TestNewCostLedgerArmedByMaxTime(t *testing.T) {
	clearCostKnobs(t)
	t.Setenv(maxTimeEnv, "30s")

	if newCostLedger() == nil {
		t.Errorf("newCostLedger() = nil with %s set; the operator's wall-clock cap was silently discarded", maxTimeEnv)
	}
}

// TestEnvDuration covers the parse. Everything that is not a well-formed
// non-negative Go duration collapses to 0, which every caller reads as "no cap"
// — the safe direction for a misspelled knob is to leave the run uncapped, not
// to kill it instantly with a zero or negative deadline.
func TestEnvDuration(t *testing.T) {
	const key = "YOLO_TEST_DURATION"
	cases := []struct {
		name string
		set  bool
		val  string
		want time.Duration
	}{
		{name: "unset", set: false, want: 0},
		{name: "empty", set: true, val: "", want: 0},
		{name: "seconds", set: true, val: "90s", want: 90 * time.Second},
		{name: "minutes", set: true, val: "10m", want: 10 * time.Minute},
		{name: "compound", set: true, val: "1h30m", want: 90 * time.Minute},
		{name: "surrounding whitespace", set: true, val: "  5m  ", want: 5 * time.Minute},
		{name: "garbage", set: true, val: "soon", want: 0},
		// A bare number is the most likely operator typo: it looks like seconds
		// but Go rejects it for lacking a unit, and guessing a unit on their
		// behalf would apply a cap they did not write.
		{name: "unitless number", set: true, val: "600", want: 0},
		// Negative is well-formed and parses, so it has to be rejected on value:
		// a negative deadline is already in the past and would abort the first
		// task on its first turn.
		{name: "negative", set: true, val: "-5m", want: 0},
		{name: "zero", set: true, val: "0s", want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(key, tc.val)
			}
			if got := envDuration(key); got != tc.want {
				t.Errorf("envDuration(%q=%q) = %v, want %v", key, tc.val, got, tc.want)
			}
		})
	}
}

// TestCostLedgerTimeCapTrips is the behavioural half: it is not enough that a
// knob produces a non-nil ledger, the cap it configures has to actually stop a
// task. A wiring bug that dropped MaxTime on the floor — leaving the ledger on
// DefaultCostConfig's 10 minutes — would pass every constructor test above and
// still let a run the operator capped at a second go on for ten.
func TestCostLedgerTimeCapTrips(t *testing.T) {
	clearCostKnobs(t)
	// Small enough that the test does not idle, large enough that the register →
	// poll round trip is not racing the deadline it is meant to observe.
	t.Setenv(maxTimeEnv, "20ms")

	ledger := newCostLedger()
	if ledger == nil {
		t.Fatalf("newCostLedger() = nil with %s set", maxTimeEnv)
	}

	const id = session.TaskID("t_timecap")
	ledger.RegisterTask(id)

	// Before the deadline nothing has been exceeded: a cap that read as tripped
	// the instant it was registered would kill every task on its first turn,
	// which is the failure mode RegisterTask's zero-MaxTime guard exists for.
	if over, cap := ledger.HardCapExceeded(id); over {
		t.Fatalf("HardCapExceeded = true (%q) immediately after RegisterTask, want false until the deadline passes", cap)
	}

	// Poll rather than sleeping exactly 20ms: a loaded CI box can overshoot, and
	// the assertion is "it trips once the time is up", not "it trips at 20ms".
	deadline := time.Now().Add(2 * time.Second)
	var (
		over bool
		cap  string
	)
	for time.Now().Before(deadline) {
		if over, cap = ledger.HardCapExceeded(id); over {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !over {
		t.Fatalf("HardCapExceeded still false 2s after a %s of 20ms; the wall-clock cap never fired", maxTimeEnv)
	}
	if cap != "time cap" {
		t.Errorf("HardCapExceeded named %q, want %q — the abort message tells the operator which knob stopped their run", cap, "time cap")
	}
}

// ledgerWarnings builds the ledger the way main does and returns both it and
// everything the operator would have seen on stderr.
func ledgerWarnings(t *testing.T) (runtime.CostLedger, string) {
	t.Helper()
	var out strings.Builder
	ledger := newCostLedgerTo(&out)
	return ledger, out.String()
}

// TestInertSpendCapIsAnnounced covers the configuration that is worse than
// having no cap at all: one the operator believes is protecting them.
//
// YOLO_MAX_COST alone builds a real ledger with a zero pricer. Every token is
// counted and valued at $0, so spend never moves, MaxDollars never trips, and
// the run looks — in the ledger, in the transcript, in the cost events —
// exactly like a run that stayed comfortably within budget. The second half of
// this test proves the warning is telling the truth rather than hedging: a
// million tokens do not move the cap an inch.
func TestInertSpendCapIsAnnounced(t *testing.T) {
	clearCostKnobs(t)
	t.Setenv(maxCostEnv, "5.00")

	ledger, warnings := ledgerWarnings(t)
	if ledger == nil {
		t.Fatalf("newCostLedger() = nil with %s set", maxCostEnv)
	}
	if !strings.Contains(warnings, maxCostEnv) || !strings.Contains(warnings, tokenRateEnv) {
		t.Errorf("startup said %q; a spend cap that cannot fire must name itself and the knob that would arm it (%s)", warnings, tokenRateEnv)
	}

	const id = session.TaskID("t_inert")
	ledger.RegisterTask(id)
	ledger.AddTokens(id, 1_000_000, 1_000_000)
	if over, cap := ledger.HardCapExceeded(id); over {
		t.Fatalf("HardCapExceeded = true (%q) with no %s set; this test's premise is stale", cap, tokenRateEnv)
	}
}

// TestPricedSpendCapTripsAndStaysQuiet is the control. With both knobs set the
// cap is real, so there must be nothing to warn about — a warning that fires on
// a correct configuration is noise, and noise is how the one that matters gets
// scrolled past.
func TestPricedSpendCapTripsAndStaysQuiet(t *testing.T) {
	clearCostKnobs(t)
	t.Setenv(maxCostEnv, "0.01")
	t.Setenv(tokenRateEnv, "0.001")

	ledger, warnings := ledgerWarnings(t)
	if ledger == nil {
		t.Fatal("newCostLedger() = nil with both spend knobs set")
	}
	if warnings != "" {
		t.Errorf("a fully configured spend cap warned anyway: %q", warnings)
	}

	const id = session.TaskID("t_priced")
	ledger.RegisterTask(id)
	ledger.AddTokens(id, 10, 10) // 20 tokens × $0.001 = $0.02, over the $0.01 cap
	over, cap := ledger.HardCapExceeded(id)
	if !over {
		t.Fatalf("HardCapExceeded = false after spending past %s=0.01", maxCostEnv)
	}
	if cap != "spend cap" {
		t.Errorf("HardCapExceeded named %q, want %q", cap, "spend cap")
	}
}

// TestUnparseableKnobIsAnnounced covers the typo. A knob that does not parse
// folds to 0, and 0 is the same value as "unset" everywhere downstream — so
// `YOLO_MAX_TIME=10` (no unit) or `YOLO_MAX_COST=$5` disarms the cap and leaves
// no trace. The parse is deliberately not made more forgiving: guessing a unit
// applies a limit the operator did not write. Saying so out loud is the fix.
func TestUnparseableKnobIsAnnounced(t *testing.T) {
	cases := []struct{ key, val string }{
		{maxTimeEnv, "600"}, // looks like seconds, has no unit
		{maxCostEnv, "$5"},  // looks like dollars, is not a float
		{tokenRateEnv, "tiny"},
	}
	for _, tc := range cases {
		t.Run(tc.key+"="+tc.val, func(t *testing.T) {
			clearCostKnobs(t)
			t.Setenv(tc.key, tc.val)

			_, warnings := ledgerWarnings(t)
			if !strings.Contains(warnings, tc.key) || !strings.Contains(warnings, tc.val) {
				t.Errorf("startup said %q; setting %s=%q had no effect and nothing said so", warnings, tc.key, tc.val)
			}
		})
	}
}

// TestUncappedRunWarnsAboutNothing pins the silence. Not configuring a cap is a
// choice, not a mistake, and a startup that grumbles about every unset knob
// trains operators to ignore the line that matters.
func TestUncappedRunWarnsAboutNothing(t *testing.T) {
	clearCostKnobs(t)

	ledger, warnings := ledgerWarnings(t)
	if ledger != nil {
		t.Errorf("newCostLedger() = %T with no knobs set", ledger)
	}
	if warnings != "" {
		t.Errorf("an uncapped run warned about caps nobody asked for: %q", warnings)
	}
}

// TestSpendCapDoesNotImposeAWallClock pins the deliberate discarding of
// DefaultCostConfig's MaxTime.
//
// The config is built from the defaults and then has MaxDollars and MaxTime
// overwritten, which reads like an accident — as if the 10-minute default were
// meant to survive an unset knob. It is not: inheriting it would mean that
// asking for a spend cap silently also kills any task running longer than ten
// minutes, a limit nobody configured and nothing announces. The runtime samples
// hard caps on every tool dispatch now, so such a task dies mid-turn.
func TestSpendCapDoesNotImposeAWallClock(t *testing.T) {
	clearCostKnobs(t)
	t.Setenv(maxCostEnv, "5.00")
	t.Setenv(tokenRateEnv, "0.000001")

	ledger := newCostLedger()
	if ledger == nil {
		t.Fatal("newCostLedger() = nil with both spend knobs set")
	}
	const id = session.TaskID("t_noclock")
	ledger.RegisterTask(id)
	if over, cap := ledger.HardCapExceeded(id); over {
		t.Fatalf("HardCapExceeded = true (%q) on a task that has spent nothing and run for no time", cap)
	}
}

// TestUncappedLedgerCannotTrip is the other side of the nil contract. With no
// knobs there is no ledger to ask, so nothing can trip: the absence of a ledger
// IS the uncapped behaviour, and this test fails loudly if newCostLedger ever
// starts handing back something that could stop a task nobody budgeted.
func TestUncappedLedgerCannotTrip(t *testing.T) {
	clearCostKnobs(t)

	ledger := newCostLedger()
	if ledger != nil {
		t.Fatalf("newCostLedger() = %T with no knobs set; an uncapped run must have no ledger at all", ledger)
	}
}
