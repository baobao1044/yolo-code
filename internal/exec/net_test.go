// Tests for the network policy (File 08 §8.4.4): default-deny — no tool may
// reach the network unless its host is on the sandbox's allowlist, and only
// tools whose metadata declares Permission.Net:true may even attempt it. The
// real per-process isolation (network namespace / firewall rule) is platform
// infra wired later; this ticket enforces the policy gate that decides
// whether a tool's network attempt is permitted before Run is called.

package exec

import (
	"context"
	"strings"
	"testing"

	"github.com/baobao1044/yolo-code/internal/event"
)

// netTool is a fake tool that declares Net:true and records whether its Run
// was reached. It never actually connects (the policy must block before Run
// for the deny case; the allow case just asserts Run was reached). Risk is low
// so the network gate — not the HITL gate (L7-006) — is what the tests
// exercise; the two gates are independent concerns.
type netTool struct {
	name string
	host string
	ran  bool
}

func (t *netTool) Name() string               { return t.name }
func (t *netTool) Metadata() Metadata         { return Metadata{Permission: Permission{Net: true}} }
func (t *netTool) Schema() Schema             { return Schema{Type: "object"} }
func (t *netTool) Risk(_ ToolCall) event.Risk { return RiskLow }
func (t *netTool) Run(_ context.Context, _ ToolInput) (ToolOutput, error) {
	t.ran = true
	return ToolOutput{Stdout: "connected to " + t.host}, nil
}

// newNetEngine wires an Engine with a Net tool registered and a sandbox whose
// hosts come from the test. The sandbox is the engine's net gate.
func newNetEngine(t *testing.T, hosts map[string]bool, tools ...Tool) (*Engine, *netTool) {
	t.Helper()
	root := t.TempDir()
	s := &Sandbox{root: root, cwd: root, hosts: hosts}
	nt := &netTool{name: "fetch", host: "evil.example:443"}
	r := new(Registry)
	for _, tl := range tools {
		r.Register(tl)
	}
	r.Register(nt)
	bus := event.New()
	t.Cleanup(func() { _ = bus.Close() })
	return New(Deps{Registry: r, Bus: bus, Sandbox: s}), nt
}

func TestNetworkDeniedByDefault(t *testing.T) {
	// No host allowlisted → a Net tool must be blocked before Run.
	eng, nt := newNetEngine(t, nil)

	_, err := eng.Dispatch(context.Background(), ToolCall{
		Tool: "fetch",
		Args: []byte(`{"host":"evil.example:443"}`),
	})
	if err == nil {
		t.Fatal("Dispatch(fetch) = nil, want ErrNetworkDenied (default-deny, no allowlisted host)")
	}
	if err != ErrNetworkDenied && !strings.Contains(err.Error(), "network") {
		t.Fatalf("err = %q, want ErrNetworkDenied", err.Error())
	}
	if nt.ran {
		t.Fatal("net tool's Run was called after the network gate denied — the gate must run before Run")
	}
}

func TestNetworkAllowedWithOptIn(t *testing.T) {
	// Allowlist the host → the Net tool runs and connects.
	root := t.TempDir()
	s := &Sandbox{root: root, cwd: root, hosts: map[string]bool{"proxy.example.com:443": true}}
	nt := &netTool{name: "fetch", host: "proxy.example.com:443"}
	r := new(Registry)
	r.Register(nt)
	eng := New(Deps{Registry: r, Bus: event.New(), Sandbox: s})

	obs, err := eng.Dispatch(context.Background(), ToolCall{
		Tool: "fetch",
		Args: []byte(`{"host":"proxy.example.com:443"}`),
	})
	if err != nil {
		t.Fatalf("Dispatch(allowlisted host) = %v, want nil", err)
	}
	if !nt.ran {
		t.Fatal("net tool's Run was not called despite the host being allowlisted")
	}
	if !strings.Contains(obs.Stdout, "proxy.example.com:443") {
		t.Fatalf("obs stdout = %q, want the connected host", obs.Stdout)
	}
}

func TestNetworkDeniesUnlistedHostEvenWithAllowlist(t *testing.T) {
	// An allowlist exists but this host isn't on it → still denied.
	eng, nt := newNetEngine(t, map[string]bool{"good.example:443": true})

	_, err := eng.Dispatch(context.Background(), ToolCall{
		Tool: "fetch",
		Args: []byte(`{"host":"evil.example:443"}`),
	})
	if err == nil {
		t.Fatal("Dispatch(unlisted host) = nil, want ErrNetworkDenied (not on the allowlist)")
	}
	if nt.ran {
		t.Fatal("net tool ran for an unlisted host — the allowlist must be exact, not permissive")
	}
}

func TestNonNetToolNotGated(t *testing.T) {
	// A tool that does NOT declare Net:true is not subject to the network
	// gate — it runs regardless of the allowlist (a Bash tool's network is
	// gated at the command-class layer, L7-004).
	root := t.TempDir()
	s := &Sandbox{root: root, cwd: root} // no allowlist
	echo := NewEcho()
	r := new(Registry)
	r.Register(echo)
	eng := New(Deps{Registry: r, Sandbox: s})

	_, err := eng.Dispatch(context.Background(), ToolCall{Tool: "echo", Args: []byte(`{"msg":"hi"}`)})
	if err != nil {
		t.Fatalf("Dispatch(echo, no net declared) = %v, want nil (not network-gated)", err)
	}
}
