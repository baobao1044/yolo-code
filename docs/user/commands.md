# Commands and Flags

## Global flags

| Flag | Default | Description |
|---|---|---|
| `--headless` | false | Run without TUI; emit JSON events to stdout |
| `--repo` | cwd | Repository root for the context engine |
| `--open` | "" | Comma-separated list of files to load into context |
| `--version` | n/a | Print version and exit |

## Environment variables

| Variable | Purpose |
|---|---|
| `YOLO_LOG` | Path to structured log file |
| `OPENAI_API_KEY` | API key for the cognitive core (when wired) |

## Exit codes

- `0` — task completed successfully.
- `1` — task failed, cancelled, or an unexpected error occurred.
- `130` — interrupted by the user.

The interactive mode returns `0` only when the task reaches `task.completed`.
The headless mode returns `0` if the transcript ends in a completed state.
