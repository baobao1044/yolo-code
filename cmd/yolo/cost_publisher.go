// Cost accrual wiring (Sprint 13 S13-003).
// Subscribes to tool.result and publishes one cost.incurred per completed tool
// call, feeding the TUI cost rail and the JSONL transcript.
//
// What this file is allowed to claim (4.10): the only quantity it measures is
// the tool-call count. Token usage does now exist in the tree — the
// OpenAI-compat provider asks for it (stream_options.include_usage) and
// forwards what a server reports, cognitive.Turn carries the counts behind a
// UsageKnown flag, and the drive loop feeds cognitive.Cost.AddTokens whenever
// that flag is true — but none of it arrives here. Those counts travel by
// return value from Think into the runtime's ledger; the bus never carries
// them. This publisher's subscription is tool.result, whose payload is a task,
// a tool name and an observation, and event.TokenEvent is still a text delta
// with no counts. So we can see that a tool ran and nothing whatsoever about
// what the turn cost. Dollars is therefore 0 and Reason says "unpriced" unless
// the operator supplies rates in YOLO_COST_RATES; when they do, the number is
// theirs and Reason labels it an estimate. The previous default table
// (bash=$0.005, patch=$0.010, verify=$0.020, …) corresponded to nothing
// measured, sourced, or published, yet reached the user as dollars; it is gone
// rather than re-tuned.
//
// Token spend has its own home next door (cost_ledger.go), priced by
// YOLO_COST_PER_TOKEN inside the drive loop where a cap can actually stop a
// run. Two accrual paths with disjoint inputs: neither figure is the other's
// total, and nothing should add them. The one rule both obey is the one
// UsageKnown was added for — a turn nobody counted is not a free turn, and must
// never be rendered as a confident $0.
//
// Bus manners (4.8b): the subscription is narrow on purpose. It used to be the
// root topic ">", so the cost.incurred published from inside run's own drain
// loop came straight back into the channel that loop reads. Under a producer
// faster than the loop the 64-slot buffer saturates, the producer parks inside
// the subscriber's gate still holding it, and the loop then blocks forever
// inside Publish waiting for a turn only it can grant — a hard deadlock, not
// merely wasted work, and one that Stop cannot break. The invariant to keep:
// run must never subscribe to a topic run publishes.

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/baobao1044/yolo-code/internal/event"
)

// Operator knobs. Every one is opt-in: unset means "count tool calls, price
// nothing, cap nothing".
const (
	// costRatesEnv holds comma-separated "tool=dollars" pairs, e.g.
	// "bash=0.005,read_file=0.001". The key "*" sets the rate for tools not
	// named explicitly. There is no default: a rate the operator did not give
	// us is a rate nobody can vouch for.
	costRatesEnv = "YOLO_COST_RATES"

	// maxToolCallsEnv caps total tool invocations for the run. Unset or <= 0
	// means no cap. This is a ceiling on a measured quantity, so it means the
	// same thing with or without rates.
	maxToolCallsEnv = "YOLO_MAX_TOOL_CALLS"

	// maxCostEnv caps the accrued estimate in dollars. Unset or <= 0 means no
	// cap, and here it can only ever fire alongside costRatesEnv — with no rates
	// the estimate stays at 0. newCostLedger reads the same variable as the
	// ledger's hard spend cap over token dollars: one name and one number, but
	// two ceilings over two different inputs, so crossing one says nothing about
	// the other.
	maxCostEnv = "YOLO_MAX_COST"
)

// costTopic is the single topic run reads. Keep it disjoint from everything run
// publishes (cost.incurred, error) — see the file header.
const costTopic = event.Topic("tool.result")

// costPublisher counts tool calls and emits cost.incurred on every completed
// tool result. It only observes the bus: it can warn when a ceiling is crossed,
// but it cannot halt a run.
type costPublisher struct {
	bus       *event.Bus
	startOnce sync.Once
	stopOnce  sync.Once
	stop      chan struct{}
	done      chan struct{}
	ch        <-chan event.Envelope // set by Start; read by Stop to unsubscribe

	rates    map[string]float64 // operator-supplied; empty means "price nothing"
	maxCalls int
	maxCost  float64

	mu        sync.Mutex
	started   bool
	calls     int
	dollars   float64
	failed    int  // cost.incurred publishes that failed for a reason we can act on
	lostOnEnd int  // accruals the bus refused because it was already closed
	warnCalls bool // tool-call ceiling warned once
	warnCost  bool // estimate ceiling warned once
}

// newCostPublisher returns a cost publisher configured from the environment.
// With no configuration it reports tool calls at $0 and enforces no ceiling.
func newCostPublisher(bus *event.Bus) *costPublisher {
	rates, bad := parseCostRates(os.Getenv(costRatesEnv))
	for _, s := range bad {
		fmt.Fprintf(os.Stderr, "yolo: ignoring unparseable %s entry %q\n", costRatesEnv, s)
	}
	return &costPublisher{
		bus:      bus,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		rates:    rates,
		maxCalls: envInt(maxToolCallsEnv),
		maxCost:  envFloat(maxCostEnv),
	}
}

// Start subscribes to tool.result and starts the accrual goroutine. It is
// idempotent.
func (c *costPublisher) Start(ctx context.Context) {
	c.startOnce.Do(func() {
		ch := c.bus.Subscribe(costTopic)
		c.mu.Lock()
		c.ch, c.started = ch, true
		c.mu.Unlock()
		go c.run(ctx, ch)
	})
}

