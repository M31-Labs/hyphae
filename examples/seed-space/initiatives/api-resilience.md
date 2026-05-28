---
mdpp: "0.1"
id: initiative.api-resilience
type: initiative
space: hypha://example/seed
status: active
title: API resilience push (2026 Q2)
tags: [api, resilience, initiative]
created: 2026-04-01
target_quarter: 2026-Q2
owners:
  - identity://example/you
---

# Initiative: API resilience push

Goal: take the public API from "mostly works" to "graceful under load
and partial failure" by the end of Q2.

## Why

Three Sev-2 incidents in March all traced back to the same pattern:
synchronized retries from large clients overwhelmed a downstream
dependency. We need to harden every retry / rate / cache path so a
single bad client (or a thundering herd) can't take down the rest.

## In scope

- Rate limiting algorithm + behavior (see [[../concepts/rate-limiting]]).
- Caching strategy + invalidation gaps (see [[../concepts/caching-strategy]]).
- Retry policy across all client SDKs (jitter, ceiling, dead-letter).
- Observability for hot paths (queue depth, retry backoff distribution).

## Out of scope

- New endpoints / features.
- Multi-region failover (separate initiative, 2026-Q3).

## Active bets

- **Jitter everywhere.** Every retry path gets bounded exponential
  backoff *with* jitter. See [[../lessons/0001-thundering-herd]].
- **Per-route limits, not just per-tier.** Hot endpoints get tighter
  limits than the tier defaults.
- **Cache hits are free.** Cache-served reads don't consume rate-limit
  tokens.

## Done when

- Zero synchronized-retry Sev-2 incidents for 60 consecutive days.
- p99 latency on hot endpoints holds under 2× normal at 5× normal QPS.
- All client SDKs ship the jittered retry helper.
