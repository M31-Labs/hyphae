---
mdpp: "0.1"
id: skill.team-conventions
type: skill
space: hypha://example/seed
status: canonical
title: Team conventions
version: "0.1"
runtimes: [generic]
triggers:
  - editing code in the example/seed codebase
  - reviewing PRs that touch services/
boundary_rules:
  - no-cache-writes-from-request-handlers
  - jitter-on-every-retry
---

# Skill: team conventions

Local conventions for the example/seed codebase. (Demonstrates that
spaces can carry their own team-local skills, alongside the global
[`hyphae`](../../skills/hyphae.md) skill.)

## Code style

- Go 1.26+. `go fmt`, `go vet`, `go test ./...` before pushing.
- No cgo.
- Errors with `errors.Is`-able sentinels at the package boundary.

## Retry policy

Every outbound call gets bounded exponential backoff **with ±25%
jitter**. See [[../lessons/0001-thundering-herd]] for why this is
non-negotiable.

## Cache invalidation

Writes invalidate the relevant Redis keys *before* writing to the
primary store, never after. This prevents the
read-after-mutation-shows-stale gap.

## PR review

- One owner approval, one peer approval for anything under `services/`.
- Diff alignment via `hypha assess change` should be in the PR
  description for any change ≥ 50 lines.
