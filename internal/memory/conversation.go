// Conversation memory — per-session, resumable (§11.3.2). The spec targets
// SQLite (modernc.org/sqlite, pure Go); the project is deliberately stdlib-only
// (the session layer set the precedent with a JSON-file store), so L10 uses a
// JSON-file backing: one file per session under root/conversations/<sid>.json,
// holding the session's ordered messages. A future SQLite swap reuses the
// Append/Persist/Messages surface — only the backing changes.
//
// Mutator discipline (§11.2): the public AppendAssistant mutator is called by
// the event listener (L10-002) reacting to assistant.message. L10-001 ships it
// package-public so the listener (in the same package) can call it; the "no
// direct writes from other layers" rule is enforced by memory being importable
// only by event/cmd-yolo — other layers can't reach this method.

package memory

import (
	"context"
	"path/filepath"
	"strconv"
	"sync"
)

// Conversation is the per-session message history (§11.3.2). Messages are
// append-ordered; the slice order IS the seq (no separate seq field, so the
// JSON file stays simple and the round-trip is byte-stable).
type Conversation struct {
	Messages []Message `json:"messages"`
}

// parts renders the conversation as Parts for the Context Engine. Each turn
// is "turn#<seq+1>" with role in Attr (the prompt compiler reads Attr["role"],
// File 06 §6.6.2).
func (c *Conversation) parts() []Part {
	if c == nil {
		return nil
	}
	out := make([]Part, 0, len(c.Messages))
	for i, m := range c.Messages {
		out = append(out, Part{
			Kind:   KindConversation,
			Source: "turn#" + strconv.Itoa(i+1),
			Text:   m.Text,
			Attr:   map[string]string{"role": string(m.Role)},
		})
	}
	return out
}

// History returns the raw messages (the runtime reads these to fork a turn).
func (c *Conversation) History() []Message {
	if c == nil {
		return nil
	}
	return c.Messages
}

// ConversationStore persists per-session conversations to JSON files. One file
// per session under root/conversations/. The in-memory map is a warm cache; a
// Load re-reads the file so two stores over the same dir see the same data
// (the cross-session-resume test relies on this).
type ConversationStore struct {
	root string
	mu   sync.Mutex
	sess map[string]*Conversation
}

// NewConversationStore returns a JSON-file conversation store rooted at dir.
func NewConversationStore(dir string) *ConversationStore {
	return &ConversationStore{root: dir, sess: make(map[string]*Conversation)}
}

func (s *ConversationStore) path(sid string) string {
	return filepath.Join(s.root, "conversations", sid+".json")
}

// AppendAssistant adds an assistant message to the session's conversation,
// creating the conversation lazily on first append (§11.4.2 append-then-persist).
func (s *ConversationStore) AppendAssistant(_ context.Context, sid string, m Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.sess[sid]
	if !ok {
		c = &Conversation{}
		s.sess[sid] = c
	}
	c.Messages = append(c.Messages, m)
}

// Persist writes the session's conversation to its JSON file.
func (s *ConversationStore) Persist(_ context.Context, sid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.sess[sid]
	if !ok {
		c = &Conversation{}
	}
	return writeJSON(s.path(sid), c)
}

// Load re-reads the session's conversation file into the warm cache.
func (s *ConversationStore) Load(_ context.Context, sid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var c Conversation
	if err := readJSON(s.path(sid), &c); err != nil {
		return err
	}
	s.sess[sid] = &c
	return nil
}

// Messages returns the session's messages from the warm cache.
func (s *ConversationStore) Messages(sid string) []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.sess[sid]
	if !ok {
		return nil
	}
	return append([]Message(nil), c.Messages...)
}
