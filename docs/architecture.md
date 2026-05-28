# Architecture

For contributors. The "what lives where" map of the Hyphae codebase.

## Repo layout

```
hyphae/
├── cmd/
│   ├── hypha/             # CLI binary (main.go is one big dispatcher)
│   └── hypha-viz/         # GoSX-based browser viz (separate binary)
├── internal/
│   ├── analyze/           # Canopy code-intel cache + lookup
│   ├── assess/            # Alignment scorer (change:assess / task / pr)
│   ├── atomicfs/          # temp+rename atomic file writes
│   ├── capability/        # Scoped capability tokens
│   ├── db/                # SQLite open + schema migration
│   ├── envelope/          # Uniform JSON envelope, four formats, TTY detect
│   ├── graft/             # Spore application engine + dry-run + diff renderer
│   ├── graph/             # Backlinks/Related/Trace queries over edges
│   ├── identity/          # Ed25519 keypair gen + identity files
│   ├── mcp/               # Model Context Protocol stdio server
│   ├── parser/            # Walk a space, extract Objects/Anchors/Edges
│   ├── pulse/             # Time-windowed signal aggregation + cache
│   ├── recall/            # FTS5 indexer + token-budgeted recall + snippets
│   ├── receipts/          # Audit log persistence + queries
│   ├── server/            # HTTP server scaffolding (for hypha-viz)
│   ├── spore/             # Validate / sign / verify / submit spores
│   ├── trace/             # In-flight work logs + Reap
│   ├── types/             # Object / Anchor / Edge / Spore / Receipt / Trace / Analysis
│   └── vizdata/           # Shared graph-query helpers for the viz binary
├── docs/                  # This documentation tree
├── skills/                # Drop-in agent skills
├── examples/              # Starter content (seed-space, sample spore)
├── web/                   # Web assets for hypha-viz
├── README.md
├── CHANGELOG.md
├── CONTRIBUTING.md
└── doc.go
```

## Package dependency direction

```
cmd/hypha  ──┐
             ├─► internal/{envelope, recall, spore, graft, trace, identity,
             │             capability, receipts, graph, pulse, assess, analyze,
             │             parser, db, mcp, atomicfs, types}
             │
cmd/hypha-viz ─► internal/{server, vizdata, recall, graph, db}

internal/mcp ────► internal/{recall, spore, graft, trace, identity, capability,
                              receipts, graph, pulse, assess, analyze, parser,
                              db, atomicfs, envelope, types}

internal/graft ──► internal/{atomicfs, types, recall (for indexing after apply)}
                  + m31labs.dev/mdpp (bounded structural edits)

internal/trace ──► internal/{atomicfs, types}

internal/spore ──► internal/{atomicfs, identity (for Verify), types}
                  + m31labs.dev/mdpp (frontmatter parsing)

internal/recall ─► internal/{db, types}
                  (FTS5 via modernc.org/sqlite — no cgo)
```

Cycle rules: no internal package imports `cmd/`. `internal/mcp` is the
union of every read+mutate surface and intentionally pulls in every
other internal package.

## Key data types (`internal/types`)

| Type | Where stored | Lifecycle |
| --- | --- | --- |
| `Object` | `objects` table (derived from .md frontmatter) | Append/update via `parser.WalkSpace` + `recall.IndexBatch` |
| `Anchor` | `anchors` table (one per heading) | Same |
| `Edge` | `edges` table | From parser (frontmatter `source_refs`, etc.) and from `graft` (`derived_from`, proposed_edges) |
| `Spore` | `<space>/inbox/agents/*.md` | Written by `spore.Submit`; status flipped by review/graft |
| `Receipt` | `receipts` table | Written by every state-changing action |
| `Trace` | `<space>/.trace/<YYYY-MM-DD>/<id>.md` | Written by `trace.Start` / `Tick` / `Done` / `Reap` |
| `Analysis` | `<space>/.analyses/*.md` | Written by `analyze.Run` |

## SQLite schema highlights

Lives at `<install-root>/.index/hyphae.db`. Migrated by
`internal/db.Open` on first use.

| Table | Role |
| --- | --- |
| `objects` | One row per typed object (id, type, space_id, file_id, status, title, tags_json, summary, updated_at) |
| `objects_fts` | FTS5 virtual table over (title, tags, summary, body). `id`, `type`, `space_id` are unindexed cols for filtering. |
| `anchors` | One row per heading (id, object_id, heading_path, start_byte, end_byte, start_line, end_line, node_kind) |
| `edges` | Typed graph edges (id, kind, src_id, dst_id, confidence, derivation, agent_source, created_by, created_at, metadata_json) |
| `receipts` | Audit log (id, space_id, subject_id, subject_kind, action, status, content_hash, identity_id, created_at, …) |
| `capabilities` | Issued tokens (id, subject, space, permissions_json, limits_json, expires_at, …) |
| `pulse_cache` | Cached `Pulse` snapshots keyed by (space, window) |

