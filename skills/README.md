# Hyphae agent skills

Drop-in skill files for agent runtimes. Copy these into your runtime's
skill path and the agent learns when (and how) to reach for Hyphae.

## What's here

| Skill | When to invoke |
| --- | --- |
| [`hyphae.md`](hyphae.md) | The primary skill: recall, show, spore, graft — read and contribute |
| [`assessing-changes.md`](assessing-changes.md) | Before any non-trivial work — run `hypha assess` and surface the alignment |
| [`using-traces.md`](using-traces.md) | When the agent is about to do multi-step work — open a trace, tick at boundaries, close at the end |
| [`using-mcp.md`](using-mcp.md) | When the agent is running inside an MCP-aware runtime — use the bundled tools efficiently |

## Install

### Claude Code

```bash
cp skills/*.md ~/.claude/skills/
```

Claude Code auto-loads anything in that directory.

### Per-project skills (Claude Code or compatible)

```bash
mkdir -p .claude/skills
cp skills/*.md .claude/skills/
```

Project skills override user skills with the same name.

### Cursor / other agent runtimes

Most agent runtimes either pick up `~/.<runtime>/skills/*.md`
(Claude-style) or accept skill files via a config knob. Drop these
files in the right place and the agent will read them on session
start.

### Generic LLM apps

These files are plain Markdown with YAML frontmatter. Any agent
framework that accepts "instruction documents" or "system prompts"
can ingest them — paste the body directly or include them via the
framework's preamble mechanism.

## What a Hyphae skill is

A YAML-fronted Markdown document that tells the agent:

- **When to invoke it** — triggers (regex or natural language).
- **Boundary rules** — what the agent must not do.
- **The workflow** — specific CLI commands or MCP tools to use.

The frontmatter is read by skill-aware runtimes; the body is what
the agent reads at trigger time.

## Provenance

These are repo-bundled mirrors of the canonical skills that live in
the Hyphae spec space (`~/.hyphae/spaces/m31labs-hyphae/skills/`).
When the canonical space is installed, the canonical files are the
source of truth; these repo copies are a drop-in for anyone who
doesn't have the spec space and just wants to wire `hypha` into their
agent.

Re-sync from canonical when you update:

```bash
cp ~/.hyphae/spaces/m31labs-hyphae/skills/*.md skills/
# … then restore each file's adapter frontmatter
```

## See also

- [../docs/getting-started.md](../docs/getting-started.md) — set up
  Hyphae before installing the skills.
- [../docs/mcp.md](../docs/mcp.md) — for runtimes that prefer MCP
  integration alongside skills (recommended).
- [../docs/cli-reference.md](../docs/cli-reference.md) — full CLI
  reference the skills assume.
