---
name: hyphae
description: Use whenever recalling, citing, or contributing to project/org memory — before starting meaningful work, when the user references prior decisions/specs/plans/lessons, and after completing work (submit a spore). Hyphae is the user's federated Markdown++ knowledge graph; use the `hypha` CLI as the read/contribute surface.
triggers:
  - recalling or citing past project/org decisions, specs, plans, lessons
  - starting meaningful work in any project (read relevant context first)
  - user references prior work or asks "what did we decide about X"
  - meaningful work completes (submit a spore)
  - user asks about Hyphae itself
boundary_rules:
  - no-canonical-writes-from-ephemeral-agents
  - no-secrets-no-pii-no-prod-data
  - tight-spores-respect-context-budget
  - never-invent-spaces
---

# Skill: using Hyphae

Hyphae is the user's federated Markdown++ knowledge graph for agents. It is
the shared memory layer. Use it.

The `hypha` CLI is the read/contribute surface. Install once with
`go install m31labs.dev/hyphae/cmd/hypha@latest` (puts it at
`$(go env GOPATH)/bin/hypha`).

## The 5-second mental model

```text
~/.hyphae/                              install root
  spaces/<authority>-<name>/            one space per directory
    SPACE.md                            manifest
    concepts/   *.md                    canonical reference docs
    decisions/  NNNN-*.md               numbered ADRs
    initiatives/ *.md                   active strategic bets
    skills/     *.md                    canonical agent skills
    inbox/agents/ *.md                  unreviewed spores (your contributions)
    protocols/  *.md  schema.sql        capability surface, HTTP, SQL
```

Source of truth: Markdown++ files on disk. The CLI emits a uniform JSON
envelope; auto-detects format (text on TTY, compact when piped). Override
with `--format text|json|jsonline|compact` or `HYPHAE_FORMAT`.

## When to use this skill

| Trigger | Action |
| --- | --- |
| User asks "what did we decide about X" / "what's our convention for Y" | `hypha recall <q>` then `hypha show <id>` on the top hit |
| User starts non-trivial work | Use the `assessing-changes` skill **first**, then recall + pulse |
| User mentions a project name or initiative | `hypha recall <name>` + `hypha pulse --space <uri>` for context |
| Agent dispatched as subagent / starting any meaningful task | `hypha trace start --agent <uri>` and tick at natural boundaries (see `using-traces`) |
| You complete meaningful work | `hypha trace done` (if a trace is open) then `hypha spore submit` |
| User asks "what's running right now" | `hypha trace list --active` |
| User asks to "remember" something durable | Spore in the right space (not runtime memory) |

## Recall (read path)

Prefer `hypha recall` over filesystem grep — it's BM25-ranked,
token-budgeted, and returns body snippets with anchor citations.

```bash
# Default: auto-detect format, ≤800 tokens, summary+anchors shape
hypha recall "webhook retries"

# Human-readable, tighter budget
hypha recall "context budget" --format text --max-tokens 400

# Headline-only (one-sentence + ≤1 hit, ≤100 tokens)
hypha recall "spore" --shape headline --format text
```

If the index is stale or missing: `hypha index rebuild`.

Filesystem fallback (use only when `hypha recall` cannot answer — e.g.,
typed enumeration not in FTS yet):

```bash
ls ~/.hyphae/spaces/
find ~/.hyphae/spaces/<space>/decisions -name '*.md'
```

## Fetch one object (`hypha show`)

Once recall hands you a URI, fetch with `hypha show` — don't manually
translate URIs to filesystem paths.

```bash
hypha show concept.spore                  # full file
hypha show concept.spore --path           # resolved file path
hypha show concept.spore --json           # metadata as JSON
hypha show concept.spore --frontmatter    # just the YAML
hypha show concept.spore --body           # just the markdown body
```

## Graph queries

```bash
hypha graph backlinks concept.federation
hypha graph related concept.spore --limit 8
hypha graph backlinks <spore-id> --kind derived_from
hypha graph trace concept.lesson --kind derived_from,cites --max-depth 4
```

## Pulse

```bash
hypha pulse --window 30d --format text     # what's been happening
hypha pulse --window 7d                    # JSON (auto-detected)
hypha pulse --ttl 0                        # force recompute
```

Use it before starting meaningful work to see what the org has been
focused on.

## Alignment scoring

Three shapes: `task` (no diff yet), `change` (files + diff_summary in
hand), `pr` (derived from a git ref). Before doing non-trivial work,
ask the prior question: *should this change be made now?* See the
`assessing-changes` skill for the full workflow.

```bash
hypha assess task --task "<user's request>" --format text
hypha assess change --task "..." --files p1,p2 --diff-summary "..." --format text
```

Surface the alignment category + recommendation **before** writing code.

## Traces