## The CLI dispatcher

`cmd/hypha/main.go` is one large file with a switch over the
top-level subcommand and one `cmd<Name>(args []string)` function per
subcommand. Each handler:

1. Parses flags via `flag.FlagSet` with `formatFlag(fs)` for `--format`.
2. Resolves the install root + opens the SQLite index when needed.
3. Calls the relevant `internal/*` package.
4. Wraps the result with `emit(command, data, format, textRenderer)`
   which routes through `internal/envelope.Emit`.

The `emit` helper means every command has the same wire shape with
zero extra boilerplate per call site.

## The envelope (`internal/envelope`)

Four formats, one shape:

- `FormatText` — caller-provided text renderer prints a human view.
- `FormatJSON` — indented full-key.
- `FormatJSONLine` — single-line full-key.
- `FormatCompact` — single-line short-key (per `keys.go` map).

`AutoDetect` picks the default: `HYPHAE_FORMAT` env → TTY check → text
or compact. The compact path rewrites keys via a single
`map[string]any` walk; unmapped keys pass through.

## The graft engine (`internal/graft`)

`Apply(...)` is the high-level entry; `ApplyWithOpts(..., ApplyOpts{DryRun})`
threads dry-run + a `Deltas[]` tracker through the handlers.

Each write-kind handler (`applyInsertWrite`, `applyCreateFile`,
`applyReplaceBlock`, `applyAddTag`) is responsible for: target
resolution, target read, new-bytes construction, parse-validation,
then `ctx.writeFile(path, newBytes)` which records the delta and
(unless dry-run) writes via `atomicfs.WriteFile`. The applyContext
struct holds both the rollback map and the delta list.

`diff.go` renders unified diffs from `FileDelta{Path, OldBytes, NewBytes}`
using a common-prefix/common-suffix shortcut — fine for the typical
graft case (single contiguous insert or section replacement).

## The MCP server (`internal/mcp`)

- `server.go` — JSON-RPC 2.0 stdio loop. Dispatches `initialize`,
  `tools/list`, `tools/call`, `ping`. `tools/call` packages the tool
  result via `render(toolName, data, opts)` which honors `format`,
  `max_tokens`, `fields`.
- `tools.go` — `buildTools` assembles 16 read tools. Each tool spec
  carries its name, terse description (single sentence to keep
  `tools/list` cheap), JSON Schema for arguments, default token
  budget, and a handler closure.
- `mutations.go` — 13 mutate tools. `hypha_graft` defaults to dry-run.
- `extras.go` — `showObject`, `assessPR`, `traceHistory` — bits that
  the CLI inlines but the MCP package re-implements to stay
  self-contained.
- `budget.go` — `render`, `projectFields`, `truncateOverBudget`.
  Truncation walks the data, finds the largest slice it can drop
  trailing items from, and re-encodes until under budget; attaches a
  `TRUNCATED` warning.

## Atomic writes (`internal/atomicfs`)

One function: `WriteFile(path, data, perm)`. Writes to
`<dir>/.<base>.tmp.<random>`, fsyncs, renames over the destination.
Used by every code path that mutates a `.md` file the user might be
reading (spore, trace, canonical edits).

## Tests

- `go test ./...` runs everything; ~17 packages green.
- `internal/recall` has a small fixture suite plus an idempotency test.
- `internal/graft` has per-write-kind tests + the dry-run isolation test.
- `internal/envelope` covers round-trip and short-key fidelity.
- `internal/mcp` has the initialize/list/call lifecycle test plus
  compact-format and truncation tests.
- `internal/atomicfs` has perms + leftover-temp coverage.
- `cmd/hypha-viz` has an HTTP endpoint smoke test.

## External dependencies

| Dep | Why |
| --- | --- |
| `m31labs.dev/mdpp` | Markdown++ parser + formatter (substrate) |
| `modernc.org/sqlite` | SQLite without cgo |
| `github.com/google/uuid` | Edge id generation in graft |
| `github.com/mattn/go-isatty` | TTY auto-detect for `FormatText` default |
| `gopkg.in/yaml.v3` | YAML for identity files + signature blocks |
| `m31labs.dev/gosx` | `hypha-viz` only (browser viz substrate) |

No cgo. Pure Go install via `go install m31labs.dev/hyphae/cmd/hypha`.

## Conventions for new code

- Every new CLI subcommand wires `--format` via `formatFlag(fs)` and
  calls `emit(...)`. No bespoke `json.Marshal` at call sites.
- Every new mutating code path writes via `atomicfs.WriteFile`.
- Every typed error gets a sentinel (`var ErrFoo = errors.New("...")`)
  so callers can `errors.Is`.
- New MCP tools go in `tools.go` (read) or `mutations.go` (write).
  Mutating tools should have a safer default than the CLI when the
  blast radius is real.
- Keep file-level comments terse and load-bearing. Skip docstrings for
  obvious helpers.
