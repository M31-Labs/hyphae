---
name: assessing-changes
description: Use BEFORE starting non-trivial work — features, refactors, bugfixes beyond a one-liner, or any code touching services/infra/internal/canonical docs. Triggers on user phrases like "let's build X", "add X", "implement X", "should I do X", "is this worth doing", "should we…", or before opening a PR with material changes. Run `hypha assess task --task "<their request>" --format text` FIRST, then surface the alignment category + recommendation verbatim before proceeding.
triggers:
  - "let's build X" / "add X" / "implement X" (anything non-trivial)
  - "should I do X" / "is this worth doing" / "should we…"
  - About to write code touching services/, infra/, internal/, or canonical docs
  - About to open a PR with material changes
boundary_rules:
  - assess-before-non-trivial-work
  - never-silently-override-review_required
  - cite-matched-initiatives
  - alignment-is-advisory-not-blocking
---

# Skill: assessing changes with Hyphae

Before doing non-trivial work, ask Hyphae the prior question: **should
this change be made now?** `hypha assess change` (or `assess task`)
scores the proposed work against active initiatives in the relevant
space, surfaces recent pressure, and recommends a next step.
Local-only, ~150–300 tokens out. Hyphae scores. The agent decides.

This skill composes with [`hyphae`](hyphae.md) (read+contribute).

---

## When to invoke (concrete triggers)

| The user says / context is | What you do FIRST |
| --- | --- |
| "let's build X" / "add X" / "implement X" (non-trivial) | `hypha assess task --task "<their request, verbatim>" --format text` |
| "should I do X" / "is this worth doing" / "should we…" | `hypha assess task --task "<X>" --format text`, surface the verdict |
| About to write code touching `services/`, `infra/`, `internal/`, public APIs, or canonical docs | `hypha assess change --task "<plan>" --files <comma-list> --diff-summary "<one-line>" --format text` |
| About to open a PR with material changes | `hypha assess change` with the PR's task + files + summary; include result in PR body |
| Just finished meaningful work | embed the assess result inside the report-back spore |

**Skip** for: typo fixes, comment tweaks, README polish, formatting-only
changes, dependency version bumps with no behavior change.

---

## What to run (exact commands)

```bash
# Smallest useful form — task-only, no diff yet.
hypha assess task --task "Add bounded backoff to billing-worker webhook retry"

# Full form — best signal when a diff is taking shape.
hypha assess change \
  --task "Add bounded backoff to billing-worker webhook retry" \
  --files services/billing-worker/retry.go,services/billing-worker/types.go \
  --diff-summary "Adds bounded exponential backoff with jitter for failed webhook delivery"

# Restrict scoring to a single space.
hypha assess change --task "Federation drift detection" --space hypha://myorg/knowledge

# Human-readable output for chat.
hypha assess change --task "..." --format text
```

Run it BEFORE writing code. Running after is just journaling.

---

## What you get back

```json
{
  "alignment": "directly_aligned",
  "score": 0.75,
  "recommendation": "proceed",
  "matched_initiatives": [
    { "id": "initiative.hyphae-federation", "score": 0.75, "reason": "Matches task description" }
  ],
  "recent_pressure": ["concept.capability-token", "concept.spore"],
  "hot_zone": { "path": "internal/server", "commits_14d": 5, "incidents_14d": 0 },
  "risks": [],
  "tokens_used": 188
}
```

### How to read it

**Alignment categories** (v0 scorer emits these four):

| Category | Score range | Meaning |
| --- | --- | --- |
| `directly_aligned` | ≥ 0.70 | Advances an active initiative head-on |
| `enabling` | ≥ 0.40 | Unlocks an active initiative |
| `adjacent` | < 0.40 | Related but not on the critical path |
| `neutral` | (no matches) | No active initiative engaged |

**Recommendations:**

