---
mdpp: "0.1"
id: concept.caching-strategy
type: concept
space: hypha://example/seed
status: canonical
title: Caching strategy
tags: [api, caching, performance]
updated: 2026-05-28
summary: Where and how the example API caches reads.
---

# Caching strategy

Three layers: in-process LRU, Redis (shared), and HTTP `Cache-Control`
hints for downstream caches.

## Layer 1 — in-process LRU

Per-pod, ~64 MiB. Used for hot read paths that tolerate a few seconds
of staleness. Keys are tenant-scoped to prevent cross-tenant leakage.

## Layer 2 — Redis

Shared across pods in a region. TTLs set per-route, never longer than
60 seconds for anything billing-related. Invalidation goes through the
write path so reads can't observe a stale-after-mutation gap.

## Layer 3 — HTTP cache headers

Public endpoints set `Cache-Control: public, max-age=N` where N is
chosen per route. Authenticated endpoints set `private` and never
expose tenant data to shared caches.

## Interactions with rate limiting

A cache hit does not consume a rate-limit token. This is deliberate —
see [[../decisions/0001-pick-rate-limiter]] for the reasoning.

## Open questions

- Should we honor `Cache-Control: no-cache` from callers on the
  Pro tier? Currently we ignore it.
