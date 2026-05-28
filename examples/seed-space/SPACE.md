---
mdpp: "0.1"
id: space.example-seed
type: space
uri: hypha://example/seed
scope: team
visibility: private
authority: example
status: active
created: 2026-05-28
owners:
  - identity://example/you
trust_default: team
---

# Space: example/seed

A minimal Hyphae space to demonstrate the directory layout, frontmatter
shapes, and the spore→graft contribution flow.

## Topology

```text
hypha://example/seed
├── concepts/          first-class concepts referenced by everything else
├── decisions/         numbered, append-only architectural decisions
├── initiatives/       active strategic bets the spec serves
├── lessons/           durable gotchas from real incidents
├── skills/            team-local agent skills
├── inbox/agents/      ephemeral agent spores (unreviewed contributions)
└── protocols/         capability surface, schemas
```

## Trust policy

| Subject kind | Default trust | Default permissions |
| --- | --- | --- |
| owner | trusted | full |
| team member | team | recall, assess, propose, graft |
| subscribed agent | subscribed | recall, assess, spore:create |
| ephemeral cloud agent | untrusted | recall (scoped), assess, spore:create |
| anonymous | none | none |

## Reading order

1. This SPACE.md
2. `concepts/rate-limiting.md`
3. `decisions/0001-pick-rate-limiter.md`
4. `initiatives/api-resilience.md`
5. `lessons/0001-thundering-herd.md`
