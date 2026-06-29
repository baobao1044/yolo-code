# yolo-code — User Documentation

`yolo-code` is a multi-agent terminal coding agent in Go. It reads a task, drives the cognitive core through the tool stack, and either modifies the repo or reports results.

## Running Modes

- **Headless** (`yolo --headless`) — no TTY; emits 1 JSON line per event. Ideal for scripts, golden tests, CI.
- **Interactive** (`yolo`) — launches a TUI with multi-agent board, cost meter, diff viewer, and status bar.

## Architecture Overview

```
┌──────────── User ──────────────────────┐
│  Headless (JSON)   │   TUI (terminal)   │
└────────┬───────────┴──────────┬─────────┘
         │                      │
    ┌────▼──────────────────────▼────┐
    │        Event Bus (L3)         │
    └──┬─────┬──────┬──────┬───────┘
       │     │      │      │
  Session  Runtime  Context  Cognitive
   (L1)     (L2)    (L4)     (L6)
                    │         │
                 Prompt    Execution
                (L5) ──►  (L7) ──► Patch (L9)
                                      │
                                 Verify (L8)
                                      │
                                 Memory (L10)
                                      │
                            Multi-Agent (L11)
```

**Data flow**: User Task → Context Engine → Prompt Compiler → Cognitive Core → Tool Execution → Verification → Loop or Done

**Golden rules**:
- Each layer depends only on lower layers + Event Bus
- TUI is subscribe-only, never holds logic
- Memory updates ONLY via events
- Runtime FSM runs on exactly 1 goroutine

See [Architecture](architecture.md) for details.

## Safety

All filesystem writes and shell commands go through the `exec` sandbox. The sandbox:
- Rejects path escapes (`../../etc/passwd` → `ErrPathEscapes`)
- Classifies network/disk/shell-escape commands as high/critical risk
- Peels wrappers like `sudo` before deciding

See `docs/security/sandbox-redteam.md` for the red-team checklist.

## Documentation

| File | Content |
|---|---|
| [Quickstart](quickstart.md) | Install and run for the first time |
| [Commands & Flags](commands.md) | Flags, env vars, exit codes |
| [Architecture](architecture.md) | 12-layer architecture |
| [Configuration](configuration.md) | Full configuration |
| [Tools Reference](tools.md) | 4 tools, schema, risk, HITL |
| [TUI Guide](tui-guide.md) | How to use the TUI |
