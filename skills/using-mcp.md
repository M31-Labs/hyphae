---
name: using-mcp
description: Use when running inside an MCP-aware runtime with `hypha mcp serve` wired in — prefer the bundled MCP tools (`hypha_recall`, `hypha_show`, `hypha_pulse`, …) over shelling out to the `hypha` CLI. Same data, fewer process spawns, integrated transport. Apply token discipline: pass `max_tokens` and `fields` on every list tool, and `format: "compact"` for hot-path calls.
triggers:
  - Hyphae MCP server is configured in the runtime (tools appear as `hypha_*` / `mcp__hyphae__hypha_*`)
  - About to call `hypha recall` / `hypha show` / `hypha pulse` / etc.
  - About to submit a spore or open a trace from inside an MCP session
boundary_rules:
  - prefer-mcp-tools-when-available
  - apply-token-budget-to-every-call
  - hypha_graft-defaults-to-dry-run
---

# Skill: using Hyphae via MCP

When `hypha mcp serve` is wired into the runtime, the Hyphae read +
mutate surface appears as 29 first-class tools. Use them instead of
shelling out to `hypha <subcommand>` — same data, fewer process
spawns, integrated transport, response shaping baked in.

If MCP isn't wired (tools don't appear), fall back to the
[`hyphae`](hyphae.md) skill and shell out.

## Tool naming

Depending on your runtime, the tools appear as either:

- `hypha_recall`, `hypha_show`, … (plain name)
- `mcp__hyphae__hypha_recall`, `mcp__hyphae__hypha_show`, … (prefixed)

Both refer to the same tool. This skill uses the plain form.

## The 29 tools at a glance

**Read (16):** `hypha_recall`, `hypha_show`, `hypha_pulse`,
`hypha_assess_task`, `hypha_assess_change`, `hypha_assess_pr`,
`hypha_spaces_list`, `hypha_graph_backlinks`, `hypha_graph_related`,
`hypha_graph_trace`, `hypha_identity_list`, `hypha_spore_list`,
`hypha_trace_list`, `hypha_trace_history`, `hypha_receipts_list`,
`hypha_analyze_list`.

**Mutate (13):** `hypha_index_rebuild`, `hypha_spore_submit`,
`hypha_spore_accept`, `hypha_spore_reject`, `hypha_graft`,
`hypha_trace_start`, `hypha_trace_tick`, `hypha_trace_done`,
`hypha_trace_reap`, `hypha_identity_init`, `hypha_cap_issue`,
`hypha_analyze_run`, `hypha_analyze_refresh`.

## Token discipline (use it)

Every tool accepts three shaping knobs:

| Arg | Effect | Default |
| --- | --- | --- |
| `format` | `jsonline` (full keys, single-line) / `json` (indented, debug) / `compact` (short keys, smallest) | `jsonline` |
| `max_tokens` | Soft response budget; list-shaped responses get trailing rows trimmed with a `TRUNCATED` warning attached | Per-tool default (300–2000) |
| `fields` | Whitelist of top-level row fields for list tools (drop `path`, `phase`, etc. when you only need `id`/`status`) | (all fields) |

Default to `format: "compact"` for hot-path agent calls. Use
`format: "jsonline"` when the agent will display the result to the
user (full keys read better in chat).

Default `max_tokens` is sane but you should override:

- **Hard cap for routine lookups**: `max_tokens: 300`
- **Recall with snippets**: `max_tokens: 800` (default is already this)
- **Graft with diffs**: `max_tokens: 2000`

## Common patterns

### Token-conscious recall

```json
{
  "name": "hypha_recall",
  "arguments": {
    "query": "webhook retries",
    "max_tokens": 400,
    "format": "compact"
  }
}
```

### Lean list (drop verbose fields)

```json
{
  "name": "hypha_spore_list",
  "arguments": {
    "status": "unreviewed",
    "fields": ["id", "status", "submitted_at"],
    "max_tokens": 200
  }
}
```

### Preview a graft

`hypha_graft` defaults to **dry-run** — that's safer than the CLI.
To actually apply, pass `apply: true`.

```json
{
  "name": "hypha_graft",
  "arguments": {
    "spore_id": "spore.2026-05-28.you.X",
    "as": "identity://myorg/you",
    "diff": true
  }
}
```

Response includes `dry_run: true`, the would-be receipt, and a
`diffs[]` array of unified diffs per touched file.

```json
{
  "name": "hypha_graft",
  "arguments": {
    "spore_id": "spore.2026-05-28.you.X",
    "as": "identity://myorg/you",
    "apply": true,
    "verify": true
  }
}
```

### Open / tick / close a trace from inside MCP

```json
{ "name": "hypha_trace_start",
  "arguments": {
    "agent": "agent://my-runtime/me",
    "task": "task-123",
    "phase": "design"
  } }

→ {"data": {"ID": "trace.2026-05-28.me.7f3a", ...}}

{ "name": "hypha_trace_tick",
  "arguments": {
    "trace_id": "trace.2026-05-28.me.7f3a",
    "message": "first cut compiles"
  } }

{ "name": "hypha_trace_done",
  "arguments": {
    "trace_id": "trace.2026-05-28.me.7f3a",
    "status": "succeeded",
    "link_spore": "spore.2026-05-28.me.design-X"
  } }
```

## When to prefer CLI over MCP

A few cases where shelling out to `hypha …` is still the right move:

| Case | Why |
| --- | --- |
| You need `--format text` for human display | MCP responses are always JSON; text rendering happens in the runtime |
| You're scripting (bash pipeline) | The CLI is the right surface for scripts |
| You need `hypha trace tail` (long-running stream) | MCP tools are request/response, not streaming |
| You're debugging the MCP server itself | Run the CLI in parallel to compare |

For everything else, MCP tools are better integrated.

## Composing with other skills

- [`hyphae`](hyphae.md) — the read/contribute mental model still
  applies; MCP just changes the transport.
- [`assessing-changes`](assessing-changes.md) — use `hypha_assess_task`
  before non-trivial work; surface the result the same way.
- [`using-traces`](using-traces.md) — use `hypha_trace_start|tick|done`
  instead of shelling out.

## Boundary rules

1. **Apply token discipline on every call.** The defaults are sensible
   but list responses can balloon — `max_tokens` and `fields` cost
   nothing to pass and prevent context-window blow-up.
2. **`hypha_graft` defaults to dry-run.** Don't accidentally pass
   `apply: true` when you meant to preview. The CLI defaults the
   other way; MCP is safer on purpose.
3. **Watch for `warnings[]`.** A `TRUNCATED` warning means you
   probably need a tighter filter, not a bigger budget.
4. **Don't poll.** MCP tools are request/response. For streaming, use
   `hypha trace tail` from the CLI side-by-side.
5. **Mutating tools write real files.** No sandbox; the MCP server
   mutates the same `~/.hyphae/` install root as the CLI.
