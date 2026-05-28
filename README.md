# Hyphae

**Obsidian for engineering orgs** — a federated, agent-native knowledge graph
built on plain Markdown files.

Current release: **v0.1.8** ([CHANGELOG](CHANGELOG.md)) ·
[Docs](docs/README.md) · [Agent skills](skills/README.md) ·
[Examples](examples/README.md)

---

## What it is

Hyphae is a shared org memory layer. Concepts, decisions, initiatives, specs,
plans, skills, and lessons live as plain `.md` files in a directory tree your
team can clone, sync, or back up like any other repo. The `hypha` CLI gives
agents a budgeted way to read it, contribute back, and prove what they did.

| If you want… | Hyphae gives you… |
| --- | --- |
| A personal vault of linked Markdown notes (Obsidian, Logseq, Foam) | All of that, plus an agent-readable CLI, typed object graph, and FTS5 recall |
| A wiki for your team's decisions and ADRs (Notion, Confluence) | Plain files in git instead of a vendor silo, with provenance on every edge |
| Memory for your agents (vector stores, prompt-injected context) | Token-budgeted BM25 recall + multi-snippet citations, no embeddings required |
| Safe writes from autonomous agents | The spore→graft protocol: agents propose, humans/trusted-agents approve, every change carries identity + receipt |
| MCP integration for Claude Code / Cursor / custom clients | `hypha mcp serve` ships 29 read/mutate tools out of the box |

The source of truth is `.md` files on disk; the graph is derived; every edge
carries provenance.

## Why

| Problem | Hyphae's answer |
| --- | --- |
| Agents lose context between sessions | Persistent, queryable org memory in plain markdown |
| Vector stores are opaque and lossy | BM25 over SQLite FTS5 — same files, no embeddings, predictable cost |
| Agent writes corrupt your knowledge base | Spores are inbox-only until a human or trusted agent grafts them; atomic file writes; signed contributions |
| Knowledge lives in a SaaS vendor | Plain files on disk; bring your own git, bring your own backups, federate across machines |
| "What did the team decide about X?" requires asking a human | `hypha recall "X"` returns ranked hits with body snippets + anchor citations in <800 tokens |
| Multi-repo orgs have no shared memory layer | One install root (`~/.hyphae/`) holds every space; agents query across all of them |

## Drop-in setup (5 minutes)

```bash
# 1. Install the binary.
go install m31labs.dev/hyphae/cmd/hypha@latest

# 2. Bootstrap a space.
mkdir -p ~/.hyphae/spaces/myorg-knowledge
cp -r $(go env GOPATH)/pkg/mod/m31labs.dev/hyphae@*/examples/seed-space/. \
      ~/.hyphae/spaces/myorg-knowledge/
# (or copy by hand from this repo's `examples/seed-space/`)

# 3. Index it.
hypha index rebuild

# 4. Read.
hypha recall "your topic"

# 5. (Optional) drop the agent skills into your runtime.
cp skills/*.md ~/.claude/skills/   # or your runtime's skill path

# 6. (Optional) wire MCP into your agent.
# Claude Code, Cursor, custom — see docs/mcp.md.
hypha mcp serve                     # JSON-RPC 2.0 over stdio, 29 tools
```

Full walkthrough: [docs/getting-started.md](docs/getting-started.md).

## Substrate

