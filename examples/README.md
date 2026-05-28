# Hyphae examples

Copy-pasteable starter content.

## `seed-space/`

A minimal but complete Hyphae space — drop it under `~/.hyphae/spaces/`
to get a working knowledge base in 30 seconds.

```bash
mkdir -p ~/.hyphae/spaces/myorg-knowledge
cp -r seed-space/. ~/.hyphae/spaces/myorg-knowledge/

# Adjust the URI in SPACE.md and the frontmatter `space:` field of
# every object to match your own authority/name. (Search/replace
# `hypha://example/seed` and `space.example-seed`.)

hypha index rebuild
hypha recall "rate limiting"
```

Includes:

| File | Type | What it teaches |
| --- | --- | --- |
| `SPACE.md` | space manifest | Trust policy, owners, scope |
| `concepts/rate-limiting.md` | concept | Canonical reference doc shape |
| `concepts/caching-strategy.md` | concept | Another concept, with cross-refs |
| `decisions/0001-pick-rate-limiter.md` | ADR | Numbered, append-only ADR shape |
| `initiatives/api-resilience.md` | initiative | Active strategic bet, what it ties together |
| `lessons/0001-thundering-herd.md` | lesson | Durable gotcha from real incident |
| `skills/team-conventions.md` | skill | Team-local skill (vs the global hyphae skills) |
| `inbox/agents/2026-05-28-example-add-jitter.md` | spore | Unreviewed contribution — graft it to see the flow end-to-end |
| `protocols/capabilities.md` | protocol | Capability surface for this space (advisory) |

## `sample-spore.md`

A standalone spore file you can `hypha spore submit` against any
space. Shows the full frontmatter shape with `proposed_writes` and
`proposed_edges`.

## Try the end-to-end flow

After copying the seed space:

```bash
# 1. Recall something.
hypha recall "thundering herd" --format text

# 2. Preview the included unreviewed spore.
hypha spore list --space hypha://example/seed --status unreviewed

hypha graft spore.2026-05-28.example.add-jitter \
  --as identity://example/you \
  --diff

# 3. Apply it (after creating an identity).
hypha identity init --name you --authority example --space hypha://example/seed
hypha graft spore.2026-05-28.example.add-jitter \
  --as identity://example/you \
  --apply

# 4. Re-index and recall — your spore's content is now canonical.
hypha index rebuild
hypha recall "jitter"
```
