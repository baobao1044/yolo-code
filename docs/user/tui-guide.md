# TUI Guide

The yolo-code TUI is built on [bubbletea](https://github.com/charmbracelet/bubbletea) + [lipgloss](https://github.com/charmbracelet/lipgloss) + [bubbles](https://github.com/charmbracelet/bubbles).

## Launch

```bash
yolo
```

The TUI takes over the entire terminal. Enter a task at the prompt.

## Layout

```
┌─────────────────────────────────────────────┐
│  Status Bar — FSM state + cost meter        │
├─────────────────────────────────────────────┤
│                                             │
│  Board — Multi-agent progress               │
│                                             │
│  Shows:                                     │
│  • What the current agent is doing          │
│  • Tool calls and results                   │
│  • State transitions                        │
│  • Diff viewer (when edit_file runs)        │
│                                             │
├─────────────────────────────────────────────┤
│  Cost Meter — Token usage + cost            │
├─────────────────────────────────────────────┤
│  Input Prompt — Type task here              │
└─────────────────────────────────────────────┘
```

### Status Bar

Displays the current FSM state:

| State | Display | Meaning |
|---|---|---|
| IDLE | `●` | Waiting for task |
| PLAN | `◆` | Planning |
| THINK | `◉` | LLM is thinking |
| EXEC | `▶` | Running a tool |
| WAIT_TOOL | `◷` | Waiting for tool to finish |
| VERIFY | `✓` | Verifying results |
| DONE | `✔` | Completed |

### Board

Real-time display:
- **Tool calls**: `read_file("main.go")` → result
- **State transitions**: THINK → EXEC → WAIT_TOOL
- **Diff**: when `edit_file` runs, shows a diff view
- **Multi-agent**: when the coordination layer splits a task, the board shows multiple agents

### Cost Meter

- Token usage (input + output)
- Estimated cost (USD)
- Updated on every LLM call

## Interaction

### Entering a task

1. Type the task at the input prompt
2. Press **Enter** to submit
3. The agent starts processing

### HITL Approval

When a tool needs approval (medium/high risk), the TUI displays a prompt:

```
⚠ bash: "go test ./..."  [Medium Risk]
Approve? [y/n]
```

- **y** → run the tool
- **n** → reject, agent receives "denied by user" result

### Interrupt

- **Ctrl+C** — Cancel the current task, exit the TUI (exit code 130)
- Agent stops at the current state, session is not auto-saved

## Headless vs Interactive

| | Interactive (TUI) | Headless |
|---|---|---|
| Launch | `yolo` | `yolo --headless` |
| Output | Beautiful TUI | JSON lines (stdout) |
| HITL | Prompt in TUI | Auto-approve config |
| Use for | Day-to-day development | Scripts, CI, tests |
| Input | TTY prompt | Stdin |

### When to use TUI

- Day-to-day development — see what the agent is doing in real-time
- Debug — track tool calls and state transitions
- Learn how the agent works

### When to use Headless

- CI/CD pipelines
- Batch automation
- Golden tests — deterministic output for regression
- Scripts — pipe task into stdin, process JSON output

## Example session

```bash
$ yolo
```

```
┌─────────────────────────────────────────────┐
│ ● IDLE                                      │
├─────────────────────────────────────────────┤
│                                             │
│ > create a CLI tool that computes fibonacci │
│                                             │
├─────────────────────────────────────────────┤
│ ◉ THINK — LLM is thinking...               │
├─────────────────────────────────────────────┤
│ ▶ EXEC — list_files()                       │
│   → 15 files found                          │
│                                             │
│ ▶ EXEC — edit_file("fibonacci/main.go")    │
│   ⚠ High Risk — Approve? [y/n] y           │
│   → File written successfully               │
│                                             │
│ ▶ EXEC — edit_file("fibonacci/main_test.go")│
│   → File written successfully               │
│                                             │
│ ▶ EXEC — bash("go test ./...")             │
│   → PASS                                    │
│                                             │
│ ✓ VERIFY — all checks passed                │
│                                             │
│ ✔ DONE — Task completed                     │
├─────────────────────────────────────────────┤
│ Tokens: 2,847 | Cost: $0.04                 │
└─────────────────────────────────────────────┘
```

## See also

- [Quickstart](quickstart.md) — first run
- [Commands & Flags](commands.md) — all flags
- [Tools Reference](tools.md) — tool details
- [Configuration](configuration.md) — HITL approval configuration
