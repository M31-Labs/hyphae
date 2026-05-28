---
mdpp: "0.1"
id: decision.0001-pick-rate-limiter
type: decision
space: hypha://example/seed
status: accepted
title: "0001 — Pick a rate limiter algorithm"
tags: [api, rate-limiting, decision]
created: 2026-05-15
decided_by:
  - identity://example/you
supersedes: []
---

# 0001 — Pick a rate limiter algorithm

## Context

We need a rate limiter for the public API. The two main contenders:

- **Token bucket** — classic, well-understood, allows controlled
  bursts.
- **Sliding window** — smoother, more accurate, slightly more memory.

## Decision

**We use a sliding window**, with a tunable burst allowance per tier.

## Consequences

- Slightly higher memory per active key (window timestamps vs. a
  single counter).
- More accurate rate enforcement at the boundary of windows — fewer
  user-visible "I'm getting limited even though I waited" complaints.
- Cache hits don't consume tokens (deliberate; lowers the effective
  cost of mostly-read clients).

See [[../concepts/rate-limiting]] for the live behavior and
[[../initiatives/api-resilience]] for the strategic context.
