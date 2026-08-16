# yolo-code Configuration

## Overview

yolo-code is configured via 3 mechanisms (in descending priority):

1. **Command-line flags** — override everything
2. **Environment variables** — primary for deployment
3. **File `.env`** — convenient for local development

## LLM Provider

### Required variables

| Variable | Description | Example |
|---|---|---|
| `YOLO_API_KEY` | API key for the LLM provider (canonical) | `sk-...` |

> `OPENAI_API_KEY` is read as a fallback ONLY if `YOLO_API_KEY` is unset.

### Optional variables

| Variable | Default | Description |
|---|---|---|
| `YOLO_BASE_URL` | `https://api.openai.com/v1` | Base URL of the OpenAI-compatible API |
| `YOLO_MODEL` | `gpt-4o` | Model name |
| `YOLO_WINDOW` | `128000` | Context window size (tokens). A non-numeric or non-positive value is ignored |
| `YOLO_PROVIDER` | — | Provider preset name (`openai`, `groq`, `ollama`, …). Use `/provider` in the TUI to list and switch |
| `YOLO_STUB` | — | Use the deterministic offline stub provider (`--stub`). **Not a real model** — its replies carry a stub label |

> Selecting a provider never falls back silently. If the preset resolves but the
> key is missing, the run fails at the first request with an actionable error
> rather than quietly answering from the stub. A key set for one provider is not
> reused for another, and `OPENAI_API_KEY` is not sent to a non-OpenAI host.

### Popular providers

#### OpenAI

```bash
export YOLO_API_KEY="sk-..."
export YOLO_BASE_URL="https://api.openai.com/v1"
export YOLO_MODEL="gpt-4o"
```

#### Custom provider

Any API compatible with OpenAI chat completions:

```bash
export YOLO_API_KEY="your-key"
export YOLO_BASE_URL="https://your-api.com/v1"
export YOLO_MODEL="your-model"
```

## Sandbox

| Variable | Default | Description |
|---|---|---|
| `YOLO_REPO_ROOT` | `.` (cwd) | Repo root directory — the sandbox confines file operations within this |

The sandbox automatically:
- Rejects path escapes (`../../etc/passwd`)
- Peels wrappers (`sudo`, `env`, `time`) before classification
- Classifies commands by risk level
- Network default-deny

## HITL Approval

| Variable | Default | Description |
|---|---|---|
| `YOLO_AUTO_APPROVE_MEDIUM` | `false` | Auto-approve medium-risk tools (e.g. `bash` with safe commands) |
| `YOLO_AUTO_APPROVE_HIGH` | `false` | Auto-approve high-risk tools (e.g. `edit_file`, `bash` with dangerous commands) |

Both are read, and both default to false, so **the gate is on unless you turn it
off**. `newExecEngine` (`cmd/yolo/headless.go`) is the single place that builds
the dispatcher for all three modes — headless, TUI and `--plan` — so there is no
laxer second policy to fall through to. `--auto-approve` is shorthand that sets
both. Critical-risk tools are denied by `exec` itself and cannot be approved.

> An earlier build hardcoded `AutoApprove{RiskMedium: true, RiskHigh: true}` at
> each composition root and read neither variable, so no shipped path ever asked
> a human anything. That is fixed; if you are reading a copy of this note that
> still says the variables are ignored, it is out of date.

> **Headless mode**: with the gate on there is no human to answer it, so the
> agent refuses the first medium/high-risk action rather than hanging — the task
> ends `CANCELLED` and the reason goes to stderr. Enable auto-approve when you
> want an unattended run to proceed:

```bash
export YOLO_AUTO_APPROVE_MEDIUM=true
export YOLO_AUTO_APPROVE_HIGH=true
```

> **Interactive mode**: The TUI displays an approval prompt, no need for auto-approve.

### Risk classification

| Risk | Tools | Behaviour |
|---|---|---|
| **Low** | `list_files`, `read_file`, `grep` | Runs automatically |
| **Medium** | `bash` (safe commands) | Requires approval (or auto-approve) |
| **High** | `edit_file`, `bash` (dangerous commands) | Requires approval (or auto-approve) |
| **Critical** | `bash` (shell escape, rm -rf) | Always rejected |

## Cost and ceilings

Two separate mechanisms live here and they do very different things. Read which
one a knob belongs to before you rely on it:

- The **cost publisher** only watches the event bus. It counts tool calls,
  prices them from the rates you supply, and prints a warning when a ceiling is
  crossed. It cannot halt anything — the run sails past the ceiling.
- The **cost ledger** sits inside the drive loop. When a hard cap trips it
  publishes `cost.abort`, drives the task's state machine to `CANCELLED`, and
  cancels the task with the name of the cap that fired.