| Recommendation | What it means |
| --- | --- |
| `proceed` | On-target. Go. |
| `proceed_with_extra_review` | Adjacent match. Worth a second look before committing. |
| `review_required` | No aligned initiative. Surface to the user. |

---

## How to surface to the user (verbatim templates)

Always cite at least one matched initiative with its score. Always show
the recommendation verbatim — don't paraphrase "proceed" as "go for it".

### `proceed`

```
Hyphae says this is **directly aligned** with `<initiative.id>` (score
0.75). Recommendation: proceed. Going ahead.
```

### `proceed_with_extra_review`

```
Hyphae says this is **adjacent** to `<initiative.id>` (score 0.22) —
not directly on an active push. Recommendation: proceed with extra
review. Want me to go ahead, or pause and look at the initiative first?
```

### `review_required`

```
Hyphae found **no aligned initiative** for this task. Recommendation:
review required. Should we proceed anyway, or talk through priorities first?
```

Never silently override `review_required`. Surface it, then proceed
only if the user OKs.

### Hot-zone callout

If `hot_zone.commits_14d > 5` or `hot_zone.incidents_14d > 0`:

```
…also: this lands in a hot zone (`<path>`, <N> commits / <M> incidents
in the last 14d). Be careful with rebases.
```

---

## The composing flow

```text
1. user asks for non-trivial work
2. hypha assess task --task "<request>"     ← gate decision
3. surface result to user (template above)
4. if proceeding:
   a. hypha recall <topic>                   ← "what do we already know"
   b. hypha show <object-id>                 ← fetch full text for any anchors you need
   c. hypha pulse --window 30d               ← "what's been happening here"
5. do the work
6. (optional) re-run hypha assess change with actual --files + --diff-summary
   to confirm the diff still aligns
7. hypha spore submit <report.md> --sign --as <identity>
8. (owner-side, separate session) hypha graft <spore-id> --as <identity> --apply --verify
```

Steps 2 + 3 happen BEFORE 4. If you start reading code without running
assess, you've already burned context on something Hyphae might say
"defer".

---

## Report-back pattern (embed alignment in the completion spore)

After completing meaningful work, include the assess result inside your
spore body. Future `hypha recall` runs can then replay the reasoning.

```md
## Alignment

- **Alignment:** directly_aligned (0.75)
- **Recommendation:** proceed
- **Matched initiatives:**
  - hypha://myorg/knowledge/object/initiative.foo (0.75)
- **Recent pressure cited:** concept.capability-token, concept.spore
- **Hot zone:** internal/server (5 grafts / 0 incidents / 14d)
```

---

## Boundary rules

1. **Assess before non-trivial work.** Cheap (~250 tokens), often
   surprising. Skip only for typo / comment / README polish / dep bump.
2. **Do not silently override `review_required`.** Surface it; ask
   before proceeding.
3. **Cite matched initiatives** when presenting. The `reason` field
   ("Matches task description") is part of the answer — include it.
4. **Alignment is advisory.** Hyphae scores; the client enforces.
   Don't claim "Hyphae blocked X" — Hyphae doesn't block, policies do.
5. **No canonical writes from ephemeral agents.** If the assess result
   suggests an update to a canonical doc, propose via a spore — see
   the [`hyphae`](hyphae.md) skill.

---

## Failure modes

| Symptom | Cause | Fix |
| --- | --- | --- |
| `hypha: command not found` | CLI not installed | `go install m31labs.dev/hyphae/cmd/hypha@latest` |
| `assess: ...: no such table` | Index missing | `hypha index rebuild` |
| `Alignment: neutral` on everything | No active initiatives in the space, or scoring scope too narrow | Run `hypha pulse` to check; widen `--space` |
| Always 0.10 (the floor) | Token overlap too sparse — `--task` too short | Add `--diff-summary` and meaningful keywords |
| Hot zone is wrong | Files don't share a directory prefix | Pass `--files` only for files in the same tree, or skip the flag |
