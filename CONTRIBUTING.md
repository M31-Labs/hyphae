# Contributing to Hyphae

Thanks for being here.

Hyphae has two layers of contribution and they work differently:

1. **The Hyphae spec** (concepts, decisions, initiatives, lessons,
   skills) lives in the `m31labs/hyphae` space, dogfooded as Hyphae
   knowledge. Contributions go through the **spore → graft** protocol
   the project itself describes.
2. **The Hyphae binary** (this repo — Go code, CLI, MCP server, viz)
   accepts contributions through standard GitHub PRs.

The two layers cross-pollinate: a code PR may also touch the spec, and
a spec spore may motivate a code change.

## Spec contributions (spore → graft)

If you want to propose a new concept, change an existing decision,
add a lesson, or refine a skill — **don't** open a PR against the
spec files in `~/.hyphae/spaces/m31labs-hyphae/`. They're canonical
and require a graft from a trusted identity.

Instead:

1. Install Hyphae (`go install m31labs.dev/hyphae/cmd/hypha@latest`)
   and the spec space (`git clone …` under `~/.hyphae/spaces/`).
2. Set up an identity:
   `hypha identity init --name <you> --authority <yourorg> --space hypha://m31labs/hyphae`
3. Write your proposal as a spore — see
   [examples/sample-spore.md](examples/sample-spore.md) for the shape.
4. Submit:
   `hypha spore submit /tmp/your-spore.md --sign --as identity://<yourorg>/<you>`
5. The Hyphae owners review and graft (or reject) it.

See [skills/hyphae.md](skills/hyphae.md) for the full contribution
flow and the boundary rules.

## Binary contributions (PRs)

For Go code, the CLI surface, the MCP server, the envelope, or any
other repo code:

### Before you start

- Read [docs/architecture.md](docs/architecture.md) for the package
  layout.
- Run `hypha assess task --task "<what you want to do>" --format text`
  if you have the spec installed. The alignment scorer often surfaces
  prior work or shifts your scope.
- Open an issue or draft PR to socialize anything non-trivial.

### Workflow

```bash
git clone https://github.com/M31-Labs/hyphae.git
cd hyphae

# Make a branch.
git checkout -b yourname/feature-X

# Hack, then verify.
go build ./...
go test ./...
go vet ./...

# If you have buckley installed, use it for commits.
buckley commit --yes --minimal-output

# Otherwise, git commit + open a PR.
```

### Conventions for new code

These mirror what the existing code does — match the house style.

- **Output goes through the envelope.** New CLI subcommands wire
  `--format` via `formatFlag(fs)` and call `emit(command, data, format, textRenderer)`.
  No bespoke `json.Marshal` at call sites.
- **Mutating code paths use `atomicfs.WriteFile`.** Don't call
  `os.WriteFile` directly when writing a file the user might be
  reading concurrently (spores, traces, canonical .md).
- **Typed sentinel errors** at the package boundary. Callers should
  be able to `errors.Is(err, pkg.ErrFoo)`.
- **MCP tools** — read tools go in `internal/mcp/tools.go`, mutating
  tools in `internal/mcp/mutations.go`. Mutating MCP tools should
  default to a safer mode than the CLI when the blast radius is real
  (see `hypha_graft` for the dry-run-by-default precedent).
- **Comments**: terse, load-bearing. Skip docstrings for obvious
  helpers. Don't reference the current task / PR / issue — those
  belong in the PR description and rot in code.
- **Tests**: `go test ./...` should stay green. Add tests with new
  code; they don't have to be exhaustive, but the failure modes that
  motivated the change should be regression-tested.

### What about the spec?

If your code change adds a new capability that deserves a concept
doc or a decision, the change comes in two steps:

1. **Code PR** — ship the implementation against this repo.
2. **Spec spore** — submit a spore against `hypha://m31labs/hyphae`
   describing the new capability, the rationale, and any boundary
   rules. Owners graft it into the canonical spec.

The code PR mentions the spore id in its description so the two
threads stay tied.

## CHANGELOG entries

The [CHANGELOG](CHANGELOG.md) follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). For
non-trivial PRs, include a draft CHANGELOG entry in your PR
description; the maintainer will refine and merge it on release.

## Questions

- Open a GitHub issue for bugs, ideas, or questions about the binary.
- For spec questions, the right channel is a spore against the
  `m31labs/hyphae` space — see above.

Welcome aboard.
