// Package infra config (File 13 §13.11.1). Config wires every cross-cutting
// concern; Start reads it to construct the Infra aggregate. Sub-configs carry
// their own defaults (DefaultConfig fills them) so a caller can build a
// test-only Infra with zero values — the stdlib-only stubs (Sprint 7 zero-deps
// precedent) need no endpoint or DSN.
//
// L12-001 ships the minimal Config newTelemetry needs (Version, HostID, OTel);
// later tickets add Log, Sentry, Permissions, RateLimit, Cost sub-configs as
// they land. DefaultConfig returns the §13.11.1 dev defaults: OTel off (a
// no-op stub tracer — no collector), text logs at INFO, no Sentry, auto
// permissions, default rate limits, generous cost budget.

package infra

import (
	"time"
)

// Config bundles every cross-cutting concern's configuration (File 13 §13.11.1).
type Config struct {
	Version     string
	HostID      string
	OTel        OTelConfig
	Log         LogConfig
	Sentry      SentryConfig
	Permissions PermissionsConfig
	RateLimit   RateLimitConfig
	Cost        CostConfig
}

// OTelConfig configures the trace + metrics exporters. With the stdlib stub
// (Sprint 8), Endpoint is unused — the stub records in-memory. The fields stay
// so the real SDK swaps in behind newTelemetry/newMetrics without a Config
// schema change.
type OTelConfig struct {
	Endpoint       string
	Insecure       bool
	SampleRate     float64
	QueueSize      int
	BatchSize      int
	BatchTimeout   time.Duration
	MetricInterval time.Duration
}

// LogConfig configures the slog logger (File 13 §13.5).
type LogConfig struct {
	Format string // "text" | "json"
	Level  int    // slog.Level value (int alias; L12-003 wires slog.Level)
}

// SentryConfig configures the Sentry hub (File 13 §13.6). Empty DSN → nil hub
// (opt-out). With the stdlib stub, DSN still gates the hub but the "init" is a
// no-op recording sink (no network).
type SentryConfig struct {
	DSN         string
	Environment string
	SampleRate  float64
}

// PermissionsConfig configures the permissions model (File 13 §13.8).
type PermissionsConfig struct {
	Mode  string // "yolo" | "auto" | "ask" | "read-only"
	Rules []policyRuleConfig
}

// policyRuleConfig is the wire form of a policy rule (loaded from config); L12-006
// translates it into a policyRule.
type policyRuleConfig struct {
	Actions []string
	Pattern string
	Verdict string
	Reason  string
}

// RateLimitConfig configures the rate limiter (File 13 §13.9).
type RateLimitConfig struct {
	Rate  float64 // tokens/sec per bucket
	Burst float64
}

// CostConfig configures the cost ledger (File 13 §13.10). The stdlib stub
// delegates to cognitive.Cost (Sprint 3); MaxDollars/MaxLoops/MaxTokens/
// Deadline are the per-task caps the controller already enforces.
type CostConfig struct {
	MaxDollars float64
	MaxLoops   int
	MaxTokens  int
	Deadline   time.Duration
}

// DefaultConfig returns the §13.11.1 dev defaults: OTel off (stub), text logs
// at INFO, no Sentry, auto permissions, a 2 req/s / burst 10 rate limit, and a
// generous cost budget. Version/HostID default to placeholder strings — the
// composition root (cmd/yolo) overrides them from build info / hostname.
func DefaultConfig() Config {
	return Config{
		Version: "dev",
		HostID:  "local",
		OTel: OTelConfig{
			SampleRate:     1.0,
			QueueSize:      2048,
			BatchSize:      512,
			BatchTimeout:   5 * time.Second,
			MetricInterval: 30 * time.Second,
		},
		Log:         LogConfig{Format: "text", Level: 20}, // 20 = slog.LevelInfo
		Sentry:      SentryConfig{SampleRate: 1.0},
		Permissions: PermissionsConfig{Mode: "auto"},
		RateLimit:   RateLimitConfig{Rate: 2.0, Burst: 10.0},
		Cost: CostConfig{
			MaxDollars: 1.0,
			MaxLoops:   6,
			Deadline:   10 * time.Minute,
		},
	}
}

// testConfig returns DefaultConfig for the package-internal tests (so a test
// builds an Infra concern without re-stating the defaults). L12-009 may override
// fields per-test.
func testConfig() Config { return DefaultConfig() }
