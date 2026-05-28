---
mdpp: "0.1"
id: spore.2026-05-28.example.add-jitter
type: spore
space: hypha://example/seed
status: unreviewed
created: 2026-05-28T12:00:00Z
agent:
  id: identity://example/you
  kind: human
confidence: high
source_refs:
  - hypha://example/seed/object/concept.rate-limiting
  - hypha://example/seed/object/lesson.0001-thundering-herd
proposed_writes:
  - kind: append_section
    target: hypha://example/seed/concepts/rate-limiting#behavior-on-overflow
    body: |

      **Jitter:** clients MUST add ±25% jitter to each backoff step.
      Bare exponential backoff is treated as a bug (see
      `lesson.0001-thundering-herd`).
proposed_edges:
  - kind: supports
    src: spore.2026-05-28.example.add-jitter
    dst: hypha://example/seed/object/initiative.api-resilience
    confidence: 0.9
---

# Codify the jitter requirement

## Summary

The thundering-herd incident on 2026-03-12 was caused by synchronized
retries. `concept.rate-limiting` describes the headers and overflow
behavior but never explicitly tells clients to jitter their backoff.
This spore proposes adding one paragraph under
`#behavior-on-overflow` to make the requirement explicit.

## Findings

- The lesson at `lesson.0001-thundering-herd` already captures the
  root cause and the fix.
- The initiative `initiative.api-resilience` lists "jitter everywhere"
  as an active bet.
- The concept doc is the canonical place for callers to read about
  rate limiting; not having jitter mentioned there means client SDK
  authors have to find the lesson on their own.

## Proposed canonical changes

See `proposed_writes` in the frontmatter. The append goes under the
existing `## Behavior on overflow` heading and adds one paragraph.

## Open questions

- Should the SDK rate-limiter helper enforce jitter by default
  (refuse a `withJitter(0)` call)? Probably yes; out of scope for
  this spore.
