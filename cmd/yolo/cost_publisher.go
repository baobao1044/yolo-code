// Cost accrual wiring (Sprint 13 S13-003).
// Listens to tool.call events and publishes cost.incurred so the coord plan
// budget can track spend across agents.

package main

import (
	"context"
	"sync"

	"github.com/yolo-code/yolo/internal/event"
)

// costPublisher maps tool names to a simple cost model and emits
// cost.incurred events on every completed tool result.
type costPublisher struct {
	bus      *event.Bus
	once     sync.Once
	stop     chan struct{}
	done     chan struct{}
	rates    map[string]float64
	defaults float64
}

// newCostPublisher returns a cost publisher that accrues a fixed amount per
// tool call. Use Rates to override; everything else uses defaultCents.
func newCostPublisher(bus *event.Bus) *costPublisher {
	return &costPublisher{
		bus:   bus,
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
		rates: defaultCostRates(),
	}
}

func defaultCostRates() map[string]float64 {
	return map[string]float64{
		"bash":   0.005,
		"read":   0.001,
		"patch":  0.010,
		"verify": 0.020,
	}
}

// Start subscribes to tool.call and starts the accrual goroutine. It is
// idempotent.
func (c *costPublisher) Start(ctx context.Context) {
	c.once.Do(func() {
		ch := c.bus.Subscribe(event.Topic(">"))
		go c.run(ctx, ch)
	})
}

// Stop closes the publisher and waits for the goroutine to drain.
func (c *costPublisher) Stop() {
	close(c.stop)
	<-c.done
}

func (c *costPublisher) run(ctx context.Context, ch <-chan event.Envelope) {
	defer close(c.done)
	for {
		select {
		case env, ok := <-ch:
			if !ok {
				return
			}
			call, ok := env.Evt.(*event.ToolResultEvent)
			if !ok {
				continue
			}
			_ = c.bus.Publish(ctx, &event.CostIncurredEvent{
				Task:    env.Evt.CausalID(),
				Tool:    call.Tool,
				Dollars: costFor(call.Tool, c.rates, 0.002),
				Reason:  "tool call",
			})
		case <-c.stop:
			return
		case <-ctx.Done():
			return
		}
	}
}

func costFor(tool string, rates map[string]float64, def float64) float64 {
	if r, ok := rates[tool]; ok {
		return r
	}
	return def
}