// Stop detaches the subscription and waits for the goroutine to drain. It is
// safe to call twice, and safe to call on a publisher that was never started —
// which is exactly what an early error return on a wiring path does. Detaching
// before the wait also releases any fan-out parked on our buffer, so a Stop
// cannot strand a publisher on a channel nobody will read again. It still
// inherits the bus's backpressure: if run is mid-Publish to a stalled
// subscriber, only that subscriber draining, the Start context, or bus.Close
// releases it.
func (c *costPublisher) Stop() {
	c.stopOnce.Do(func() {
		c.mu.Lock()
		started, ch := c.started, c.ch
		c.mu.Unlock()
		if !started {
			return
		}
		c.bus.Unsubscribe(ch)
		close(c.stop)
		<-c.done

		c.mu.Lock()
		failed := c.failed
		c.mu.Unlock()
		if failed > 0 {
			fmt.Fprintf(os.Stderr, "yolo: %d cost.incurred events were not delivered; the cost figures for this run are incomplete\n", failed)
		}
	})
}

func (c *costPublisher) run(ctx context.Context, ch <-chan event.Envelope) {
	defer close(c.done)
	for {
		select {
		case env, ok := <-ch:
			if !ok {
				return
			}
			res, ok := env.Evt.(*event.ToolResultEvent)
			if !ok {
				continue
			}
			c.accrue(ctx, env.Evt.CausalID(), res.Tool)
		case <-c.stop:
			return
		case <-ctx.Done():
			return
		}
	}
}

// accrue records one tool call, publishes the accrual, and warns once per
// ceiling that this call crossed.
func (c *costPublisher) accrue(ctx context.Context, task event.TaskID, tool string) {
	rate, priced := costFor(tool, c.rates)

	c.mu.Lock()
	c.calls++
	c.dollars += rate
	calls, dollars := c.calls, c.dollars
	overCalls := c.maxCalls > 0 && calls >= c.maxCalls && !c.warnCalls
	overCost := c.maxCost > 0 && dollars >= c.maxCost && !c.warnCost
	c.warnCalls = c.warnCalls || overCalls
	c.warnCost = c.warnCost || overCost
	c.mu.Unlock()

	if err := c.bus.Publish(ctx, &event.CostIncurredEvent{
		Task:    task,
		Tool:    tool,
		Dollars: rate,
		Reason:  accrualReason(priced),
	}); err != nil {
		// A publish that lost to Close is a shutdown-ordering artifact, not a
		// fault: every caller closes the bus before calling Stop, so the last
		// accrual or two can miss the transcript. It is counted separately
		// because by then there is no subscriber left to tell — the fix is the
		// caller's Stop-then-Close ordering, not a warning here.
		c.mu.Lock()
		if errors.Is(err, event.ErrBusClosed) {
			c.lostOnEnd++
		} else {
			c.failed++
		}
		c.mu.Unlock()
	}

	if overCalls {
		c.warn(ctx, task, "cost_ceiling_tool_calls", fmt.Sprintf(
			"tool-call ceiling crossed: %d calls (%s=%d). This is a warning — the run is not halted.",
			calls, maxToolCallsEnv, c.maxCalls))
	}
	if overCost {
		c.warn(ctx, task, "cost_ceiling_estimate", fmt.Sprintf(
			"estimated-spend ceiling crossed: $%.4f (%s=%.4f). The figure is an estimate from your %s rates, not a bill. This is a warning — the run is not halted.",
			dollars, maxCostEnv, c.maxCost, costRatesEnv))
	}
}

// warn publishes a ceiling notice. Retry is false: nothing here is retryable,
// and nothing here aborts — the publisher observes the bus, it does not drive
// the runtime.
func (c *costPublisher) warn(ctx context.Context, task event.TaskID, code, msg string) {
	_ = c.bus.Publish(ctx, &event.ErrorEvent{
		Task:  task,
		Layer: "cost",
		Code:  code,
		Msg:   msg,
		Retry: false,
	})
}

// costFor returns the operator's rate for tool and whether one was configured.
// An unconfigured tool is worth $0 and is reported as unpriced, never guessed.
func costFor(tool string, rates map[string]float64) (float64, bool) {
	if r, ok := rates[tool]; ok {
		return r, true
	}
	if r, ok := rates["*"]; ok {
		return r, true
	}
	return 0, false
}

// accrualReason spells out, on every event, where the number came from — and,
// since token counts stop at the ledger and never reach this subscription, what
// it leaves out. It must not tell the operator the build cannot report token
// usage: providers that honour stream_options.include_usage do, it is simply
// billed elsewhere.
func accrualReason(priced bool) string {
	if priced {
		return "estimate: 1 tool call at the rate configured in " + costRatesEnv + " (tool calls only; token spend is not in this figure)"
	}
	return "1 tool call; unpriced (no " + costRatesEnv + " rate configured). Token spend is never part of this figure — it is the run's cost ledger that prices it, via " + tokenRateEnv + "."
}

// parseCostRates parses "tool=dollars" pairs. It returns the rates it
// understood plus the entries it could not, so the caller can say out loud that
// a knob the operator set is not in effect. Negative rates are rejected.
func parseCostRates(s string) (map[string]float64, []string) {
	var bad []string
	rates := map[string]float64{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		tool, amount, ok := strings.Cut(pair, "=")
		tool = strings.TrimSpace(tool)
		v, err := strconv.ParseFloat(strings.TrimSpace(amount), 64)
		if !ok || tool == "" || err != nil || v < 0 {
			bad = append(bad, pair)
			continue
		}
		rates[tool] = v
	}
	if len(rates) == 0 {
		return nil, bad
	}
	return rates, bad
}

// envInt reads a non-negative integer knob; anything unset or unparseable is 0,
// which every caller reads as "off".
func envInt(key string) int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// envFloat reads a non-negative float knob; unset or unparseable is 0 ("off").
func envFloat(key string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv(key)), 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}
