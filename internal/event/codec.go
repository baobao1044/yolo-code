// Package event — on-disk codec (L3-004 durability, expanded by L3-006).
//
// Envelopes are written as one JSON object per line ("JSON Lines"). Each line
// is a versioned wireEnvelope: the bus-assigned Seq and At, the event's Topic
// (a type tag), and the event payload as raw JSON. Replay uses a topic→factory
// registry to reconstruct the concrete event before unmarshaling the payload
// into it.
//
// Versioning (File 05 §5.4.10): every envelope carries "v":1. Within v1,
// additive optional fields are allowed; removing or renaming a field is a
// breaking change that requires a log-reader migration. The full 16-topic
// catalog is populated in L3-006; today only the topics tests register are
// known, which is all the durability path needs.

package event

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Version is the on-disk envelope schema version.
const Version = 1

var (
	regMu sync.RWMutex
	reg   = map[Topic]func() Event{}
)

// Register associates a Topic with a factory returning a fresh zero Event of
// its concrete type. The durability log calls the factory on replay to build a
// value to unmarshal the payload into. L3-006 registers every catalog topic.
func Register(typ Topic, factory func() Event) {
	regMu.Lock()
	reg[typ] = factory
	regMu.Unlock()
}

func factoryFor(typ Topic) (func() Event, bool) {
	regMu.RLock()
	f, ok := reg[typ]
	regMu.RUnlock()
	return f, ok
}

// wireEnvelope is the on-disk form of Envelope.
type wireEnvelope struct {
	V    int             `json:"v"`
	Seq  uint64          `json:"seq"`
	At   time.Time       `json:"at"`
	Type Topic           `json:"type"`
	Evt  json.RawMessage `json:"evt"`
}

// MarshalJSON renders an Envelope as a versioned wireEnvelope. The event's
// concrete type must be JSON-marshalable.
func (e Envelope) MarshalJSON() ([]byte, error) {
	payload, err := json.Marshal(e.Evt)
	if err != nil {
		return nil, fmt.Errorf("event: marshal payload of %q: %w", e.Evt.Type(), err)
	}
	return json.Marshal(wireEnvelope{
		V:    Version,
		Seq:  e.Seq,
		At:   e.At,
		Type: e.Evt.Type(),
		Evt:  payload,
	})
}

// UnmarshalJSON rebuilds an Envelope, looking up the factory for its Type and
// unmarshaling the payload into a fresh event of that type.
func (e *Envelope) UnmarshalJSON(data []byte) error {
	var w wireEnvelope
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	factory, ok := factoryFor(w.Type)
	if !ok {
		return fmt.Errorf("event: no factory registered for topic %q", w.Type)
	}
	evt := factory()
	if len(w.Evt) > 0 {
		if err := json.Unmarshal(w.Evt, evt); err != nil {
			return fmt.Errorf("event: unmarshal payload of %q: %w", w.Type, err)
		}
	}
	e.Seq = w.Seq
	e.At = w.At
	e.Evt = evt
	return nil
}
