---
mdpp: "0.1"
id: lesson.0001-thundering-herd
type: lesson
space: hypha://example/seed
status: canonical
title: "0001 — Synchronized retries cause thundering herds"
tags: [api, retries, incident]
incident_date: 2026-03-12
severity: sev-2
---

# 0001 — Synchronized retries cause thundering herds

## What happened

At 09:01 UTC on 2026-03-12, the signing service went down for 90s
for a routine deploy. Every client SDK in the wild retried after
exactly 5 seconds (the default backoff was a constant). When the
service came back, ~12k clients hit it simultaneously, exhausting the
connection pool and extending the incident to 27 minutes.

## Root cause

Constant retry backoff (no jitter). All clients computed the same
"next retry" timestamp, so when the underlying service recovered,
the load spike on retry was higher than steady-state.

## The fix

Two-part:

1. **Add jitter at the client.** Every backoff step has ±25% jitter.
   Existing exponential backoff (30s, 2m, 8m, …) becomes
   (30s ±25%, 2m ±25%, …).
2. **Add a per-route load shed at the server.** Hot endpoints (the
   signing path was the worst) cap at 30% of the tier limit so a
   thundering herd can't drown the shared write path.

## What to do next time

- Any new retry path needs jitter from day one. **Bare exponential
  backoff is a bug.**
- Any new hot endpoint needs a tighter per-route limit, not just the
  tier default.

## See also

- [[../concepts/rate-limiting]] — current limits + behavior.
- [[../initiatives/api-resilience]] — the broader Q2 push this lesson
  feeds.
