// Tests for L12-007 — Rate limiter: per-key token buckets (File 13 §13.9).
// The limiter is NOT event-driven — it's an API the exec/llm/mcp layers call
// directly (wired through the composition root in L12-009). Each key gets its
// own token bucket seeded with `burst` on first use; every Allow consumes one
// token and refills at `rate` tokens/sec. §13.9.2 fixes the bucket keys:
// llm:<provider>, tool:<name>, mcp:<server>.
//
// The exit bar (§13.9.2) is "throttled, not errored": a fast loop past the
// bucket's burst returns a nonzero wait + ok=false (the caller sleeps `wait`
// and retries); only a canceled context returns (0, false) and that path
// neither seeds nor consumes the bucket.

package infra

import (
	"context"
	"testing"
	"time"
)

// TestRateLimiterBurstAllowedImmediately pins §13.9.2: the first call per key
// seeds the bucket with `burst` tokens, so the first `burst` calls each
// consume one and return (0, true) — zero wait, allowed.
func TestRateLimiterBurstAllowedImmediately(t *testing.T) {
	cfg := testConfig() // Burst=10, Rate=2 (DefaultConfig)
	l := newRateLimiter(cfg)
	ctx := context.Background()
	for i := 0; i < int(cfg.RateLimit.Burst); i++ {
		wait, ok := l.Allow(ctx, "tool:ls")
		if !ok || wait != 0 {
			t.Fatalf("call %d/%d: got (%v, %v), want (0, true)", i+1, int(cfg.RateLimit.Burst), wait, ok)
		}
	}
}

// TestRateLimiterThrottledAfterBurst pins the exit bar (§13.9.2): once the
// bucket is drained, the next call is THROTTLED — it returns a nonzero wait +
// ok=false — not errored. The caller sleeps `wait` and retries; the limiter
// never panics or returns an error on overload.
func TestRateLimiterThrottledAfterBurst(t *testing.T) {
	cfg := testConfig()
	l := newRateLimiter(cfg)
	ctx := context.Background()
	for i := 0; i < int(cfg.RateLimit.Burst); i++ {
		l.Allow(ctx, "tool:ls")
	}
	wait, ok := l.Allow(ctx, "tool:ls")
	if ok {
		t.Fatalf("post-burst call: got ok=true, want false (throttled, not errored)")
	}
	if wait <= 0 {
		t.Fatalf("post-burst call: wait=%v, want > 0 (caller needs a backoff duration)", wait)
	}
}

// TestRateLimiterRefillsOverTime pins §13.9.2 refill: a drained bucket
// regenerates tokens at `rate`/sec, so after a short sleep the next call is
// allowed again. A high Rate keeps the sleep short + the test stable across
// the 3× stability run. This also guards the "disable refill" mutation:
// without refill the post-sleep call stays throttled.
func TestRateLimiterRefillsOverTime(t *testing.T) {
	cfg := testConfig()
	cfg.RateLimit.Rate = 50 // tokens/sec — 100ms ≈ 5 tokens, well past the 1 needed
	cfg.RateLimit.Burst = 4
	l := newRateLimiter(cfg)
	ctx := context.Background()
	for i := 0; i < int(cfg.RateLimit.Burst); i++ {
		l.Allow(ctx, "tool:ls")
	}
	// Immediate next call is throttled (the bucket is empty, ~0 elapsed).
	if wait, ok := l.Allow(ctx, "tool:ls"); ok || wait <= 0 {
		t.Fatalf("pre-sleep call: got (%v, %v), want (wait>0, false)", wait, ok)
	}
	time.Sleep(100 * time.Millisecond)
	// After the sleep the bucket has refilled ≥1 token → allowed, no wait.
	if wait, ok := l.Allow(ctx, "tool:ls"); !ok || wait != 0 {
		t.Fatalf("post-sleep call: got (%v, %v), want (0, true) — refill should restore a token", wait, ok)
	}
}

// TestRateLimiterCanceledContextReturnsFalseWithoutConsume pins §13.9.2: a
// canceled context short-circuits to (0, false) WITHOUT seeding or consuming
// the bucket — so a later live call on the same key still sees the full
// first-call burst. This guards the "disable ctx-cancel check" mutation.
func TestRateLimiterCanceledContextReturnsFalseWithoutConsume(t *testing.T) {
	cfg := testConfig()
	l := newRateLimiter(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	wait, ok := l.Allow(ctx, "tool:rm")
	if ok || wait != 0 {
		t.Fatalf("canceled-ctx call: got (%v, %v), want (0, false)", wait, ok)
	}
	// The canceled call must not have seeded or consumed the bucket: a live call
	// on the same key returns (0, true) — the full first-call burst.
	if wait, ok := l.Allow(context.Background(), "tool:rm"); !ok || wait != 0 {
		t.Fatalf("live call after canceled: got (%v, %v), want (0, true) — cancel must not consume", wait, ok)
	}
}

// TestRateLimiterIndependentKeys pins §13.9.2: bucket keys are isolated per
// §13.9.2's key scheme (llm:<provider> / tool:<name> / mcp:<server>).
// Draining llm:openai's bucket leaves tool:ls's bucket full — the two keys
// never share a bucket, so one provider's burst exhaustion can't starve a
// tool's calls.
func TestRateLimiterIndependentKeys(t *testing.T) {
	cfg := testConfig()
	l := newRateLimiter(cfg)
	ctx := context.Background()
	for i := 0; i < int(cfg.RateLimit.Burst); i++ {
		l.Allow(ctx, "llm:openai")
	}
	// llm:openai is now drained → throttled.
	if wait, ok := l.Allow(ctx, "llm:openai"); ok {
		t.Fatalf("drained llm:openai: got ok=true, want false (throttled)")
	} else if wait <= 0 {
		t.Fatalf("drained llm:openai: wait=%v, want > 0", wait)
	}
	// tool:ls is untouched → first call seeds + consumes from a full bucket.
	if wait, ok := l.Allow(ctx, "tool:ls"); !ok || wait != 0 {
		t.Fatalf("independent tool:ls: got (%v, %v), want (0, true)", wait, ok)
	}
}
