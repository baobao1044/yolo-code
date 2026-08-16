# TUI Guide

The yolo-code TUI is built on [bubbletea](https://github.com/charmbracelet/bubbletea) + [lipgloss](https://github.com/charmbracelet/lipgloss) + [bubbles](https://github.com/charmbracelet/bubbles).

## Launch

```bash
yolo
```

The TUI takes over the entire terminal (alt-screen). Type a goal at the prompt and press Enter to start a task.

## Layout

```
┌───────────────────────────────────────────────────────┐
│ Header — task id · goal · state · spinner/icon        │
├───────────────────────────────────────────────────────┤
│                                    │                  │
│  Chat pane                         │  Rail            │
│  (messages, thinking, tools,       │  (approval,      │
│   reflections, verification)       │   diff viewer,   │
│                                    │   cost meter,     │
│                                    │   board)         │
│                                    │                  │
├───────────────────────────────────────────────────────┤
│ Input prompt — type a goal, press Enter                │
├───────────────────────────────────────────────────────┤
│ Status line — focus tags · key hints · scroll offset   │
└───────────────────────────────────────────────────────┘
```

On narrow terminals (< ~96 columns) the rail stacks below the chat pane instead of beside it.

### Header

Shows the task id, goal, current FSM state (with a spinner while streaming or a tool is active, ✔ on DONE, ✘ on CANCELLED), and the transition reason (dimmed) when available.

### Chat pane

Streams in real time: user echoes, assistant messages, thinking blocks, active tool names, observations, reflections, and verification stage results. Scroll up with PgUp, back down with PgDn.

Messages use compact glyphs and a Codex-style color palette:

| Role | Glyph | Color |
|---|---|---|
| user | (none, bold) | default bold |
| assistant | `│` | magenta |
| tool | `▸` | dim |
| observation | `←` | dim |
| reflection | `⟳` | amber |
| error | `✗` | red |
| system | `·` | dim |

### Rail

The side panel switches between:
- **Approval** — tool name, summary, risk level, preview, and the `y`/`n` prompt.
- **Diff viewer** — changed files with `+N -N` counts, and real unified-diff hunks (Phase D: `+` lines green, `-` lines red, context dimmed, with line numbers).
- **Cost meter** — accumulated dollars (`cost.incurred` per-tool-call) + rough token estimate (`~N tok`, from `llm.token` deltas), plus the degradation level and abort reason.
- **Board** — the multi-agent plan's todos with status tags: `[~]` in progress, `[+]` done, `[!]` rework/failed (color-blind text fallback).

### Input prompt

A [bubbles/textinput](https://pkg.go.dev/github.com/charmbracelet/bubbles/textinput) widget (Phase B): real blinking cursor, ←/→/Home/End/Ctrl-A/Ctrl-E cursor movement, Ctrl-W word delete, and full UTF-8 support (non-ASCII is no longer dropped).

## Themes & accessibility

### Theme selection

`YOLO_THEME` selects the palette (default: `dark`):

| Theme | Env | Use |
|---|---|---|
| Dark (default) | `YOLO_THEME=dark` | Cyan/blue/gray on dark backgrounds |
| Light | `YOLO_THEME=light` | Dark text on light backgrounds (adaptive colors) |
| Contrast | `YOLO_THEME=contrast` | Bold + reverse-video focus for low-vision |
| Mono | `YOLO_THEME=mono` | No color, bold-only distinctions |

`NO_COLOR` (any non-empty value, per the [NO_COLOR spec](https://no-color.org/)) forces mono regardless of `YOLO_THEME`.

### Reduced motion

`YOLO_NO_MOTION` (any non-empty value) disables the spinner animation and cursor blink — a steady `●` and a static cursor instead.

### Color-blind status tags

The board uses glyph + text tags (`[~]` in progress, `[+]` done, `[!]` rework) so status is readable without relying on color alone.

## Key bindings

| Key | Action | Notes |
|---|---|---|
| **Enter** | Submit goal / message | Echoes optimistically, publishes `user.submit` |
| **Esc** | Cancel current task | No-op if no active task |
| **Ctrl+P** | Pause task | |
| **Ctrl+R** | Resume paused task | |
| **Ctrl+C** | Quit | Publishes `user.quit` and exits. The one unconditional binding |
| **y / n** | Approve / reject | Only when an approval is pending — otherwise plain text |
| **Tab** | Cycle focus: chat → diff → board | Skips empty panes |
| **PgUp / PgDn** | Scroll chat up / down | |
| **?** | Toggle help overlay | Only when the input isn't capturing text — use `/help` otherwise |
| **←/→/Home/End** | Move cursor in input | (textinput widget) |
| **Ctrl-A / Ctrl-E** | Cursor to start / end | |
| **Ctrl-W** | Delete word backward | |
| **Backspace** | Delete character | |

### Printable keys are text while you type

`q`, `?`, `y` and `n` are ordinary characters whenever the input line owns the keyboard — which is the whole session, except while an approval is pending. Typing `query` types `query`; it does not quit on the `q`. Those four act as commands only when the input is not capturing text.

**Ctrl+C** is the one binding that always quits, whatever owns the keyboard — typing, help overlay, or a pending approval. **`/help`** is the always-available way to reach the help overlay, since `?` is a character while you type.

The help overlay is modal: once open, any key closes it.

### Approval is non-trapping (Phase B)

When an approval is pending, `y`/`n` approve/reject — but **scroll, help, quit, and cancel still work**. Only typing into the input line is suppressed until you answer.

> **You will not currently see this prompt.** The TUI reuses the headless
> composition root, which hardcodes `AutoApprove{RiskMedium: true, RiskHigh: true}`
> — so medium- and high-risk tools run without asking. Critical-risk tools are
> denied outright and also never prompt. The approval UI and the gate behind it
> are both implemented; nothing currently triggers them. See
> [Configuration → HITL Approval](configuration.md#hitl-approval).

## Onboarding (empty state)

Before the first task, the chat pane shows a welcome panel naming the agent, three example prompts, and the `/help` hint — so a new user knows what to do instead of staring at a blank screen.

## Slash commands

Type a line starting with `/` to run a command (local or runtime). They execute immediately on Enter — no goal is submitted.

| Command | Scope | Action |
|---|---|---|
| `/help` | local | Toggle the help overlay — always available, unlike `?` |
| `/clear` | local | Wipe the chat pane + reset scroll |
| `/theme` | local | List themes + current (`dark light contrast mono`) |
| `/theme <name>` | local | Switch palette immediately (e.g. `/theme contrast`) |
| `/model` | runtime | (no effect — usage: `/model <name>`) |
| `/model <name>` | runtime | Swap model, rebuild provider, keep conversation history |
| `/provider` | runtime | List all 29 provider presets |
| `/provider <name>` | runtime | Switch provider preset (e.g. `/provider groq`) |
| `/status` | runtime | Show current model, provider, theme |

**Local commands** (help, clear, theme) run in the TUI without the runtime.

**Runtime commands** (model, provider, status) go through the event bus to the driver, which rebuilds the cognitive Core and swaps the provider. They're blocked while a task is running (finish or cancel first). The response appears as a `system:` line in the chat.

### Provider presets

29 presets are built in (OpenAI, Anthropic-compat, Together, Groq, Mistral, DeepSeek, OpenRouter, Ollama, LM Studio, ...). Use `/provider` to list them, `/provider <name>` to switch. Each preset sets the base URL + default model; the API key comes from the preset's env var (e.g. `GROQ_API_KEY`) or falls back to `YOLO_API_KEY`. Local providers (Ollama, LM Studio) need no key.

## Headless vs Interactive

| | Interactive (TUI) | Headless |
|---|---|---|
| Launch | `yolo` | `yolo --headless` |
| Output | TUI | JSON lines (stdout) |
| HITL | Prompt in TUI | Auto-approve config |
| Use for | Day-to-day development | Scripts, CI, tests |

## See also

- [Quickstart](quickstart.md) — first run
- [Commands & Flags](commands.md) — all flags
- [Tools Reference](tools.md) — tool details
- [Configuration](configuration.md) — HITL approval configuration