yolo-code counts tool calls, which it can measure. It does **not** ship a price
list: a rate nobody supplied is a rate nobody can vouch for, and an invented
`$0.00` reads as a measurement rather than as the absence of one. Supply your
own rates and the cost rail starts showing money, marked as your estimate.

### Warnings — the run is not halted

| Variable | Default | Description |
|---|---|---|
| `YOLO_COST_RATES` | (empty) | Comma-separated `tool=dollars` pairs, e.g. `bash=0.005,read_file=0.001`. `*` sets the rate for tools not named explicitly. Unparseable and negative entries are reported on stderr and ignored |
| `YOLO_MAX_TOOL_CALLS` | (no cap) | Warns once when total tool invocations for the run reach this number. A ceiling on a measured quantity, so it means the same with or without rates. **Warning only** — nothing stops |

### Hard caps — these abort the task

| Variable | Default | Description |
|---|---|---|
| `YOLO_MAX_COST` | (no cap) | Dollar spend cap. When a task's accrued token spend reaches it, the task is **killed**: `cost.abort` is published, the FSM lands in `CANCELLED`, and the cancellation reason is `spend cap reached`. Needs `YOLO_COST_PER_TOKEN` to ever fire |
| `YOLO_MAX_TIME` | (no cap) | Wall-clock cap as a Go duration (`10m`, `90s`, `1h30m`), measured from when the task was registered. On trip the task is cancelled with reason `time cap reached` |
| `YOLO_COST_PER_TOKEN` | `0` (tokens accrue no dollars) | Price of one token in dollars, e.g. `0.000003`. One rate covers input and output alike: a turn costs `(tokens_in + tokens_out) × rate` |

`YOLO_MAX_COST` **changed meaning**. It used to be a soft ceiling that only the
publisher observed, priced from your `YOLO_COST_RATES` tool-call table. It is
now also the ledger's spend cap, and exceeding it ends the task. The old warning
still fires too, so one number is compared against two independently computed
figures — the publisher's tool-call estimate, and the ledger's token spend. Set
it to whichever is the number you actually want enforced.

Pricing for the spend cap comes from `YOLO_COST_PER_TOKEN` and nothing else:
`YOLO_COST_RATES` is dollars-per-*tool-call* and cannot be converted into a
per-token rate. Only turns whose provider reported its token usage are billed —
a turn nobody measured contributes nothing rather than a free `0`, so a cap set
against a provider that reports no usage will never fire. If you need a bound on
that run, use `YOLO_MAX_TIME`.

`YOLO_MAX_TIME` is parsed by Go's `time.ParseDuration`, and **anything that is
not a well-formed non-negative duration silently means "no cap"** — the safe
direction for a typo is to leave the run uncapped, not to kill it instantly:

| You export | You get |
|---|---|
| `10m`, `90s`, `1h30m`, `500ms`, `  5m  ` | that duration |
| `600` | **no cap** — a bare number has no unit and Go rejects it. This is the typo most likely to bite: it looks like ten minutes and caps nothing |
| `1d` | **no cap** — there is no day unit; write `24h` |
| `soon`, empty, unset | no cap |
| `-5m` | no cap — a negative deadline is already past and would abort every task on its first turn |
| `0s` | no cap |

The wall clock is checked once per planning turn, not on a timer, so a task
sitting inside one long tool call can overrun the deadline and is only stopped
when the loop next comes round to plan.

### No cap means genuinely unlimited

With neither `YOLO_MAX_COST` nor `YOLO_MAX_TIME` set to a positive value, the
ledger is **not built at all** — the runtime installs a no-op stub that counts
nothing and can never trip. There is no hidden default budget, no per-task
bookkeeping, and no dollar or wall-clock figure being accumulated behind your
back. An unbudgeted run really will keep going.

`YOLO_COST_PER_TOKEN` on its own does not arm anything: it is a price, not a
limit. Pair it with `YOLO_MAX_COST`.

### Loop and reflection thresholds are not caps

The agent also degrades itself after 6 reflection loops (reflection off) and 10
reflection calls (submit the best state for review). These are **degradation
rungs, not kill switches**: reaching one makes the agent do less, it does not
stop the task, and it does not cancel anything. Only `YOLO_MAX_COST` and
`YOLO_MAX_TIME` end a run. Neither threshold is configurable from the
environment — treating the loop count as a hard cap would turn a threshold tuned
for reflection into a six-turn limit on every task.

```bash
# warn only
export YOLO_COST_RATES="bash=0.005,edit_file=0.002,*=0.001"
export YOLO_MAX_TOOL_CALLS=200

# actually stop
export YOLO_COST_PER_TOKEN=0.000003
export YOLO_MAX_COST=2.50
export YOLO_MAX_TIME=15m
```

