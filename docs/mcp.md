# MCP integration

`hypha mcp serve` is a Model Context Protocol stdio server that exposes
the Hyphae read + mutate surface as 29 tools, with token-aware response
shaping built in. It's the right wiring when your agent runtime (Claude
Code, Cursor, custom JSON-RPC client) speaks MCP natively.

For agents that don't speak MCP, drop the
[skill files](../skills/README.md) into your runtime and let the agent
shell out to `hypha …` directly.

## What you get

Run `hypha mcp serve` and the agent gets, over a single stdio session:

- **16 read tools** — recall, show, pulse, assess (task/change/pr),
  graph (backlinks/related/trace), identity_list, spaces_list,
  spore_list, trace (list/history), receipts_list, analyze_list.
- **13 mutate tools** — index_rebuild, spore_submit, spore_accept,
  spore_reject, graft (dry-run by default), trace (start/tick/done/reap),
  identity_init, cap_issue, analyze (run/refresh).

Every tool accepts the same token-discipline knobs:

- `format` — `jsonline` (default), `json` (debug), `compact` (smallest).
- `max_tokens` — soft response budget. List rows get trimmed when over;
  a `TRUNCATED` warning is attached.
- `fields` — whitelist of top-level row fields for list tools (drop
  `path`, `phase`, etc. when the agent only needs `id` and `status`).

Mutations are CLI-equivalents but with safer defaults:

- `hypha_graft` defaults to **dry-run**. Pass `apply: true` to persist.
  Pass `diff: true` to include unified diffs in the response.
- All disk writes go through atomic `temp+rename` (same as the CLI).
- Receipts and edges land in the audit log.

## Quick check from the command line

```bash
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"0"},"capabilities":{}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
  '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"hypha_recall","arguments":{"query":"webhook"}}}' \
  | hypha mcp serve
```

You'll see three response objects (one per request with `id`). Tool
results land under `result.content[0].text` as a JSON-encoded envelope
(double-encoded because the MCP transport itself is JSON).

## Wiring it up

### Claude Code

In `~/.claude/settings.json` (or `.claude/settings.json` per-project):

```json
{
  "mcpServers": {
    "hyphae": {
      "command": "hypha",
      "args": ["mcp", "serve"],
      "env": {
        "HYPHAE_HOME": "/home/you/.hyphae",
        "HYPHAE_FORMAT": "jsonline"
      }
    }
  }
}
```

Restart Claude Code; tools appear as `mcp__hyphae__hypha_recall`,
`mcp__hyphae__hypha_pulse`, etc.

### Cursor

In `.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "hyphae": {
      "command": "hypha",
      "args": ["mcp", "serve"]
    }
  }
}
```

### Custom client

Any JSON-RPC 2.0 client that can spawn a subprocess and pipe stdio
will work. Send line-delimited JSON requests; receive line-delimited
JSON responses.

Protocol cheatsheet:

```
→ {"jsonrpc":"2.0","id":1,"method":"initialize","params":{...}}
← {"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","serverInfo":{...},"capabilities":{"tools":{}}}}

→ {"jsonrpc":"2.0","method":"notifications/initialized"}
  (no response)

→ {"jsonrpc":"2.0","id":2,"method":"tools/list"}
← {"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"hypha_recall","description":"…","inputSchema":{…}},…]}}

→ {"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"hypha_recall","arguments":{"query":"X"}}}
← {"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"<envelope JSON>"}]}}
```

## Tool reference

### Read tools

| Tool | Required args | Notable optional args | Returns |
| --- | --- | --- | --- |
| `hypha_recall` | `query` | `limit`, `max_tokens`, `shape` | Ranked hits + snippets + citations |
| `hypha_show` | `id` | `slice` (`metadata`/`frontmatter`/`body`/`full`/`path`) | Object slice |
| `hypha_pulse` | — | `space`, `window` | Time-windowed signal |
| `hypha_assess_task` | `task` | `space` | Alignment scoring |
| `hypha_assess_change` | `task` | `files`, `diff_summary`, `space` | Alignment with files |
| `hypha_assess_pr` | `task` | `base`, `source`, `space` | Alignment derived from git base...HEAD |
| `hypha_spaces_list` | — | — | Installed spaces |
| `hypha_graph_backlinks` | `object_id` | `kind`, `limit` | Edges pointing at the object |
| `hypha_graph_related` | `object_id` | `kind`, `limit` | Edges incident on the object |
| `hypha_graph_trace` | `object_id` | `kind`, `max_depth` | BFS along edge kinds |
| `hypha_identity_list` | — | — | Local identities |
| `hypha_spore_list` | — | `space`, `status`, `limit` | Inbox spores |
| `hypha_trace_list` | — | `space`, `agent`, `active` | Traces |
| `hypha_trace_history` | one of `similar`/`task`/`agent` | `space`, `include_open`, `limit` | FTS search of closed traces |
| `hypha_receipts_list` | — | `space`, `subject`, `action`, `since`, `limit` | Audit log |
| `hypha_analyze_list` | — | `kind`, `target_file`, `space` | Cached canopy analyses |

