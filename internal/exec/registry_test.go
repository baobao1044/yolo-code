// Tests for the tool registry (File 08 §8.1.5): static Go registration by
// name, metadata lookup, schema enumeration, and per-call args validation
// against the tool's declared schema. One test per case, package-local
// helpers — the conventions established by the cognitive/context suites.

package exec

import (
	"context"
	"strings"
	"testing"

	"github.com/yolo-code/yolo/internal/event"
)

// fakeTool is a minimal Tool the registry tests assemble inline to exercise
// multi-tool enumeration without a second built-in. It carries a name and a
// schema; the other Tool methods are inert. Echo (the real built-in) is used
// wherever the test wants Run/Metadata to mean something.
func fakeTool(name string, sch Schema) Tool {
	return &stubTool{name: name, sch: sch}
}

type stubTool struct {
	name string
	sch  Schema
}

func (t *stubTool) Name() string               { return t.name }
func (t *stubTool) Schema() Schema             { return t.sch }
func (t *stubTool) Metadata() Metadata         { return Metadata{Category: "demo"} }
func (t *stubTool) Risk(_ ToolCall) event.Risk { return RiskLow }
func (t *stubTool) Run(_ context.Context, _ ToolInput) (ToolOutput, error) {
	return ToolOutput{}, nil
}

func TestRegisterAndLookupTool(t *testing.T) {
	r := new(Registry)
	r.Register(NewEcho())

	got, ok := r.Get("echo")
	if !ok {
		t.Fatal("Get(echo): not found")
	}
	if got.Name() != "echo" {
		t.Fatalf("Name = %q, want %q", got.Name(), "echo")
	}
	if got.Metadata().Category == "" {
		t.Fatal("Metadata().Category is empty; registry must surface tool metadata")
	}
}

func TestDuplicateRegistrationPanics(t *testing.T) {
	r := new(Registry)
	r.Register(NewEcho())

	defer func() {
		if recover() == nil {
			t.Fatal("registering the same name twice should panic")
		}
	}()
	r.Register(NewEcho())
}

func TestRegistryNamesAndSchemas(t *testing.T) {
	r := new(Registry)
	r.Register(NewEcho())
	r.Register(fakeTool("other", Schema{Required: []string{"path"}}))

	names := r.Names()
	if len(names) != 2 {
		t.Fatalf("Names = %v (len %d), want 2", names, len(names))
	}
	if !contains(names, "echo") || !contains(names, "other") {
		t.Fatalf("Names = %v, want echo+other present", names)
	}
	schemas := r.Schemas()
	if len(schemas) != 2 {
		t.Fatalf("Schemas len = %d, want 2", len(schemas))
	}
}

// TestRegistryNamesInInsertionOrder locks S5: the <tools> block is built from
// Names(), so the order must be byte-stable across runs (Go map iteration is
// not). Six elements make the guard robust — if Names() iterated the map, the
// chance of matching insertion order is 1/720, so a map-iteration mutation
// fails this test reliably rather than passing by luck.
func TestRegistryNamesInInsertionOrder(t *testing.T) {
	r := new(Registry)
	want := []string{"echo", "tool1", "tool2", "tool3", "tool4", "tool5"}
	r.Register(NewEcho())
	for _, n := range want[1:] {
		r.Register(fakeTool(n, Schema{}))
	}
	got := r.Names()
	if len(got) != len(want) {
		t.Fatalf("Names len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Names[%d] = %q, want %q (got order %v)", i, got[i], want[i], got)
		}
	}
}

func TestValidateArgsRejectsMissingRequired(t *testing.T) {
	sch := Schema{Required: []string{"path"}}

	if err := validateArgs(sch, []byte(`{}`)); err == nil {
		t.Fatal(`validateArgs({}) with required [path] = nil, want error`)
	}
	if err := validateArgs(sch, []byte(`{"path":"x"}`)); err != nil {
		t.Fatalf(`validateArgs({"path":"x"}) = %v, want nil`, err)
	}
}

func TestEchoToolRuns(t *testing.T) {
	echo := NewEcho()
	out, err := echo.Run(context.Background(), ToolInput{Args: []byte(`{"msg":"hello"}`)})
	if err != nil {
		t.Fatalf("Echo.Run = %v, want nil", err)
	}
	if !strings.Contains(out.Stdout, "hello") {
		t.Fatalf("Echo.Run stdout = %q, want it to contain the echoed msg", out.Stdout)
	}
}

// contains reports whether ss holds s. A local strings.Contains stand-in so
// the test file does not pull in sort just for membership checks.
func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// errContains checks the error's message contains want; nil error fails.
func errContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %q, want substring %q", err.Error(), want)
	}
}
