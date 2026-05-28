# Concepts

The Hyphae mental model in one page. Read it once; the rest of the docs
assume these terms.

## Install root

`~/.hyphae/` is the convention (`HYPHAE_HOME` overrides). One root per
machine, like `~/.claude/` or `~/.config/`.

```
~/.hyphae/
  spaces/<authority>-<name>/        one space per directory
    SPACE.md                        manifest
    concepts/    *.md               canonical reference docs
    decisions/   NNNN-*.md          numbered ADRs
    initiatives/ *.md               active strategic bets
    lessons/     *.md               durable gotchas / lessons learned
    skills/      *.md               canonical agent skills
    specs/       *.md               design specs
    inbox/agents/ *.md              unreviewed spores
    protocols/   *.md  schema.sql   capability surface, HTTP, SQL
    .trace/<date>/<id>.md           in-flight work logs (gitignored)
  .index/hyphae.db                  derived SQLite index (rebuildable)
  .catalog/identities/              Ed25519 identity files + key sidecars
  .canopy/                          cached code-intel analyses
```

The directory layout is convention. The CLI cares about typed `frontmatter`
on each file, not the path.

## Space

A **space** is one Hyphae knowledge base. URI form
`hypha://<authority>/<name>`. The directory under `spaces/` is named
`<authority>-<name>`.

Spaces are independent: agents can query across all installed spaces or
scope to one with `--space`. The shared install root is what makes
"this is the org's knowledge" tractable.

## Object

Every Markdown file with valid Hyphae frontmatter is an **object** with
a stable id. Objects have a type (`concept`, `decision`, `initiative`,
`lesson`, `spec`, `plan`, `skill`, `spore`, `trace`, `analysis`,
`receipt`, `space`, …) and live at a stable URI:
`hypha://<authority>/<name>/object/<id>`.

Minimum required frontmatter:

```yaml
---
mdpp: "0.1"
id: concept.billing-webhooks
type: concept
space: hypha://myorg/knowledge
---
```

Common optional fields: `status`, `title`, `tags`, `summary`,
`updated`, `created`, `source_refs`.

## Anchor

Every heading inside an object is an **anchor** — a slug-stable
sub-URI: `hypha://<authority>/<name>/object/<id>#<heading-slug>`.
Anchors don't drift when you reorder body text the way line numbers do,
which makes them the right primary citation target.

Recall's `Snippet.Citation.Anchor` field cites the nearest preceding
heading by anchor URI plus a `line`/`end_line` range for tooling that
wants precision.

## Edge

Every typed relationship between objects is an **edge** with a
provenance trail. Edge kinds: `derived_from`, `cites`, `supports`,
`applies_to`, `blocks`, `related`, `source_ref`. Edges land in the
SQLite `edges` table; `hypha graph backlinks|related|trace` walks them.

Every edge carries `created_by`, `created_at`, `confidence`,
`agent_source`, `derivation` — so "who put this connection here, and
why" is always answerable.

## Spore

A **spore** is a contribution from an ephemeral agent (or anyone). It's
an mdpp file dropped into `<space>/inbox/agents/`. Spores are inbox-only
until a human or trusted agent **grafts** them. The agent never writes
to canonical files directly.

A spore can carry:

- `proposed_writes` — typed edits the grafter can apply.
- `proposed_edges` — new graph edges to add.
- A markdown body — the report itself (findings, summary, open questions).

Supported write kinds: `append_section`, `insert_after`, `replace_block`,
`create_file`, `add_tag`.

