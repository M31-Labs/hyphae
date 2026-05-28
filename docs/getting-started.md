# Getting started

Zero to a working Hyphae setup in ~15 minutes. We'll install the binary,
bootstrap a space, index it, run your first recall, submit and graft a
spore, and (optionally) wire your agent runtime to it.

## 0. Prerequisites

- Go 1.26+ (`go version`).
- A terminal that handles UTF-8 (Hyphae sometimes prints `…` and `→`).
- (Optional) `git` for the federation story later.

## 1. Install

```bash
go install m31labs.dev/hyphae/cmd/hypha@latest
```

This puts `hypha` at `$(go env GOPATH)/bin/hypha` — make sure that's in
your `PATH`. Verify:

```bash
hypha --help
```

## 2. Bootstrap a space

A **space** is one Hyphae knowledge base — a directory of Markdown files.
The install root lives at `~/.hyphae/` (override with `HYPHAE_HOME`).

The fastest path is to copy the seed space from this repo:

```bash
mkdir -p ~/.hyphae/spaces/myorg-knowledge
cp -r path/to/hyphae/examples/seed-space/. ~/.hyphae/spaces/myorg-knowledge/
```

Or build a minimal one by hand:

```bash
mkdir -p ~/.hyphae/spaces/myorg-knowledge/{concepts,decisions,initiatives,skills,inbox/agents,protocols}

cat > ~/.hyphae/spaces/myorg-knowledge/SPACE.md <<'EOF'
---
mdpp: "0.1"
id: space.myorg-knowledge
type: space
uri: hypha://myorg/knowledge
scope: team
visibility: private
authority: myorg
status: active
owners:
  - identity://myorg/you
trust_default: team
---

# Space: myorg/knowledge

Your org's shared memory layer.
EOF
```

Two things to know:

- The directory name is `<authority>-<name>` (matches the URI:
  `hypha://<authority>/<name>`). Many spaces live side by side under
  `~/.hyphae/spaces/`.
- The directory layout — `concepts/`, `decisions/`, `initiatives/`,
  `skills/`, `inbox/agents/`, `protocols/` — is convention, not
  hard-coded. The CLI doesn't care what you call your subdirectories;
  the typed `frontmatter` on each file is what determines its kind.

## 3. Set up your identity (one-time)

Identities sign your spores and grafts so the audit trail tells a real
story. Generate one now:

```bash
hypha identity init --name you --authority myorg --space hypha://myorg/knowledge
```

This creates `~/.hyphae/.catalog/identities/you.md` (public) and
`you.key` (private, mode 0600). Your URI is now `identity://myorg/you`.

Confirm:

```bash
hypha identity list
```

## 4. Add some real content

Drop a concept file. Hyphae's frontmatter convention:

```bash
cat > ~/.hyphae/spaces/myorg-knowledge/concepts/billing-webhooks.md <<'EOF'
---
mdpp: "0.1"
id: concept.billing-webhooks
type: concept
space: hypha://myorg/knowledge
status: canonical
title: Billing webhooks
tags: [billing, webhooks, payments]
updated: 2026-05-28
---

# Billing webhooks

The billing service emits webhook events on invoice creation, payment
success, and payment failure. Retries use exponential backoff with a
24-hour ceiling.

## Retry policy

Backoff sequence: 30s, 2m, 8m, 30m, 2h, 8h, 24h. Drop after the 7th
attempt. Failed events land in the dead-letter table for ops review.
EOF
```

The required fields are `id`, `type`, and `space`. Everything else is
optional but useful.

## 5. Index it

The index is a derived SQLite DB at `<install-root>/.index/hyphae.db`.
Rebuild it whenever you add or change files:

```bash
hypha index rebuild
```

You should see a line like
`indexed N objects, M anchors, K edges across 1 space(s)`.

## 6. Recall

```bash
hypha recall "webhook retries"
```

You'll get a hit on `concept.billing-webhooks` with a body snippet
showing the matched span and an anchor citation pointing at the
nearest heading. With piped output you'll see compact short-key JSON;
with a TTY you'll see human text. (Force human with `--format text`.)

A few useful variants:

```bash
hypha recall "webhook" --format text         # human view
hypha recall "webhook" --max-tokens 400      # tighter budget
hypha recall "webhook" --shape headline      # one-line summary
hypha recall "webhook" --format compact      # short-key JSON
```

