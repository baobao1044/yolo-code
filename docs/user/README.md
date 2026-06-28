# yolo-code User Documentation

`yolo-code` is a multi-agent terminal coding agent. It reads a task from stdin,
drives a cognitive core through the tool stack, and either edits your repo or
reports back what it found.

## Modes

- **Headless** (`yolo --headless`) — no TTY; emits one JSON line per event.
  Useful for scripts, golden tests, and CI.
- **Interactive** (`yolo`) — starts the TUI with the multi-agent board, cost
  meter, diff viewer, and status bar.

## Key files

- `README.md` — this overview.
- `quickstart.md` — install and first run.
- `commands.md` — flags, environment variables, and exit codes.

## Safety notes

All filesystem writes and shell commands go through the `exec` sandbox. The
sandbox denies path escapes, classifies network/disk/shell-escape commands as
high/critical risk, and peels wrappers like `sudo` before deciding. See
`docs/security/sandbox-redteam.md` for the current red-team checklist.