See [getting-started.md §8](getting-started.md#8-submit-your-first-spore)
for a worked example.

## Graft

A **graft** applies a spore. The grafter is identified by an
`identity://` URI; every applied write produces a `derived_from` edge
back to the originating spore so provenance is preserved.

Safety:

- `--dry-run` plans the graft without persisting anything.
- `--diff` renders unified diffs per touched file (implies dry-run
  unless `--apply` is also passed).
- `--verify` checks the spore's Ed25519 signature first.
- Writes go through atomic `temp+rename` so a crashed graft can't leave
  half-written files.
- The post-graft `mdpp.fmt` pass normalizes formatting.

## Trace

A **trace** is an in-flight, checkpoint-emitted work log. Decisions
answer *what was chosen*; lessons answer *what to do*; traces answer
*how it actually went*.

```bash
hypha trace start --agent agent://my-runtime/me --task task-123 --phase "writing X"
hypha trace tick <trace-id> "wired the API"
hypha trace tick <trace-id> "tests green"
hypha trace done <trace-id> --status succeeded --link-spore <spore-id>
```

Traces live at `<space>/.trace/<YYYY-MM-DD>/<trace-id>.md`, gitignored
by default. `hypha trace tail` streams ticks live. `hypha trace reap`
force-closes traces whose `last_tick` exceeded a staleness threshold.

`hypha trace history --similar "<query>"` queries closed traces by FTS —
how did we solve a similar problem before?

## Identity

An **identity** is an Ed25519 keypair plus an mdpp identity file under
`<install-root>/.catalog/identities/`. Identities sign spores and
attribute grafts.

URI forms: `identity://<authority>/<name>` (humans),
`agent://<runtime>/<short>` (agents), `service://<...>` (services).

```bash
hypha identity init --name you --authority myorg --space hypha://myorg/knowledge
hypha identity list
```

The private key is a `.key` sidecar with mode 0600. Never commit it.

## Capability

A **capability token** is a scoped permission grant — "subject X can do
Y in space Z, with these rate/size limits, until T". Capabilities are
persisted; the SQL schema enforces uniqueness.

```bash
hypha cap issue \
  --subject agent://cloud/some-runner \
  --space hypha://myorg/knowledge \
  --permissions memory:recall,spore:create \
  --expires 24h
```

## Receipt

Every state-changing action emits a **receipt** — a content-hashed
audit log entry. Spore submit, spore accept/reject, graft, identity
init, capability issue all produce receipts.

```bash
hypha receipts list --since 24h --limit 50
hypha receipts list --action graft
hypha receipts list --subject identity://myorg/you
```

Receipts are the audit trail. Combined with traces (in-flight) and
spores (contributions), they answer "what happened here and why".

## Pulse

`hypha pulse [--window 30d]` is the "what's going on in this space"
view. Time-windowed aggregation: top initiatives, hot zones, recent
pressure, edge-kind distribution, activity counts. Cached at
`pulse_cache` — pass `--ttl 0` to force recompute.

Use it before starting non-trivial work to see what the org has been
focused on.

## Assess (alignment)

`hypha assess change|task|pr` runs the alignment scorer. Matches your
proposed work against active initiatives in the space, composes pulse
signals for recent pressure, infers a path-prefix hot zone, returns
the recommendation (`proceed`, `proceed_with_extra_review`,
`review_required`).

The point: ask the prior question — *should this change be made now?*
— before writing code.

## Analyze (code intelligence)

`hypha analyze impact|callgraph|refs|hotspot|dead|review` runs
Canopy-backed code analyses against a Go project that owns a space.
Outputs are cached as `analysis` objects so the same query is cheap
to re-issue.

When `hypha assess change` is given `--space` and `--source`, it folds
cached impact analyses into the `hot_zone` — precise
`affected_symbols`/`affected_files` instead of coarse grafts-in-prefix
counts.

## Envelope

Every machine-readable response is wrapped in a uniform envelope:

```json
{
  "ok": true,
  "command": "recall",
  "hyphae_version": "0.1.8",
  "schema": 1,
  "data": { /* command-specific payload */ },
  "warnings": [],
  "errors": []
}
```

Four output formats (`text` / `json` / `jsonline` / `compact`) — see
[output-formats.md](output-formats.md).

## Federation (preview)

Multiple machines can host the same space. Subscriptions move signed
spores between install roots; identities are portable. Full federation
(signed manifests + drift detection) is on the roadmap; see
[../CHANGELOG.md](../CHANGELOG.md) for what's shipped.

## Mental model summary

```
spaces hold objects.
objects have anchors and edges.
agents recall objects, propose changes via spores, and (sometimes)
  graft those spores into canonical state.
identities sign spores and attribute grafts.
capabilities scope what subjects can do.
receipts audit every state change.
traces narrate the work as it happens.
pulse summarizes the room.
assess gates the next move.
analyze (Go) folds code intel into the same graph.
```
