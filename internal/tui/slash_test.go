// Tests for Phase 4 slash commands. handleSlashCommand routes "/foo args"
// lines: local commands (/help, /clear, /theme) mutate the model directly;
// runtime commands (/model, /provider, /status) publish a UserCommandEvent.
// Regular text (no slash) is a normal user.submit.

package tui

import (
	"strings"
	"testing"

	"github.com/baobao1044/yolo-code/internal/event"
)

// TestSlashHelpToggles pins /help toggles the help overlay locally (no event).
func TestSlashHelpToggles(t *testing.T) {
	m := newModelForTest()
	m, cmd := handleSlashCommand(m, "/help")
	runCmd(t, cmd) // should be nil
	if !m.showHelp {
		t.Error("/help did not toggle showHelp = true")
	}
	m, cmd = handleSlashCommand(m, "/help")
	runCmd(t, cmd)
	if m.showHelp {
		t.Error("/help did not toggle showHelp = false on second call")
	}
}

// TestSlashClearClears pins /clear wipes messages + resets scroll.
func TestSlashClearClears(t *testing.T) {
	m := newModelForTest()
	m.messages = append(m.messages, messageView{role: "user", text: "hello"})
	m.messages = append(m.messages, messageView{role: "assistant", text: "hi"})
	m.scrollOffset = 30
	m, cmd := handleSlashCommand(m, "/clear")
	runCmd(t, cmd)
	if len(m.messages) != 0 {
		t.Errorf("/clear left %d messages, want 0", len(m.messages))
	}
	if m.scrollOffset != 0 {
		t.Errorf("/clear left scrollOffset = %d, want 0", m.scrollOffset)
	}
}

// TestSlashThemeSwaps pins /theme <name> swaps the palette + echoes confirmation.
func TestSlashThemeSwaps(t *testing.T) {
	withEnv(t, map[string]string{"YOLO_THEME": "dark"}, func() {
		theme = loadTheme() // ensure starting state
		m := newModelForTest()
		m, cmd := handleSlashCommand(m, "/theme contrast")
		runCmd(t, cmd)
		if currentThemeName() != "contrast" {
			t.Errorf("currentThemeName() = %q, want contrast", currentThemeName())
		}
		if len(m.messages) == 0 || !strings.Contains(m.messages[len(m.messages)-1].text, "contrast") {
			t.Errorf("/theme did not echo confirmation: %v", m.messages)
		}
	})
}

// TestSlashThemeInvalid pins /theme <bad> echoes an error, leaves env unchanged.
func TestSlashThemeInvalid(t *testing.T) {
	withEnv(t, map[string]string{"YOLO_THEME": "dark"}, func() {
		theme = loadTheme()
		m := newModelForTest()
		m, cmd := handleSlashCommand(m, "/theme neon")
		runCmd(t, cmd)
		if currentThemeName() != "dark" {
			t.Errorf("theme changed to %q on invalid name, want dark", currentThemeName())
		}
		if len(m.messages) == 0 || !strings.Contains(m.messages[len(m.messages)-1].text, "unknown theme") {
			t.Errorf("/theme invalid did not echo error: %v", m.messages)
		}
	})
}

// TestSlashModelPublishesCommand pins /model <name> publishes a UserCommandEvent.
func TestSlashModelPublishesCommand(t *testing.T) {
	pub := &fakePublisher{}
	m := newModelForTest()
	m.publisher = pub
	m, cmd := handleSlashCommand(m, "/model gpt-4o-mini")
	runCmd(t, cmd)
	if pub.count != 1 {
		t.Fatalf("published %d events, want 1 (UserCommandEvent)", pub.count)
	}
	uc, ok := pub.last.(*event.UserCommandEvent)
	if !ok {
		t.Fatalf("published %T, want *UserCommandEvent", pub.last)
	}
	if uc.Command != "model" {
		t.Errorf("Command = %q, want model", uc.Command)
	}
	if uc.Args != "gpt-4o-mini" {
		t.Errorf("Args = %q, want gpt-4o-mini", uc.Args)
	}
}

// TestSlashProviderPublishesCommand pins /provider <name> publishes UserCommandEvent.
func TestSlashProviderPublishesCommand(t *testing.T) {
	pub := &fakePublisher{}
	m := newModelForTest()
	m.publisher = pub
	m, cmd := handleSlashCommand(m, "/provider groq")
	runCmd(t, cmd)
	if pub.count != 1 {
		t.Fatalf("published %d events, want 1", pub.count)
	}
	uc, ok := pub.last.(*event.UserCommandEvent)
	if !ok {
		t.Fatalf("published %T, want *UserCommandEvent", pub.last)
	}
	if uc.Command != "provider" {
		t.Errorf("Command = %q, want provider", uc.Command)
	}
	if uc.Args != "groq" {
		t.Errorf("Args = %q, want groq", uc.Args)
	}
}

