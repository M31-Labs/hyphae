# Changelog

All notable changes to Hyphae are recorded here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
versioning is [SemVer](https://semver.org/spec/v2.0.0.html) (pre-1.0 — the
surface can break between minors).

## [0.1.8] — 2026-05-28

The "everyday driver" release: standardize the agent-facing surface,
add snippets+citations to recall, make graft safe to preview, harden
trace/spore writes, and ship a Model Context Protocol stdio server.

### Added

- **Uniform output envelope** (`internal/envelope`). Every `hypha`
  subcommand now emits the same wire shape:
  `{ok, command, hyphae_version, schema, data, warnings, errors}`.
  Envelope schema version starts at `1`.
- **Four output formats** via `--format text|json|compact|jsonline`:
  - `text` — human-readable (default on TTY)
  - `json` — full-key indented (debug / jq-friendly)
  - `jsonline` — single-line full-key (parse-friendly)
  - `compact` — single-line short-key (`c`/`d`/`hs`/`sn`/`ci`/…) for
    hot-path agent calls; ~7–40% smaller than `json` per response.
- **`HYPHAE_FORMAT` env var** to override the auto-detected default
  (TTY → `text`, pipe → `compact`).
- **Recall snippets + citations**. Each `hypha recall` hit now carries
  up to three body snippets, each with a `Citation` that points at the
  nearest preceding markdown heading (`hypha://…#slug`) plus a 1-indexed
  line range. Budget-aware: snippets get trimmed before whole hits when
  approaching `--max-tokens`.
- **`hypha graft --dry-run`** — plan the apply without persisting any
  file, spore-status, or edge changes. Returns `Deltas[]` so callers can
  inspect what *would* have happened.
- **`hypha graft --diff`** — render a unified diff per touched file
  (implies `--dry-run` unless `--apply` is also passed).
- **`hypha trace reap [--older-than 1h]`** — force-close open traces
  whose `last_tick` exceeded the staleness threshold; annotates the body
  with the reaping reason, flips status to `killed`, persists atomically.
- **Atomic file writes** (`internal/atomicfs`) for every spore, trace,
  and canonical-file mutation. Write-to-`<path>.tmp.<pid>` → fsync →
  rename. A crashed mid-write can no longer corrupt agent-visible state.
- **Typed sentinel errors**: `spore.ErrDuplicate`,
  `spore.ErrInvalidStatusTransition`, `trace.ErrStale`.
  Callers can `errors.Is` instead of string-matching.
- **`hypha mcp serve`** — Model Context Protocol stdio server (JSON-RPC
  2.0). Exposes **29 tools** out of the box:
  - 16 read: `hypha_recall`, `hypha_show`, `hypha_pulse`,
    `hypha_assess_task|change|pr`, `hypha_spaces_list`,
    `hypha_graph_backlinks|related|trace`, `hypha_identity_list`,
    `hypha_spore_list`, `hypha_trace_list|history`,
    `hypha_receipts_list`, `hypha_analyze_list`.
  - 13 mutate: `hypha_index_rebuild`,
    `hypha_spore_submit|accept|reject`, `hypha_graft` (dry-run by
    default; `apply=true` to persist, `diff=true` to include unified
    diffs), `hypha_trace_start|tick|done|reap`, `hypha_identity_init`,
    `hypha_cap_issue`, `hypha_analyze_run|refresh`.
- **Token-aware MCP responses**. Every tool accepts
  `format` (`jsonline`/`json`/`compact`), `max_tokens` (soft cap;
  list-shaped responses drop trailing rows and attach a `TRUNCATED`
  warning when the budget kicks in), and `fields` (whitelist of
  top-level row fields for list tools).

### Changed

- **Recall response shape**: `anchors` field renamed to `hits`. The old
  name collided with mdpp's per-heading anchor IDs (which are now used
  *inside* the new `Citation.Anchor` field). Each `Hit` may carry
  `Snippets[]`.
- **Default CLI format**: text on a TTY, `compact` JSON when stdout is
  piped. Previously was always raw JSON (per command) or required an
  explicit flag.
- **`internal/graft.Apply`** now delegates to
  `ApplyWithOpts(..., ApplyOpts{DryRun: bool})`. The single-arg form is
  preserved as a wrapper.
- **Spore submit / review writes** route through `atomicfs.WriteFile`.
- **Trace writes** (open / tick / done / reap) route through
  `atomicfs.WriteFile`.
- **mdpp.fmt post-graft pass** is skipped in dry-run mode.

### Fixed

- **`graft create_file`** previously wrote-then-validated, leaving
  malformed files on disk when validation failed. Now parses first,
  writes only on pass.
- **`graft` rollback paths** for `applyInsertWrite`, `applyReplaceBlock`,
  and `applyAddTag` no longer race against the (now atomic) primary
  write — re-parse failures abort *before* any byte hits disk.
- **`hypha-viz` search endpoint** updated for the recall
  `anchors → hits` rename (and the page-level JS).

### Migration

- Go consumers of `recall.Response` should rename `Anchors` → `Hits`
  (each `Hit` shares the old `Anchor` fields plus a new `Snippets[]`).
- JSON consumers of `hypha recall --format json` should expect the
  `hits` key instead of `anchors`.
- Direct callers of `graft.Apply` keep working unchanged; new code
  wanting dry-run should call `ApplyWithOpts`.

### Earlier versions

For 0.1.0 → 0.1.7, see `git log`. v0.1.7 was a dependency bump; v0.1.6
landed the trace lifecycle + canopy `analyze` + spore review +
post-graft `mdpp.fmt`; v0.1.4–v0.1.5 added in-flight traces and
`hypha analyze`; earlier minors built up the read / contribute / review
/ audit loop on top of mdpp.
