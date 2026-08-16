// Wiring test for the exec adapter's fail-closed gate. The gate itself lives in
// exec_adapter.go; this pins that the composition root actually arms it.

package main

import (
	"testing"

	"github.com/baobao1044/yolo-code/internal/event"
	"github.com/baobao1044/yolo-code/internal/exec"
	"github.com/baobao1044/yolo-code/internal/runtime"
)

// TestDefaultDepsGateAnUnroutedToolAtHighRisk pins the registry hand-off.
//
// execAdapter.unrouted reports false for every name while registry is nil, so
// the fail-closed classification is inert until a construction site passes the
// registry — the check can be present, correct and reviewed, and still let a
// tool the engine has never heard of through at RiskLow with no approval,
// because exec.Engine reports RiskLow on a registry miss. Asserting on the
// adapter built by defaultHeadlessDeps rather than on one the test constructs
// is the whole point: a hand-built adapter would pass with the production
// wiring still missing.
func TestDefaultDepsGateAnUnroutedToolAtHighRisk(t *testing.T) {
	t.Setenv("YOLO_REPO_ROOT", t.TempDir())
	t.Setenv("YOLO_MEMORY_DIR", t.TempDir())
	t.Setenv("YOLO_STUB", "1")

	bus := event.New()
	deps, err := defaultHeadlessDeps(bus)
	if err != nil {
		_ = bus.Close()
		t.Fatalf("defaultHeadlessDeps: %v", err)
	}
	// Bus first: the memory listener drains until it closes, and Store.Close
	// waits on that drain.
	t.Cleanup(func() {
		_ = bus.Close()
		if deps.memory != nil {
			_ = deps.memory.Close()
		}
		if deps.snap != nil {
			_ = deps.snap.close()
		}
	})

	ad, ok := deps.exec.(*execAdapter)
	if !ok {
		t.Fatalf("deps.exec is %T, want *execAdapter", deps.exec)
	}
	// Diagnostic, not fatal: the assertions below are the ones that matter, and
	// letting them run says what the missing wiring actually costs rather than
	// only that a field is nil.
	if ad.registry == nil {
		t.Error("the composition root built the adapter without the registry; " +
			"unrouted() reports false for every name and the gate never fires")
	}

	// A name no registered tool and no adapter route answers to. A scripted
	// model emitting one of these is not hypothetical here — that is how a
	// "say hello" turn ended up writing a file with no prompt.
	unknown := runtime.ToolCall{Tool: "exfiltrate", Args: []byte(`{"path":"/etc/passwd"}`)}
	if got := ad.RiskOf(unknown); got != exec.RiskHigh {
		t.Errorf("RiskOf(%q) = %v, want %v", unknown.Tool, got, exec.RiskHigh)
	}
	if !ad.NeedsApproval(unknown) {
		t.Errorf("NeedsApproval(%q) = false; an unroutable tool must reach a human", unknown.Tool)
	}

	// The control: arming the gate must not sweep up the registered read-only
	// tools, which would put a prompt in front of every file read.
	safe := runtime.ToolCall{Tool: "read_file", Args: []byte(`{"path":"x.go"}`)}
	if got := ad.RiskOf(safe); got == exec.RiskHigh {
		t.Errorf("RiskOf(read_file) = %v; a registered read-only tool must keep its own class", got)
	}
	if ad.NeedsApproval(safe) {
		t.Error("NeedsApproval(read_file) = true; the gate is over-firing")
	}
}
