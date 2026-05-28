# CLI reference

Every `hypha` subcommand, every flag. Run `hypha --help` for a terse
in-binary version; this doc is the explained form.

## Conventions

- All commands accept `--format text|json|compact|jsonline`. See
  [output-formats.md](output-formats.md). The default auto-detects from
  the TTY.
- `--space <uri>` filters to one installed space (default: all).
- `--limit N` caps list-output rows.
- `--since <duration>` accepts Go durations plus `Nd` (e.g. `7d`,
  `30d`, `q1` → 90d).
- Subcommands that write to disk go through atomic `temp+rename`.

Exit codes: `0` success, non-zero on error. Error envelopes carry a
`code`, `message`, `hint`, and `path` (JSONPath-ish pointer into the
data payload).

## `hypha index rebuild`

Walk the install root and (re)populate `~/.hyphae/.index/hyphae.db`
(FTS5 + objects + anchors + edges tables) over every space.

```bash
hypha index rebuild [--root <path>]
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--root <path>` | `HYPHAE_HOME` or `~/.hyphae` | Install root override |
| `--format ...` | auto | Output format |

Rebuild whenever you add or edit files outside of `hypha spore submit` /
`hypha graft`.

## `hypha recall`

Full-text search across all installed spaces. BM25-ranked, token-budgeted,
returns hits with body snippets and per-snippet citations.

```bash
hypha recall <query> [--limit N] [--max-tokens N] [--shape ...] [--format ...]
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--limit N` | `12` | Max hits to consider before budgeting |
| `--max-tokens N` | `800` | Token budget for the full response |
| `--shape <name>` | `summary+anchors` | `headline` / `summary+anchors` / `count_only` |
| `--format ...` | auto | Output format |

Response data (per hit):

```json
{
  "uri": "hypha://...",
  "title": "...",
  "tokens_full": 1234,
  "score": -1.234,
  "snippets": [
    {
      "text": "…matched body span…",
      "citation": {
        "anchor": "hypha://...#heading-slug",
        "line": 42,
        "end_line": 44
      }
    }
  ]
}
```

When the budget is tight, snippets get trimmed before whole hits.

## `hypha show`

Fetch one object by id or `hypha://` URI. Closes the recall→read loop
without manual URI→path translation.

```bash
hypha show <id-or-uri> [--path | --json | --frontmatter | --body]
```

| Flag | Meaning |
| --- | --- |
| `--path` | Print the resolved absolute file path only |
| `--json` | Print object metadata as JSON (id, type, space, path, title, status, tags, summary, updated_at) |
| `--frontmatter` | Print just the YAML frontmatter block |
| `--body` | Print just the markdown body |
| (none) | Print the full file |

## `hypha spaces list`

Enumerate installed spaces under `$HYPHAE_HOME/spaces`.

```bash
hypha spaces list [--format ...]
```

## `hypha spore submit`

Validate a spore file and write it to the matching space's inbox.
Optionally Ed25519-sign first.

```bash
hypha spore submit <file> [--sign --as <identity-uri>] [--format ...]
```

| Flag | Meaning |
| --- | --- |
| `--sign` | Sign before submitting (requires `--as`) |
| `--as <uri>` | Signer identity URI |

Validation errors come back as `field "<path>": <message>` on stderr.
On success, the response carries the receipt id, on-disk path, and
content hash.

## `hypha spore list`

Enumerate inbox spores across installed spaces, newest first.

```bash
hypha spore list [--space <uri>] [--status <state>] [--since 24h] [--limit N] [--format ...]
```

`--status` filters by spore status: `unreviewed`, `accepted`,
`partial`, `rejected`, `duplicate`, `superseded`, `archived`.

## `hypha spore accept` / `hypha spore reject`

Flip an `unreviewed` spore to `accepted` / `rejected` without applying
any canonical writes. Persists a receipt; useful for queueing or
formal rejection.

```bash
hypha spore accept <spore-id> --as <identity> [--reason "..."] [--space <uri>] [--format ...]
hypha spore reject <spore-id> --as <identity> [--reason "..."] [--space <uri>] [--format ...]
```

To actually apply proposed_writes from an accepted spore, use
`hypha graft`.

## `hypha graft`

Apply a spore's `proposed_writes` and `proposed_edges` to canonical
files. The default is **dry-run** when `--diff` is set, **apply** when
neither `--dry-run` nor `--diff` is set. Use the matrix:

| Flag combo | Effect |
| --- | --- |
| (none) | Apply for real |
| `--dry-run` | Plan; no writes |
| `--diff` | Plan; print unified diffs; no writes |
| `--diff --apply` | Print diffs *and* persist |

```bash
hypha graft <spore-id> --as <identity-uri> [flags...]
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--as <uri>` | required | Grafter identity URI (recorded in the receipt) |
| `--space <uri>` | auto-detect | Space URI override (otherwise inferred from inbox) |
| `--verify` | `false` | Verify spore's Ed25519 signature first |
| `--no-fmt` | `false` | Skip the post-graft `mdpp.fmt` pass |
| `--dry-run` | `false` | Plan only |
| `--diff` | `false` | Render unified diffs (implies dry-run unless `--apply`) |
| `--apply` | `false` | With `--diff`: persist after printing |

Supported write kinds: `append_section`, `insert_after`, `replace_block`,
`create_file`, `add_tag`.

## `hypha graph backlinks|related|trace`

Walk the typed edges graph.