Hyphae is built on **[Markdown++ (mdpp)](https://github.com/M31-Labs/mdpp)** —
a structural Markdown parser, renderer, formatter, and LSP server. Every
spore, decision, concept, lesson, spec, plan, skill, trace, and analysis
object in Hyphae is an mdpp document. The mdpp AST is what `hypha recall`,
`hypha show`, `hypha graft`, and the indexer all operate on. When `hypha
graft` applies a contribution, `m31labs.dev/mdpp/fmt` normalizes the
touched file in-place so canonical state stays canonical. Hyphae is a
load-bearing dogfood of mdpp.

## Pillars

- **Markdown++ as the substrate** — typed frontmatter, container directives,
  admonitions, footnotes, math, diagrams, wikilinks, and a source-preserving
  formatter, all owned by [mdpp](https://github.com/M31-Labs/mdpp). Hyphae
  uses the mdpp Go API; no other Markdown library is imported.
- **Plain `.md` files** — your knowledge stays portable, diffable, and editable
  in any tool that opens markdown.
- **Typed objects + a real graph** under the surface — concepts, decisions,
  initiatives, lessons, specs, plans, spores — addressable by stable URIs.
- **Token-budgeted recall by default** — BM25 over SQLite FTS5, no embeddings
  required to start. Agents reach for it because each call costs ~hundreds of
  tokens, not thousands.
- **Contribution protocol** — ephemeral agents submit **spores** to a space
  inbox; humans or trusted agents **graft** them into canonical knowledge.
  Every contribution carries identity, provenance, and a receipt.
- **Federated, local-first** — each space is a directory and (optionally) a
  git repo. Spaces subscribe to each other; the org gets a shared memory layer
  that survives any one machine or contributor.
- **Agent-first ergonomics** — uniform JSON envelope, four output formats
  (text / json / jsonline / compact), TTY auto-detect, `HYPHAE_FORMAT` env
  override, atomic writes, typed errors, and an MCP stdio server with 29
  token-budgeted tools.

## Documentation

- [docs/getting-started.md](docs/getting-started.md) — install → first space → first recall → first spore → first graft → MCP wiring.
- [docs/concepts.md](docs/concepts.md) — the mental model: space, object, anchor, spore, graft, trace, identity, capability, receipt.
- [docs/cli-reference.md](docs/cli-reference.md) — every command, every flag, every output.
- [docs/output-formats.md](docs/output-formats.md) — the envelope, the four formats, `HYPHAE_FORMAT`, exit codes.
- [docs/mcp.md](docs/mcp.md) — wire Hyphae into Claude Code, Cursor, or any JSON-RPC client; tool reference.
- [docs/architecture.md](docs/architecture.md) — package layout for contributors.
- [skills/README.md](skills/README.md) — agent skills you can drop into any runtime (`~/.claude/skills/`, etc.).
- [examples/seed-space/](examples/seed-space/) — copy-pasteable starter space with sample objects.
- [CONTRIBUTING.md](CONTRIBUTING.md) — how to contribute (it's spores all the way down).

## Status

**v0.1.8** — Hyphae meets the "everyday driver" bar. Source builds with
`go install` are clean; CLI output is uniform across every command;
recall returns body snippets with anchor citations; graft has a safe
`--dry-run` + `--diff` preview path; trace and spore writes are atomic;
stale traces can be reaped; and a Model Context Protocol stdio server
(`hypha mcp serve`) exposes the whole agent-facing surface as 29 tools.

Built on top of the existing v0.1.6 substrate: Ed25519 signing, graph
queries, pulse aggregation, alignment scoring (`change:assess` /
`task` / `pr`), spore submit/list/accept/reject, single-object fetch
(`show`), in-flight trace lifecycle (`start | tick | done | list |
history | tail | reap`), canopy-backed code intelligence (`analyze
impact | callgraph | refs | hotspot | dead | review`), mdpp.fmt
post-graft, GoSX-based browser viz (mid-flight).

Today you can:

- `hypha index rebuild` — walk an install root and populate a SQLite
  index (FTS5 + objects + anchors + edges) over every space.
- `hypha recall <query>` — BM25-ranked, token-budgeted full-text search
  returning a compact `summary + hits` response. Each hit carries up to
  three body snippets with anchor-URI + line-range **citations**, so
  agents can decide relevance without a follow-up `show`.
- `hypha show <id>` — fetch one object by id (or `hypha://` URI). Default
  prints the full file; `--path` / `--json` / `--frontmatter` / `--body`
  select a slice. Closes the recall→read loop without URI→path translation.
- `hypha spaces list` — enumerate installed spaces under `$HYPHAE_HOME/spaces`.
- `hypha spore submit <file> [--sign --as <id>]` — validate, optionally
  Ed25519-sign, write to inbox **atomically**, emit + persist a
  content-hashed receipt.
- `hypha spore list [--space --status --since --limit]` — enumerate inbox
  spores across installed spaces, newest first, with filters.
- `hypha spore accept|reject <spore-id> --as <id> [--reason "..."]` —
  flip status + persist a review receipt.
- `hypha graft <spore-id> --as <id> [--verify] [--dry-run] [--diff]
  [--apply]` — apply a spore's `proposed_writes` (`append_section`,
  `insert_after`, `replace_block`, `create_file`, `add_tag`) via
  bounded mdpp edits, record `derived_from` edges, update spore status
  in-place, persist the receipt. `--dry-run` previews without writing;
  `--diff` renders unified diffs per touched file (implies dry-run
  unless `--apply` is also passed).
- `hypha identity init|list` — Ed25519 keypair generation, identity files
  (mode-0600 private key sidecar), listing.
- `hypha cap issue` — scoped local capability token, persisted.
- `hypha graph backlinks|related|trace <id>` — walk the typed graph
  (cycle-safe BFS, optional kind filters).
- `hypha pulse [--window 30d]` — time-windowed signal aggregation: top
  initiatives, hot zones, recent pressure, edge-kind distribution,
  activity counts. Cached in `pulse_cache`.
- `hypha assess change|task|pr` — alignment scoring. Matches the
  proposed work against active initiatives, composes pulse signals for
  recent pressure, infers a path-prefix hot zone. Returns the JSON shape
  from `concepts/initiative-alignment.md`: alignment category, score,
  recommendation, matched initiatives, citations.
- `hypha trace reap [--older-than 1h]` — force-close open traces whose
  `last_tick` exceeded the staleness threshold (annotates body, flips
  status to `killed`).
- `hypha receipts list` — query the audit log by space, subject, action,
  time window.
- `hypha mcp serve` — Model Context Protocol stdio server with 29 tools
  (read + mutate), token-budgeted responses, compact short-key format
  for agent callers. See *Output formats* and *MCP* below.

For the browser visualization (separate binary, GoSX-based):

- `hypha-viz [--addr 127.0.0.1:7777]` — local server with a force-directed
  knowledge graph, search bar, click-to-expand neighbors, object detail
  panel. Earth-tone palette, plain Go + GoSX, no JS build step.

### Output formats

Every read/write command supports `--format text|json|compact|jsonline`:

| Format | What | Use when |
| --- | --- | --- |
| `text` | Human-readable | Reading at a terminal (default on TTY) |
| `json` | Indented full-key JSON envelope | Debugging, jq-friendly |
| `jsonline` | Single-line full-key JSON | Pipe / agent / parse-friendly |
| `compact` | Single-line short-key JSON envelope (`c`, `d`, `hs`, `sn`, `ci`, …) | Hot-path agent calls — ~7–40% smaller than json |

The default auto-detects: `text` on a TTY, `compact` when stdout is
piped. Override with `--format <name>` or the `HYPHAE_FORMAT` env var.
Schema version (envelope-shape) starts at 1; every response carries
`{ok, command, hyphae_version, schema, data, warnings, errors}` (or
the short-key equivalent).

### MCP

```bash
hypha mcp serve            # JSON-RPC 2.0 over stdio
```

The server exposes 29 tools — 16 read (`hypha_recall`, `hypha_show`,
`hypha_pulse`, `hypha_assess_task|change|pr`, `hypha_graph_*`,
`hypha_*_list`, `hypha_trace_history`, `hypha_analyze_list`) and 13
mutate (`hypha_index_rebuild`, `hypha_spore_submit|accept|reject`,
`hypha_graft` (dry-run by default; pass `apply=true` to persist,
`diff=true` to include unified diffs), `hypha_trace_start|tick|done|reap`,
`hypha_identity_init`, `hypha_cap_issue`, `hypha_analyze_run|refresh`).

Every tool accepts the same token-discipline knobs: `format`
(`jsonline`/`json`/`compact`), `max_tokens` (soft budget, list rows
trimmed when over with a `TRUNCATED` warning attached), and `fields`
(whitelist of top-level row fields for list tools). `hypha_graft`
defaults to dry-run so an MCP client must consciously opt into
persistence.

Coming next: HTTP API for cloud-agent spore submission, peer federation
(signed manifests + drift detection). Engine-backed graph rendering
(Go-via-TinyGo for the canvas) is mid-flight; see
`specs/gosx-engine-surface-completion.md`.

The canonical Hyphae space (concepts, decisions, initiatives, protocols, skills)
is installed under `~/.hyphae/spaces/m31labs-hyphae/`. The binary in this repo
operates on whatever space tree you point it at via `HYPHAE_HOME` (default
`~/.hyphae`).

## Install

```bash
go install m31labs.dev/hyphae/cmd/hypha@latest
```

Or from source:

```bash
git clone https://github.com/M31-Labs/hyphae.git
cd hyphae
go install ./cmd/hypha
```

The full first-run walkthrough — bootstrapping a space, indexing,
submitting your first spore, grafting it back, and wiring an agent —
lives at [docs/getting-started.md](docs/getting-started.md).

## Layout

Hyphae knowledge lives in a centralized install root, not in source repos:

```
~/.hyphae/                                install root (override with HYPHAE_HOME)
  spaces/<authority>-<name>/              one space per directory
    SPACE.md                              space manifest
    concepts/   *.md                      canonical concept docs
    decisions/  NNNN-*.md                 numbered ADRs
    initiatives/ *.md                     active strategic bets
    skills/     *.md                      canonical agent skills
    inbox/agents/ *.md                    unreviewed spores
    protocols/  *.md  schema.sql          capability surface, HTTP, SQL
  .index/hyphae.db                        derived SQLite index (rebuildable)
```

This repo is the **binary**. The **knowledge** lives in `~/.hyphae/` —
deliberately outside any source repo, so it can be backed up, synced, and
federated independently of any one codebase.

## Architecture

| Package | Role |
| --- | --- |
| `cmd/hypha` | CLI surface |
| `cmd/hypha-viz` | GoSX-based browser visualization (separate binary) |
| `internal/types` | Object / Anchor / Edge / Spore / Capability / Receipt |
| `internal/db` | SQLite open + embedded schema migration |
| `internal/parser` | Walk an mdpp space, extract Objects + Anchors + Edges |
| `internal/spore` | Validate spore frontmatter, sign/verify (Ed25519), write to inbox, emit receipt |
| `internal/recall` | FTS5 indexer + token-budgeted recall query with snippet/citation extraction |
| `internal/graft` | Hyphae graft engine — bounded mdpp edits, dry-run + diff renderer |
| `internal/graph` | Backlinks / Related / Trace queries over the edges table |
| `internal/pulse` | Time-windowed signal aggregation + cache |
| `internal/identity` | Ed25519 keypair gen + identity files + private-key sidecar (0600) |
| `internal/receipts` | Audit log persistence + queries |
| `internal/capability` | Scoped local capability tokens |
| `internal/trace` | In-flight trace lifecycle + Reap for stale traces |
| `internal/envelope` | Uniform JSON envelope (`text` / `json` / `jsonline` / `compact`) + TTY auto-detect |
| `internal/atomicfs` | Crash-safe temp+rename file writes for spore/trace/canonical edits |
| `internal/mcp` | Model Context Protocol stdio server — 29 tools, token-budgeted |
| `internal/analyze` | Canopy-cache for impact / callgraph / refs / hotspot / dead / review |
| `internal/assess` | Alignment scorer (`change:assess` / `task` / `pr`) |
| `internal/vizdata` | Shared graph-query helpers for the viz binary |

Built on [Markdown++ (mdpp)](https://github.com/odvcencio/mdpp): a
grammar-aware Markdown stack with byte-precise ranges, source-preserving
formatting, diagnostics, LSP, and lint — all on one AST.

## Design principles

```
No knowledge without a space.
No edge without provenance.
No federation without trust.
No contribution without identity, provenance, and receipt.
Code lives in repos. Knowledge lives in Hyphae.
Hyphae spends tokens at index time so it costs few tokens at query time.
```

## Tests

```bash
go test ./...
```

All packages green; recall package additionally validated under `-race`.

## License

MIT. See [LICENSE](LICENSE).