With rates set the rail reads `cost: 42 tool calls · ~1.2k tok · ~$0.21 (your
rates)`; without them it reads `cost: 42 tool calls · ~1.2k tok`. The token
figure is an estimate in both cases.

## Persistence

Memory and session transcripts are written under your user config directory and
survive the process. Both roots can be redirected, which is what tests and
sandboxed runs do so they never touch your real state.

| Variable | Default | Description |
|---|---|---|
| `YOLO_MEMORY_DIR` | `<user config dir>/yolo-code/memory` | Durable root for the memory store (preferences, lessons, the repo index) |
| `YOLO_SESSION_DIR` | `<user config dir>/yolo-code/sessions` | Durable root for session and task transcripts |

> Both were temporary directories deleted on exit until recently: the runner
> flushed everything to disk and then threw the disk away, so nothing could be
> resumed and no lesson outlived the run.

A store file that fails to parse is moved aside to `<name>.corrupt` rather than
aborting startup, and the quarantine is reported on stderr:

```
yolo: memory: corrupt store file .../preference.json: invalid character 'n' ...
```

## Appearance

These affect the TUI only; headless and plan mode ignore them.

| Variable | Default | Description |
|---|---|---|
| `YOLO_THEME` | `dark` | Color theme: `dark`, `light`, `contrast`, `mono` |
| `NO_COLOR` | — | Any non-empty value forces the `mono` theme, per [no-color.org](https://no-color.org/). Takes precedence over `YOLO_THEME` |
| `YOLO_NO_MOTION` | — | Any non-empty value disables the spinner and cursor blink (reduced motion) |

## Logging

| Variable | Default | Description |
|---|---|---|
| `YOLO_EVENT_LOG` | (empty) | Append every event to a durable, fsynced log. Also `--event-log <path>` |
| `YOLO_LOG` | (empty) | Structured log file path (slog format). **Not read by the current code** — the logger takes its destination from `infra.Config`, which nothing populates from the environment |

`YOLO_EVENT_LOG` is the one that works. It records the event stream — state
transitions, tool calls and their results, cost events — fsynced as it goes, so
a run stays replayable after a crash:

```bash
yolo --headless --event-log /tmp/yolo-events.jsonl < task.txt
grep tool.result /tmp/yolo-events.jsonl
```

`YOLO_LOG` sets nothing. Exporting it produces no file and no error; the
variable is reserved for a logger destination that is not wired to the
environment yet. Use `YOLO_EVENT_LOG` instead. (This section used to describe
what `YOLO_LOG` would write, immediately below the note saying it is not read —
the description was aspirational, and following it left you grepping a file
that was never created.)

## File .env

Copy `.env.example` and edit:

```bash
cp .env.example .env
```

```ini
# LLM Provider
YOLO_API_KEY=sk-...
YOLO_BASE_URL=https://api.openai.com/v1
YOLO_MODEL=gpt-4o

# Logging
YOLO_EVENT_LOG=
# YOLO_LOG is not read by the current code — see the Logging section
YOLO_LOG=

# Auto-approve (headless)
YOLO_AUTO_APPROVE_MEDIUM=true
YOLO_AUTO_APPROVE_HIGH=true
```

yolo-code automatically loads `.env` from the current directory on startup.

## Command-line flags

Flags override environment variables:

| Flag | Env equivalent | Description |
|---|---|---|
| `--headless` | — | Run without TUI |
| `--repo <path>` | `YOLO_REPO_ROOT` | Repo root |
| `--model <name>` | `YOLO_MODEL` | Override model |
| `--base-url <url>` | `YOLO_BASE_URL` | Override API URL |
| `--plan <goal>` | — | Multi-agent orchestrator for a complex goal |
| `--version` | — | Print version |

## Configuration examples

### Development (local)

```bash
# .env
YOLO_API_KEY=sk-abc123
YOLO_MODEL=gpt-4o
YOLO_AUTO_APPROVE_MEDIUM=true
YOLO_AUTO_APPROVE_HIGH=true
```

```bash
yolo  # interactive mode
```

### CI/CD (headless)

```bash
export YOLO_API_KEY="${{ secrets.API_KEY }}"
export YOLO_BASE_URL="https://api.openai.com/v1"
export YOLO_MODEL="gpt-4o"
export YOLO_AUTO_APPROVE_MEDIUM=true
export YOLO_AUTO_APPROVE_HIGH=true

echo "fix bug #42" | yolo --headless --repo /path/to/repo
```

### Debug mode

```bash
yolo --headless --event-log /tmp/yolo-events.jsonl < task.txt 2>&1 | tee /tmp/yolo-output.json
```

`--event-log` is what to reach for here; `YOLO_LOG` writes nothing (see
[Logging](#logging)).
