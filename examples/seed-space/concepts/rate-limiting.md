---
mdpp: "0.1"
id: concept.rate-limiting
type: concept
space: hypha://example/seed
status: canonical
title: Rate limiting
tags: [api, rate-limiting, resilience]
updated: 2026-05-28
summary: How the example API enforces request rate limits.
---

# Rate limiting

The example API uses a sliding-window rate limiter keyed by the
caller's API key, with per-route overrides for hot endpoints.

## Limits

| Tier | Default per-key limit | Burst |
| --- | --- | --- |
| Free | 60 / minute | 10 |
| Pro | 600 / minute | 60 |
| Enterprise | 6000 / minute | 600 |

Hot endpoints (`/v1/payments`, `/v1/webhooks/test`) cap at 30% of
the tier limit to keep the shared write path safe.

## Headers

Every response includes:

- `X-RateLimit-Limit` — the tier ceiling.
- `X-RateLimit-Remaining` — requests left in the current window.
- `X-RateLimit-Reset` — Unix timestamp when the window rolls over.

## Behavior on overflow

Returns `429 Too Many Requests` with a `Retry-After` header. Callers
should back off **with jitter** — see
[[../lessons/0001-thundering-herd]] for why.

## See also

- [[../decisions/0001-pick-rate-limiter]] — why we picked the sliding
  window over token bucket.
- [[../initiatives/api-resilience]] — the strategic bet this concept
  serves.