## 7. Show one object

Once you have a URI from recall:

```bash
hypha show concept.billing-webhooks                  # full file
hypha show concept.billing-webhooks --json           # metadata as JSON
hypha show concept.billing-webhooks --body           # just markdown
hypha show concept.billing-webhooks --path           # resolved path
```

## 8. Submit your first spore

A **spore** is a contribution from an ephemeral agent (or anyone, even
you). It's an mdpp file with a typed `proposed_writes` array describing
how the canonical knowledge should change. Spores land in
`<space>/inbox/agents/` and stay there until a reviewer grafts them.

```bash
cat > /tmp/billing-retry-jitter.md <<'EOF'
---
mdpp: "0.1"
id: spore.2026-05-28.you.billing-retry-jitter
type: spore
space: hypha://myorg/knowledge
status: unreviewed
created: 2026-05-28T12:00:00Z
agent:
  id: identity://myorg/you
  kind: human
confidence: high
source_refs:
  - hypha://myorg/knowledge/concepts/billing-webhooks
proposed_writes:
  - kind: append_section
    target: hypha://myorg/knowledge/concepts/billing-webhooks#retry-policy
    body: |
      Add ±25% jitter to each backoff step so retry storms don't
      synchronize across tenants.
---

# Add jitter to webhook retry backoff

## Summary
A thundering-herd problem on Tuesday morning showed that synchronized
retries across tenants overwhelmed the downstream signing service.
Adding ±25% jitter spreads the load.
EOF

hypha spore submit /tmp/billing-retry-jitter.md --sign --as identity://myorg/you
```

Output includes a content-hashed receipt and the on-disk path of the
spore file. The audit log persisted it:

```bash
hypha receipts list --limit 5
```

## 9. Graft the spore

Spores live in the inbox until someone reviews them. Inspect first:

```bash
hypha spore list --status unreviewed --space hypha://myorg/knowledge
```

Preview what would happen (no writes, no edges, no receipt):

```bash
hypha graft spore.2026-05-28.you.billing-retry-jitter \
  --as identity://myorg/you --diff
```

You'll see a unified diff per touched file. When you're happy:

```bash
hypha graft spore.2026-05-28.you.billing-retry-jitter \
  --as identity://myorg/you --apply --verify
```

`--verify` checks the Ed25519 signature first. `--apply` upgrades the
default dry-run preview to a real write. The canonical file is
formatted via `mdpp.fmt` before persistence so your tree stays clean.

Rebuild the index so recall picks up the new content:

```bash
hypha index rebuild
hypha recall "jitter"   # → hits your newly grafted edit
```

## 10. Look around with `pulse`

```bash
hypha pulse --window 30d --format text
```

This is the "what's been happening in the org" view: top initiatives,
hot zones, recent pressure, edge-kind distribution.

## 11. Wire up your agent

Three options, from least to most powerful:

### Option A — shell out from skills

Drop the skill files from this repo's `skills/` directory into your
agent runtime's skill path:

```bash
cp skills/*.md ~/.claude/skills/      # Claude Code
# or your runtime's equivalent
```

Skills tell the agent when to reach for `hypha recall`, when to open
a trace, when to submit a spore. See [skills/README.md](../skills/README.md).

### Option B — wire MCP

```bash
hypha mcp serve            # JSON-RPC 2.0 over stdio, 29 tools
```

Configure your MCP-aware client to launch this binary on stdio. Full
setup walkthrough (Claude Code, Cursor, custom) lives at
[mcp.md](mcp.md).

### Option C — both

Skills tell the agent *when* to use Hyphae; MCP gives it the tools.
They compose. Most teams want both.

## 12. Add more spaces

Repeat steps 2–5 with a different name to add more spaces. They're
independent — agents can query across all installed spaces (or scope
to one with `--space`).

```bash
hypha spaces list
```

## What's next

- [concepts.md](concepts.md) — the mental model (space, spore, graft,
  trace, identity, capability, receipt).
- [cli-reference.md](cli-reference.md) — every command with every flag.
- [mcp.md](mcp.md) — wire `hypha mcp serve` into your agent.
- [../skills/README.md](../skills/README.md) — the drop-in skills.
- [../CONTRIBUTING.md](../CONTRIBUTING.md) — contribute back.
