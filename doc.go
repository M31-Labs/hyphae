// Package hyphae is the root of the Hyphae knowledge-graph binary —
// "Obsidian for engineering orgs": plain Markdown++ files, a typed
// object graph, BM25 recall, a spore→graft contribution protocol, and
// an MCP server agents can plug into.
//
// The substrate is Markdown++ (m31labs.dev/mdpp). The CLI binary lives
// at ./cmd/hypha; the MCP stdio server runs as the `mcp serve`
// subcommand of that same binary; the browser visualization is the
// separate ./cmd/hypha-viz binary.
//
// Start here:
//
//   - README.md                  — what Hyphae is, drop-in setup.
//   - docs/getting-started.md    — zero to working in 15 minutes.
//   - docs/concepts.md           — the mental model.
//   - docs/cli-reference.md      — every command, every flag.
//   - docs/output-formats.md     — text / json / jsonline / compact.
//   - docs/mcp.md                — wire `hypha mcp serve` into agents.
//   - docs/architecture.md       — package layout for contributors.
//
// The canonical Hyphae spec (concepts, decisions, initiatives, protocols)
// lives in the m31labs/hyphae space, dogfooded as a Hyphae space. When
// installed under ~/.hyphae/spaces/m31labs-hyphae/, it is the source of
// truth for the project's own conventions.
package hyphae