```bash
hypha graph backlinks <object-id> [--kind k1,k2] [--limit N] [--format ...]
hypha graph related   <object-id> [--kind k1,k2] [--limit N] [--format ...]
hypha graph trace     <object-id> [--kind k1,k2] [--max-depth 4] [--format ...]
```

- `backlinks` — edges pointing AT `<object-id>`.
- `related` — edges in either direction.
- `trace` — cycle-safe BFS along the chosen edge kinds, capped at
  `--max-depth`.

`--kind` defaults: all kinds for backlinks/related;
`derived_from,cites,source_ref` for trace.

## `hypha pulse`

Time-windowed signal aggregation: top initiatives, hot zones, recent
pressure, edge-kind distribution, activity counts. Cached.

```bash
hypha pulse [--space <uri>] [--window 30d] [--ttl 5m] [--format ...]
```

`--window` accepts `Nd` plus standard Go durations. `--ttl 0` forces
recompute.

## `hypha assess`

Alignment scoring. Three input shapes:

```bash
hypha assess task   --task "<text>" [--space <uri>] [--format ...]
hypha assess change --task "<text>" [--files p1,p2] [--diff-summary "<text>"] [--space <uri>] [--source <path>] [--format ...]
hypha assess pr     --task "<text>" --base <ref> [--space <uri>] [--source <path>] [--format ...]
```

Returns `{alignment, score, recommendation, matched_initiatives,
recent_pressure, hot_zone, risks}`. See
`~/.hyphae/spaces/m31labs-hyphae/concepts/initiative-alignment.md` for
the canonical category set.

## `hypha trace start|tick|done|list|history|tail|reap`

In-flight, checkpoint-emitted work logs.

```bash
hypha trace start --agent <uri> [--task <id>] [--phase <text>] [--parent <uri>] [--session <id>] [--space <uri>]
hypha trace tick <trace-id> "<message>" [--space <uri>]
hypha trace done <trace-id> [--status succeeded|failed|killed|superseded] [--link-spore <id>] [--space <uri>]
hypha trace list [--active] [--agent <uri>] [--space <uri>] [--format ...]
hypha trace history [--similar <q>] [--task <id>] [--agent <uri>] [--include-open] [--limit N] [--space <uri>]
hypha trace tail [--id <trace-id>] [--agent <uri>] [--interval 1s] [--timeout 5m] [--space <uri>]
hypha trace reap [--older-than 1h] [--space <uri>] [--format ...]
```

- `start` opens a trace; returns the new id.
- `tick` appends a checkpoint to an open trace.
- `done` closes a trace with terminal status; with `--link-spore`,
  appends the compacted work log to the spore body.
- `list` enumerates traces; `--active` narrows to open.
- `history` does FTS5 search across closed traces (methodology recall).
- `tail` polls and streams new ticks live.
- `reap` force-closes open traces whose `last_tick` exceeded
  `--older-than` (default `1h`).

## `hypha analyze`

Canopy-backed code intelligence. Cached as `analysis` objects per space.

```bash
hypha analyze <kind> [target] [--space <uri>] [--source <path>] [--diff-ref <ref>] [--max-depth N] [--refresh]
hypha analyze list   [--kind <k>] [--space <uri>] [--target-file <path>] [--format ...]
hypha analyze refresh <id> [--space <uri>] [--source <path>]
```

Kinds: `impact`, `callgraph`, `refs`, `hotspot`, `dead`, `review`.
Pass `--refresh` to ignore the cache.

## `hypha identity init|list`

```bash
hypha identity init --name <name> --authority <auth> --space <uri> [--expires 1y] [--format ...]
hypha identity list [--format ...]
```

`init` generates an Ed25519 keypair, writes the public identity file
under `<install-root>/.catalog/identities/`, and writes the private
key sidecar with mode 0600.

## `hypha cap issue`

```bash
hypha cap issue --subject <uri> --space <uri> [--permissions p1,p2] [--expires 24h] [...limits] [--format ...]
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--subject <uri>` | required | Identity URI to grant the token to |
| `--space <uri>` | required | Target space |
| `--permissions <csv>` | `memory:recall,spore:create` | Allowed actions |
| `--expires <dur>` | `24h` | Token lifetime |
| `--max-recall-results N` | `25` | `limits.max_recall_results` |
| `--max-response-tokens N` | `800` | `limits.max_response_tokens` |
| `--max-spores N` | `3` | `limits.max_spores` |
| `--max-bytes N` | `200000` | `limits.max_bytes` |

## `hypha receipts list`

Query the local audit log.

```bash
hypha receipts list [--space <uri>] [--subject <uri>] [--action <name>] [--since 24h] [--limit N] [--format ...]
```

Action examples: `spore:create`, `spore:accepted`, `spore:rejected`,
`graft`, `cap:issue`.

## `hypha mcp serve`

Stdio Model Context Protocol server. See [mcp.md](mcp.md) for the full
tool reference and client setup.

```bash
hypha mcp serve
```

## Environment

| Var | Default | Meaning |
| --- | --- | --- |
| `HYPHAE_HOME` | `~/.hyphae` | Install root |
| `HYPHAE_FORMAT` | (unset) | Default output format when `--format` is not given. Overrides TTY auto-detect; loses to explicit `--format`. |
| `NO_COLOR` | (unset) | Honored in `text` mode |

## Adjacent binary: `hypha-viz`

GoSX-based browser visualization of the graph. Separate binary
(`go install m31labs.dev/hyphae/cmd/hypha-viz`). Status: mid-flight.

```bash
hypha-viz [--addr 127.0.0.1:7777] [--root <hyphae-home>]
```
