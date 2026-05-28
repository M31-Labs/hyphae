---
mdpp: "0.1"
id: spore.YYYY-MM-DD.your-source.short-id
type: spore
space: hypha://your-authority/your-space
status: unreviewed
created: YYYY-MM-DDTHH:MM:SSZ
agent:
  id: agent://your-runtime/session-short
  kind: ephemeral                     # or human
  model: model-id-if-known            # optional
confidence: medium                    # low | medium | high
source_refs:
  - hypha://your-authority/your-space/concepts/relevant-doc
  - file:///absolute/path/touched.go
# Optional: typed write proposals the grafter can apply.
# Supported kinds: append_section, insert_after, replace_block, create_file, add_tag.
proposed_writes:
  - kind: append_section
    target: hypha://your-authority/your-space/concepts/relevant-doc#section-heading
    body: |
      Whatever new prose you want appended under that heading.
# Optional: typed edges the grafter can add to the graph.
proposed_edges:
  - kind: supports                    # supports | derived_from | applies_to | blocks | cites
    src: spore.YYYY-MM-DD.your-source.short-id
    dst: hypha://your-authority/your-space/object/initiative.something
    confidence: 0.9
---

# Short report title

## Summary

One paragraph. What was done and why.

## Findings

- Bullet, source-cited where you can.
- Quote spans, not whole sections.

## Proposed canonical changes

- (optional) What to append where. The reviewer will graft.

## Open questions

- (optional)
