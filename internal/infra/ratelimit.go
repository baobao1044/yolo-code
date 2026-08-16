// L12-007 — Rate limiter: per-key token buckets (File 13 §13.9).
//
// The limiter is one of the four non-event-driven infra concerns (with Secrets,
// Permissions, Cost): the exec/llm/mcp layers call it directly through the
// composition root (L12-009), it does not subscribe to the event bus. Each key
// owns a token bucket seeded with `burst` on first use; Allow consumes one token
// and refills at `rate` tokens/sec.
//
// §13.9.2 fixes the bucket keys: llm:<provider>, tool:<name>, mcp:<server>.
// Each is isolated — one provider's burst exhaustion can't starve a tool's
// calls. The exit bar (§13.9.2) is "throttled, not errored": an empty bucket
// returns a nonzero wait + ok=false (the caller sleeps `wait` and retries); a
// canceled context short-circuits to (0, false) and touches no bucket state.

package infra

import (
	"context"
	"sync"
	"time"
)

// bucket is a single token bucket (§13.9.2). tokens is a float so partial refill
// accumulates between calls; last is the wall-clock of the previous Allow so the
// elapsed-since-last delta drives the refill. rate/burst are copied from the
// limiter's config at seed time so a later config change can't race a live bucket.
type bucket struct {
	tokens float64
	last   time.Time
	rate   float64 // tokens/sec
	burst  float64
}

// RateLimiter holds one token bucket per key (§13.9.2 key scheme). The mutex
// guards the buckets map; each bucket is mutated only while the lock is held
// inside Allow, so callers never see a half-refilled bucket.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64 // default rate for newly seeded buckets
	burst   float64 // default burst for newly seeded buckets
}

// newRateLimiter builds a RateLimiter from the rate/burst in cfg. Buckets are
// created lazily on first Allow for a key (seeded with `burst`), so an unused
// key costs nothing. A non-positive rate or burst disables the limiter (see
// Allow) rather than closing it.
func newRateLimiter(cfg Config) *RateLimiter {
	return &RateLimiter{
		buckets: make(map[string]*bucket),
		rate:    cfg.RateLimit.Rate,
		burst:   cfg.RateLimit.Burst,
	}
}

// Allow attempts to consume one token from key's bucket. It returns (0, true)
// when a token was consumed; (wait, false) when the bucket is empty — `wait` is
// how long the caller should sleep before retrying; (0, false) when ctx is
// already canceled, without seeding or consuming the bucket. Allow never blocks
// and never errors: throttling is signaled via ok=false, not a returned error.
func (l *RateLimiter) Allow(ctx context.Context, key string) (time.Duration, bool) {
	// Canceled context short-circuits before touching any bucket: no seed, no
	// consume, no refill. A later live call on the same key still gets the full
	// first-call burst (§13.9.2 — cancel must not "burn" a token).
	if err := ctx.Err(); err != nil {
		return 0, false
	}
	// An unconfigured limiter is OFF, not shut. A zero rate/burst is what
	// infra.Config{} produces — the zero Config startWithLog documents as
	// supported — and reading it as "a limit of zero" inverted the seam: the
	// bucket seeded empty, could never refill, and every Allow for the life of
	// the process returned ok=false. The wait it handed back was worse than
	// useless: (1-0)/0 is +Inf, and converting that to a Duration yields
	// -2562047h47m16s, so a caller obeying the documented "sleep `wait` and
	// retry" contract slept a negative duration and hot-spun forever on a
	// throttle nobody asked for. An unset cap means no cap (the same reading
	// cost_publisher.go's `maxCost > 0` guard already uses); a negative one is
	// nonsense config and gets the same treatment rather than a nonsense wait.
	if l.rate <= 0 || l.burst <= 0 {
		return 0, true
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	b, ok := l.buckets[key]
	if !ok {
		// First call for this key: seed with the full burst so the initial burst
		// of calls is admitted immediately (§13.9.2).
		b = &bucket{tokens: l.burst, last: now, rate: l.rate, burst: l.burst}
		l.buckets[key] = b
	} else {
		// Refill: add rate*elapsed tokens, capped at burst. Elapsed is only
		// positive when real wall-clock has advanced (a tight loop adds ~0).
		if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
			b.tokens = min(b.burst, b.tokens+elapsed*b.rate)
			b.last = now
		}
	}

	if b.tokens >= 1 {
		b.tokens--
		return 0, true
	}
	// Empty: tell the caller how long until one token regenerates. (1-tokens)/rate
	// seconds — at rate=2/sec an empty bucket waits ~500ms; the high-rate test
	// config waits only a few ms.
	wait := time.Duration((1 - b.tokens) / b.rate * float64(time.Second))
	return wait, false
}
