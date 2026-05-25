# Hyphae

A federated Markdown++ knowledge graph for agents (and the humans they work with).

Hyphae is an efficient, OSS knowledge base — usable as a drop-in for the kinds
of tools teams reach for when they want a personal or shared vault of plain
`.md` notes, but built from the ground up to be read and written by agents as
a first-class concern.

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

The source of truth is `.md` files on disk; the graph is derived; every edge
carries provenance.

## Status

**v0.1.1** — the contribution loop closes end-to-end.

Today you can:

- `hypha index rebuild` — walk a Hyphae install root and populate a SQLite
  index over every space's mdpp files.
- `hypha recall <query>` — BM25-ranked, token-budgeted full-text search
  returning a compact `summary + anchors` response.
- `hypha spore submit <file>` — validate, write to a space inbox, emit a
  content-hashed receipt, and persist it to the audit log.
- `hypha graft <spore-id> --as <identity>` — apply a spore's
  `proposed_writes` to canonical files using bounded mdpp edits, record
  `derived_from` edges, update spore status in-place, persist the receipt.
- `hypha identity init` / `hypha identity list` — Ed25519 keypair
  generation and listing.
- `hypha cap issue` — scoped local capability token, persisted with a
  matching receipt.
- `hypha receipts list` — query the audit log by space, subject, action,
  time window.

Coming next (v0.1.2+): Ed25519 spore signing on submit + verification on
intake, mdpp.fmt after graft, additional `proposed_write` kinds
(`replace_block`, `create_file`, `add_tag`), HTTP API, alignment
(`change:assess`), pulse aggregation.

The canonical Hyphae space (concepts, decisions, initiatives, protocols, skills)
is installed under `~/.hyphae/spaces/m31labs-hyphae/`. The binary in this repo
operates on whatever space tree you point it at via `HYPHAE_HOME` (default
`~/.hyphae`).

## Install

```bash
go install github.com/M31-Labs/hyphae/cmd/hypha@latest
```

Or from source:

```bash
git clone git@github.com:M31-Labs/hyphae.git
cd hyphae
go install ./cmd/hypha
```

## Quick start

```bash
# 1. Install the spec space (knowledge), if you don't already have one
mkdir -p ~/.hyphae/spaces/m31labs-hyphae
# … place mdpp files under it; see "Layout" below

# 2. Index it
hypha index rebuild

# 3. Search
hypha recall "spore submission" --format text
hypha recall "context budget" --shape headline --format text

# 4. Submit a spore
cat > /tmp/my-report.md <<'EOF'
---
mdpp: "0.1"
id: spore.2026-05-25.local.example
type: spore
space: hypha://m31labs/hyphae
status: unreviewed
created: 2026-05-25T00:00:00Z
agent:
  id: agent://local/me
  kind: human
confidence: medium
source_refs:
  - hypha://m31labs/hyphae/concepts/spore
---

# Example report

## Summary
Hello, Hyphae.
EOF

hypha spore submit /tmp/my-report.md
```

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
| `internal/types` | Object / Anchor / Edge / Spore / Capability / Receipt |
| `internal/db` | SQLite open + embedded schema migration |
| `internal/parser` | Walk an mdpp space, extract Objects + Anchors + Edges |
| `internal/spore` | Validate spore frontmatter, write to inbox, emit receipt |
| `internal/recall` | FTS5 indexer + token-budgeted recall query |
| `internal/capability` | Local (unsigned, v0.1) capability tokens |

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