Traces are in-flight, checkpoint-emitted work logs — the third leg of
the knowledge tripod alongside decisions and lessons. See the
`using-traces` skill for the full workflow.

```bash
hypha trace start --agent agent://<runtime>/<short> --task <id> --phase "<label>"
hypha trace tick <trace-id> "<checkpoint>"
hypha trace done <trace-id> --status succeeded --link-spore <spore-id>

hypha trace list --active
hypha trace history --similar "<query>"
hypha trace tail --id <trace-id>
```

## Contribute (write path) — submit a spore

When you complete meaningful work, write a spore. **Never** edit files
under `concepts/`, `decisions/`, `initiatives/`, `protocols/`, or
`skills/` directly — those are canonical and require a graft.

Authoring flow:

1. Write an mdpp file to any path with the frontmatter below.
2. Submit it (signed if you have an identity):

```bash
hypha spore submit /tmp/my-report.md --sign --as identity://<authority>/<you>
```

A reviewer (human or trusted agent) then applies via:

```bash
# Preview first
hypha graft <spore-id> --as identity://<authority>/<you> --diff

# Apply
hypha graft <spore-id> --as identity://<authority>/<you> --apply --verify
```

### Reviewing the inbox

```bash
hypha spore list --limit 20
hypha spore list --space hypha://<authority>/<name> --status unreviewed
hypha spore list --since 24h

# Flip status without grafting (queueing or formal rejection)
hypha spore accept <spore-id> --as <identity> --reason "..."
hypha spore reject <spore-id> --as <identity> --reason "..."
```

### Auditing

```bash
hypha receipts list --since 24h --limit 50
hypha receipts list --space hypha://<authority>/<name> --action graft
```

Supported write kinds in `proposed_writes`: `append_section`,
`insert_after`, `replace_block`, `create_file`, `add_tag`.

Required spore frontmatter:

```md
---
mdpp: "0.1"
id: spore.<YYYY-MM-DD>.<source>.<short-id>
type: spore
space: hypha://<authority>/<name>
status: unreviewed
created: <YYYY-MM-DD>T<HH:MM:SS>Z
agent:
  id: agent://<runtime>/session-<short>
  kind: ephemeral
  model: <model-id-if-known>
confidence: low | medium | high
source_refs:
  - hypha://<authority>/<name>/concepts/<file>
  - file://<absolute-path-touched>
# optional:
proposed_writes:
  - kind: append_section
    target: hypha://<authority>/<name>/concepts/<file>#<heading-slug>
    body: |
      …
proposed_edges:
  - kind: supports | derived_from | applies_to | blocks | cites
    src: <this spore id>
    dst: <target hypha:// URI>
    confidence: 0.0-1.0
---

# <Short report title>

## Summary
One paragraph. What was done and why.

## Findings
- Bullet, source-cited.

## Proposed canonical changes
- (optional) what to append where; reviewer will graft.

## Open questions
- (optional)
```

Body should be **tight** (target ≤ 1500 tokens, hard cap 5000). If you
have a long transcript, link to it rather than pasting.

## Boundary rules (do not violate)

- Never write under `concepts/`, `decisions/`, `initiatives/`,
  `protocols/`, `skills/`, or `SPACE.md` — propose via `proposed_writes`.
- Never store secrets, credentials, API keys, PII, or production data.
- Never bypass the spore flow because "it's simpler" — auditability
  matters.
- Never invent a space; if the right space does not exist, ask the user.

## Context budget

Hyphae's first principle: **spend tokens at index time to save tokens
at query time.** Apply that as a consumer:

- Prefer `hypha recall` over filesystem grep.
- Open files with `hypha show` only after recall points you at them.
- When citing in chat, quote a span, not a section.
- When proposing canonical changes, name the heading and write the
  delta — do not paste the full file.

## Report-back pattern

At the end of meaningful work — especially if you discovered something
durable (a lesson, a gotcha, a convention) — drop a spore. This is how
the org gets smarter from your run.

`hypha spore submit` prints the durable path on stderr:

```text
Reported back to Hyphae: /home/<you>/.hyphae/spaces/<space>/inbox/agents/<file>.md
```

Surface that line to the user verbatim.

## Identity setup (one-time)

If `hypha identity list` returns nothing, set up an identity:

```bash
hypha identity init --name <yourname> --authority <org> --space hypha://<org>/<space>
```

This generates an Ed25519 keypair, public identity file, and a
mode-0600 private key sidecar. Use the resulting `identity://...` URI
for `--as` and `--sign --as`.

## Fallback if `~/.hyphae/` does not exist

This means Hyphae knowledge isn't installed on this machine. Tell the
user, don't invent paths. Setup walkthrough:
[hyphae repo docs/getting-started.md](https://github.com/M31-Labs/hyphae/blob/main/docs/getting-started.md).