### Mutate tools

| Tool | Required args | Notable optional args | Returns |
| --- | --- | --- | --- |
| `hypha_index_rebuild` | — | — | Counts + db path |
| `hypha_spore_submit` | `path` | `sign`, `as` | Receipt + file path |
| `hypha_spore_accept` | `spore_id`, `as` | `reason`, `space` | Status flip receipt |
| `hypha_spore_reject` | `spore_id`, `as` | `reason`, `space` | Status flip receipt |
| `hypha_graft` | `spore_id`, `as` | **`apply`** (default `false`!), `diff`, `verify`, `no_fmt`, `space` | Plan or apply result |
| `hypha_trace_start` | `agent` | `task`, `phase`, `parent`, `session`, `space` | Trace id |
| `hypha_trace_tick` | `trace_id`, `message` | `space` | `{ok: true}` |
| `hypha_trace_done` | `trace_id` | `status`, `link_spore`, `space` | Closed trace |
| `hypha_trace_reap` | — | `older_than`, `space` | Reaped trace report |
| `hypha_identity_init` | `name`, `authority`, `space` | — | Identity + file paths |
| `hypha_cap_issue` | `subject`, `space` | `permissions`, `expires` | Capability token |
| `hypha_analyze_run` | `kind`, `space` | `target`, `source`, `diff_ref`, `max_depth`, `refresh` | Analysis |
| `hypha_analyze_refresh` | `analysis_id` | `space`, `source` | Refreshed analysis |

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

### Pulled-down list (drop verbose path field)

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

### Preview a graft before applying

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

The response includes `dry_run: true`, the would-be receipt, and a
`diffs[]` array with unified diffs per touched file. To actually apply:

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

### Open a trace, tick, close

```json
{ "name": "hypha_trace_start",
  "arguments": { "agent": "agent://my-runtime/me", "task": "task-123", "phase": "design" } }

→ {"data": {"ID": "trace.2026-05-28.me.7f3a", ...}}

{ "name": "hypha_trace_tick",
  "arguments": { "trace_id": "trace.2026-05-28.me.7f3a", "message": "first cut compiles" } }

{ "name": "hypha_trace_done",
  "arguments": {
    "trace_id": "trace.2026-05-28.me.7f3a",
    "status": "succeeded",
    "link_spore": "spore.2026-05-28.me.design-X"
  } }
```

## Limits and safety

- `hypha mcp serve` reads/writes the same `~/.hyphae/` install root as
  the CLI. No isolation; if your agent can mutate, it mutates real
  files.
- `hypha_graft` defaults to dry-run; treat `apply: true` as a real
  write.
- The MCP server does not expose `hypha-viz` (browser viz) or any
  network-bound surface.
- Capabilities (`hypha_cap_issue`) are persisted but not yet enforced
  by every code path — treat them as advisory until federation lands.
- For untrusted agents you don't want to mutate, run a custom build
  that strips the write tools (one line edit in `internal/mcp/tools.go`:
  drop `writeTools(s)...` from `buildTools`).

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `tools/list` returns nothing | `initialize` wasn't sent first | Send `initialize` then `notifications/initialized` then `tools/list` |
| `hypha_recall` returns no results | Index missing or stale | Call `hypha_index_rebuild` |
| `hypha_graft` says "applied 0, skipped 0" | Spore has zero `proposed_writes` (edges-only spore) | Expected — the spore status flipped, no canonical bytes changed |
| Responses too big | Default budget too generous for your context window | Pass `max_tokens` and/or `fields` on every list tool |
| Short keys confuse the agent | Using `format: "compact"` without the key map | Switch to `format: "jsonline"` |