// TestSlashStatusPublishesCommand pins /status publishes UserCommandEvent.
func TestSlashStatusPublishesCommand(t *testing.T) {
	pub := &fakePublisher{}
	m := newModelForTest()
	m.publisher = pub
	m, cmd := handleSlashCommand(m, "/status")
	runCmd(t, cmd)
	if pub.count != 1 {
		t.Fatalf("published %d events, want 1", pub.count)
	}
	uc, ok := pub.last.(*event.UserCommandEvent)
	if !ok {
		t.Fatalf("published %T, want *UserCommandEvent", pub.last)
	}
	if uc.Command != "status" {
		t.Errorf("Command = %q, want status", uc.Command)
	}
}

// TestSlashPrefPublishesCommand pins /pref <key> <value> as a runtime command.
//
// The negative half is the point. Before this, "pref" was not in the runtime
// case list, so it fell to the default arm and was echoed back as "unknown
// command: /pref" — locally, with nothing published. A test that only checked
// the happy path would pass just as well against a routing table that had lost
// the entry to a merge, because the assertions would then never run: pub.last
// would be nil and the type assert would Fatal with a message about the wrong
// event type rather than about the routing. So this asserts the count first and
// says what a zero means.
func TestSlashPrefPublishesCommand(t *testing.T) {
	pub := &fakePublisher{}
	m := newModelForTest()
	m.publisher = pub
	m, cmd := handleSlashCommand(m, "/pref style I prefer table-driven tests")
	runCmd(t, cmd)

	if pub.count != 1 {
		t.Fatalf("published %d events, want 1 — /pref fell through to the unknown-command "+
			"arm and was answered locally instead of reaching the driver; messages=%v",
			pub.count, m.messages)
	}
	uc, ok := pub.last.(*event.UserCommandEvent)
	if !ok {
		t.Fatalf("published %T, want *UserCommandEvent", pub.last)
	}
	if uc.Command != "pref" {
		t.Errorf("Command = %q, want pref", uc.Command)
	}
	// The whole remainder is the value, spaces and all. Cutting at the first
	// space would leave "I" as the preference and drop the sentence — and a
	// preference is prose far more often than it is a single token.
	if uc.Args != "style I prefer table-driven tests" {
		t.Errorf("Args = %q, want the key plus the full remaining line", uc.Args)
	}
}

// TestSlashPrefWithNoArgsStillReachesTheDriver pins the listing form. A bare
// /pref must not be treated as a malformed command and swallowed locally: the
// preferences live in the memory store, which only the driver can read, so the
// TUI has nothing to answer with and every form has to be forwarded.
func TestSlashPrefWithNoArgsStillReachesTheDriver(t *testing.T) {
	pub := &fakePublisher{}
	m := newModelForTest()
	m.publisher = pub
	m, cmd := handleSlashCommand(m, "/pref")
	runCmd(t, cmd)

	if pub.count != 1 {
		t.Fatalf("bare /pref published %d events, want 1; messages=%v", pub.count, m.messages)
	}
	uc, ok := pub.last.(*event.UserCommandEvent)
	if !ok {
		t.Fatalf("published %T, want *UserCommandEvent", pub.last)
	}
	if uc.Command != "pref" || uc.Args != "" {
		t.Errorf("got Command=%q Args=%q, want pref with empty args", uc.Command, uc.Args)
	}
}

// TestSlashUnknownEchoesError pins /foo (unknown) echoes an error locally.
func TestSlashUnknownEchoesError(t *testing.T) {
	pub := &fakePublisher{}
	m := newModelForTest()
	m.publisher = pub
	m, cmd := handleSlashCommand(m, "/foo")
	runCmd(t, cmd)
	if pub.count != 0 {
		t.Errorf("unknown command published %d events, want 0 (local echo only)", pub.count)
	}
	if len(m.messages) == 0 || !strings.Contains(m.messages[len(m.messages)-1].text, "unknown command") {
		t.Errorf("/foo did not echo unknown-command error: %v", m.messages)
	}
}

// TestSlashNoPrefixIsSubmit pins: text without "/" prefix is a normal submit.
func TestSlashNoPrefixIsSubmit(t *testing.T) {
	pub := &fakePublisher{}
	m := newModelForTest()
	m.publisher = pub
	m, cmd := handleSlashCommand(m, "write a function")
	runCmd(t, cmd)
	if pub.count != 1 {
		t.Fatalf("published %d events, want 1 (UserSubmitEvent)", pub.count)
	}
	if _, ok := pub.last.(*event.UserSubmitEvent); !ok {
		t.Fatalf("published %T, want *UserSubmitEvent", pub.last)
	}
}

// TestSlashThemeNoArgListsThemes pins /theme (no arg) echoes the theme list + current.
func TestSlashThemeNoArgListsThemes(t *testing.T) {
	withEnv(t, nil, func() {
		m := newModelForTest()
		m, cmd := handleSlashCommand(m, "/theme")
		runCmd(t, cmd)
		if len(m.messages) == 0 {
			t.Fatal("/theme (no arg) did not echo a message")
		}
		last := m.messages[len(m.messages)-1]
		if !strings.Contains(last.text, "dark") || !strings.Contains(last.text, "light") {
			t.Errorf("/theme (no arg) message %q missing theme list", last.text)
		}
		if !strings.Contains(last.text, "current:") {
			t.Errorf("/theme (no arg) message %q missing 'current:'", last.text)
		}
	})
}
