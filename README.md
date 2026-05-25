# Hyphae

A federated Markdown++ knowledge graph for agents.

Hyphae is the shared memory layer ephemeral and persistent agents report into
and reason from. The source of truth is `.md` files on disk; the graph is
derived; every edge carries provenance. Spaces federate. Contributions land
as **spores** in a space inbox and become canonical via an explicit **graft**
operation.

## Status

**v0.1** — design phase complete, first cut of the binary works end-to-end.

Today you can:

- `hypha index rebuild` — walk a Hyphae install root and populate a SQLite
  index over every space's mdpp files.
- `hypha recall <query>` — BM25-ranked, token-budgeted full-text search
  returning a compact `summary + anchors` response.
- `hypha spore submit <file>` — validate, write to a space inbox, and emit
  a content-hashed receipt.
- `hypha cap issue` — issue a scoped local capability token.

Coming next (v0.1.1+): Ed25519 signing, graft engine, HTTP API, alignment
(`change:assess`), pulse aggregation.

The full spec — concepts, decisions, initiatives, protocols, integrations,
skills — lives at `~/.hyphae/spaces/m31labs-hyphae/`, dogfooded as a Hyphae
space. See [`concepts/install-layout.md`](https://github.com/M31-Labs/hyphae/wiki)
for the `~/.hyphae/` convention.

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

This repo is the **binary**. The **knowledge** lives in `~/.hyphae/`. See
[`decisions/0006-install-layout.md`](https://github.com/M31-Labs/hyphae/wiki)
for the rationale.

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
