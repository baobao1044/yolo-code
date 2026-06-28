// The tool registry (File 08 §8.1.5): static Go tools register at startup by
// name; the dispatcher looks them up per call, and the system prompt's
// <tools> block enumerates their schemas (File 06). A duplicate registration
// is a programmer error and panics — tools register once at init, so a clash
// means two tools fighting over a name that should surface loudly, not be
// silently shadowed.
//
// Names are tracked in insertion order (not by iterating the map) so the
// <tools> block is byte-stable across runs — S5 determinism. See
// TestRegistryNamesInInsertionOrder.

package exec

import (
	"encoding/json"
	"fmt"
)

// Registry is the name→tool table. Constructed via new(Registry); the
// zero-value map is lazy-initialized on first Register. Get/Names/Schemas
// serve the single-goroutine startup path the runtime drives.
type Registry struct {
	tools map[string]Tool
	order []string // insertion order, for deterministic enumeration
}

// Register adds t under its Name(), panicking on a duplicate (File 08
// §8.1.5). Names are appended to order so enumeration is insertion-stable.
func (r *Registry) Register(t Tool) {
	if r.tools == nil {
		r.tools = map[string]Tool{}
	}
	name := t.Name()
	if _, dup := r.tools[name]; dup {
		panic("exec: duplicate tool: " + name)
	}
	r.tools[name] = t
	r.order = append(r.order, name)
}

// Get returns the tool registered under name. The bool is false when no such
// tool exists; the dispatcher surfaces that as a normal tool error rather than
// a panic.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Names returns the registered tool names in insertion order (a copy, so the
// caller cannot mutate the registry's slice).
func (r *Registry) Names() []string {
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Schemas returns each tool's input schema in insertion order, paralleling
// Names() so the <tools> block pairs names and schemas deterministically.
func (r *Registry) Schemas() []Schema {
	out := make([]Schema, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.tools[n].Schema())
	}
	return out
}

// validateArgs checks args against sch (File 08 §8.3.2): args must be a JSON
// object and must contain every key in sch.Required. A missing key returns an
// error naming it; the dispatcher turns that into a tool result so the model
// learns which arg it forgot. Per-property type checks are deferred to a
// later ticket (see Schema).
func validateArgs(sch Schema, args []byte) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(args, &obj); err != nil {
		return fmt.Errorf("invalid args json: %w", err)
	}
	for _, key := range sch.Required {
		if _, ok := obj[key]; !ok {
			return fmt.Errorf("missing required arg %q", key)
		}
	}
	return nil
}
