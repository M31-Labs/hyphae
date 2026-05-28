---
mdpp: "0.1"
id: protocol.capabilities
type: protocol
space: hypha://example/seed
status: advisory
title: Capability surface
updated: 2026-05-28
---

# Capability surface

The set of named permissions any subject (identity or agent) can be
granted in this space via `hypha cap issue`.

| Permission | Scope | Notes |
| --- | --- | --- |
| `memory:recall` | Read | FTS over canonical + (optional) inbox. |
| `memory:show` | Read | Fetch one object by id. |
| `memory:graph` | Read | Walk edges. |
| `spore:create` | Write | Submit a spore to `inbox/agents/`. |
| `spore:review` | Write | Flip an unreviewed spore's status. |
| `graft` | Write | Apply a spore (requires identity ownership + space trust). |
| `cap:issue` | Write | Issue further tokens (typically owner-only). |

## Defaults by trust level

| Trust | Granted permissions |
| --- | --- |
| `trusted` | all |
| `team` | `memory:*`, `spore:create`, `spore:review`, `graft` |
| `subscribed` | `memory:recall`, `memory:show`, `spore:create` |
| `untrusted` | `memory:recall` (scoped), `spore:create` |
| `none` | nothing |

## Issuing a token

```bash
hypha cap issue \
  --subject agent://cloud/some-runner \
  --space hypha://example/seed \
  --permissions memory:recall,spore:create \
  --expires 24h
```

Tokens are persisted; revocation today is "let it expire" or wipe the
row. (Real revocation is roadmap.)
