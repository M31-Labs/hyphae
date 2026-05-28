---
name: using-traces
description: Use BEFORE starting non-trivial multi-step work, when dispatched as a subagent, or when work involves regenerating code/state that produces ambiguous diagnostics. Open a trace with `hypha trace start --agent <your-uri> --task <id> --phase "<label>"`, then `hypha trace tick <id> "<one-sentence checkpoint>"` at natural boundaries. Close with `hypha trace done <id> --status succeeded|failed|killed`.
triggers:
  - starting non-trivial multi-step work
  - dispatched as a subagent
  - regenerating code/state that produces ambiguous diagnostics
  - user asks "what's running right now" / "what are the subagents doing"
boundary_rules:
  - always-close-traces
  - tick-at-natural-boundaries
  - one-sentence-ticks
---

# Skill: using traces

Traces are the third leg of the Hyphae knowledge tripod:

- **Decisions** answer *what was chosen*.
- **Lessons** answer *what to do next time*.
- **Traces** answer *how the work actually went*.

They survive agent crashes, enable handoff between subagents, and
power "what's running right now" queries.

## When to open a trace

| Situation | Open a trace |
| --- | --- |
| Multi-step work (3+ tool calls, multiple files, dispatch) | yes |
| Dispatched as a subagent | yes — caller may inspect via `hypha trace list --active` |
| Regenerating code/state that emits ambiguous diagnostics | yes — checkpoint each attempt |
| One-shot answer / quick lookup | no |
| Trivial typo fix / comment tweak | no |
| Read-only exploration | usually no, unless it's long enough that resumption matters |

## The lifecycle

```bash
# 1. Open at the start.
hypha trace start \
  --agent agent://<runtime>/<short> \
  --task <task-id> \
  --phase "<short label>" \
  --space hypha://<authority>/<name>
# → trace.2026-05-28.<short>.1d5a   (capture the id)

# 2. Tick at natural boundaries.
hypha trace tick <trace-id> "first cut compiles"
hypha trace tick <trace-id> "tests green; reviewing edge cases"
hypha trace tick <trace-id> "found subtle interaction with retry logic; fixing"

# 3. Close on completion.
hypha trace done <trace-id> --status succeeded \
  --link-spore spore.2026-05-28.you.X

# Or, if things went sideways:
hypha trace done <trace-id> --status failed
hypha trace done <trace-id> --status killed     # user interrupted
hypha trace done <trace-id> --status superseded # work redone elsewhere
```

## Natural tick boundaries

A "natural boundary" is one of:

- **Turn end** — finishing a chunk you'd send to the user.
- **Tool complete** — a write finished, a test ran, a search returned.
- **File written** — a new file landed, or an old one was edited.
- **Phase done** — moved from research → design → implementation, etc.
- **Decision point** — when you chose between two paths.
- **Blocker found** — when you hit something that requires user input.

Don't tick every line of output. A useful tick is one sentence that
would help your *future self* (or a handoff agent) understand what
state the work was in at that moment.

## Tick style

Good ticks:

- `"draw loop wired; testing with sample data"`
- `"hot-path benchmark shows 12µs/op; under target"`
- `"sqlite migration green; one row count off in tests, investigating"`

Bad ticks:

- `"working on it"` (no information)
- `"called grep for 'foo'; got 23 results; iterating; tool returned …"` (verbose; let the body capture detail)
- `"step 1 done"` (no context)

## What's running right now

```bash
hypha trace list --active                                # all open traces, all spaces
hypha trace list --active --agent agent://my-runtime/me  # mine only
hypha trace list --space hypha://myorg/knowledge         # one space
```

Useful for orchestrators dispatching subagents: tick frequency + last
tick time tell you whether the subagent is making progress or hung.

## Reaping stale traces

If an agent crashed mid-work, its trace stays `open` forever. Periodic
cleanup:

```bash
hypha trace reap --older-than 1h --space hypha://myorg/knowledge
```

This force-closes any open trace whose `last_tick` exceeded the
threshold, annotates the body with the reaping reason, flips status
to `killed`, and persists atomically. Safe to run on a cron.

## Methodology recall

Closed traces are FTS5-indexed. When you start similar work, ask
Hyphae if you (or someone) solved it before:

```bash
hypha trace history --similar "force-directed graph"
hypha trace history --task task-25
hypha trace history --agent agent://my-runtime/me --limit 5
```

Returns ranked closed traces. `hypha show <trace-id>` to read the full
work log.

## Live tail

For real-time observation (orchestrator watching a subagent):

```bash
hypha trace tail --id <trace-id>                          # one trace
hypha trace tail --agent agent://my-runtime/me            # all from one agent
hypha trace tail --interval 500ms --timeout 5m            # tighten polling
```

Polls (no fsnotify dep); prints new ticks as they arrive.

## Linking traces to spores

When you close a trace with `--link-spore`, the compacted work log is
appended to the spore body as a `## Work log (trace.…)` section.
Reviewers grafting that spore can see exactly how the work went.

```bash
hypha trace done <trace-id> --status succeeded \
  --link-spore spore.2026-05-28.you.feature-X
```

Idempotent — running it twice doesn't double-append.

## Boundary rules

1. **Always close traces.** Open without close = stale trace. Use
   `--status failed` or `killed` if work didn't complete; don't leave
   it open.
2. **Tick at natural boundaries, not on a timer.** Ticks are for
   *checkpoints*, not heartbeats.
3. **One sentence per tick.** Verbose ticks balloon trace files and
   make `hypha trace tail` noisy.
4. **Use one trace per task.** Don't multiplex multiple unrelated
   tasks into one trace. Open separate traces for separate work.
5. **Tag with a task ref when possible.** `--task` makes
   `hypha trace history --task <id>` useful later.

## Failure modes

| Symptom | Fix |
| --- | --- |
| `trace: not found` on tick/done | The id is wrong, or the date prefix in the id doesn't match the file. The CLI falls back to a full scan, so this is rare — but if it persists, check that `<space>/.trace/<YYYY-MM-DD>/<id>.md` exists. |
| `trace: already closed` on tick | The trace was reaped or closed elsewhere. Open a new one. |
| Lots of stale open traces | Run `hypha trace reap --older-than 1h`. |
| `hypha trace tail` shows nothing | The agent isn't ticking. That itself is a signal. |
