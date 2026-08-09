package graft

import (
	"os"
	"strings"
	"testing"
)

// TestApply_TolerantWriteFormats replays the exact author mistakes that
// produced silent partial grafts on 2026-08-09: markdown hashes on the
// payload heading, a "content" payload key, and a ".md" suffix on the
// target URI. All three must now apply.
func TestApply_TolerantWriteFormats(t *testing.T) {
	conn := openTestDB(t)
	installRoot := t.TempDir()
	spaceRoot := installRoot + "/spaces/test-space"

	canonicalContent := `---
mdpp: "0.1"
id: concept.tolerant
type: concept
space: hypha://test/space
status: canonical
---

# Title

## Remaining work

### Phase 1 - optional backend

Original phase one body.

### Phase 2 - distributed capabilities

Original phase two body.
`
	canonicalFile := makeCanonicalFile(t,
		installRoot+"/spaces",
		"test-space/concepts/tolerant.md",
		canonicalContent,
	)

	sporeID := "spore.2026-08-09.test.tolerant-writes"
	proposedWritesYAML := `proposed_writes:
  - kind: append_section
    target: hypha://test/space/concepts/tolerant.md
    heading: "### Phase 1 - optional backend"
    content: |
      Signed off body via hash heading and content key.
  - kind: create_file
    target: hypha://test/space
    path: concepts/created-via-content.md
    content: |
      ---
      mdpp: "0.1"
      id: concept.created-via-content
      type: concept
      space: hypha://test/space
      status: canonical
      ---

      # Created via content

      Body from the content key.
`
	makeSporeFile(t, spaceRoot, sporeID, "agent://test/tolerant", "unreviewed", proposedWritesYAML)

	result, err := Apply(conn, installRoot, spaceRoot, sporeID, "identity://odvcencio")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(result.AppliedWrites) != 2 {
		t.Fatalf("AppliedWrites = %d, want 2 (skipped: %+v)", len(result.AppliedWrites), result.SkippedWrites)
	}

	got, _ := os.ReadFile(canonicalFile)
	text := string(got)
	bodyPos := strings.Index(text, "Signed off body via hash heading")
	nextHeading := strings.Index(text, "### Phase 2")
	if bodyPos < 0 || nextHeading < 0 || bodyPos > nextHeading {
		t.Fatalf("append landed at %d relative to next heading %d:\n%s", bodyPos, nextHeading, text)
	}
	if strings.Contains(text, "## ###") {
		t.Fatalf("hash heading corrupted a created section:\n%s", text)
	}

	created, err := os.ReadFile(installRoot + "/spaces/test-space/concepts/created-via-content.md")
	if err != nil {
		t.Fatalf("created file missing: %v", err)
	}
	if !strings.Contains(string(created), "Body from the content key.") {
		t.Fatalf("create_file content payload not written:\n%s", created)
	}
}

// TestApply_FragmentAnchorIsSlugified accepts heading-text fragments in
// target URIs, not only pre-slugged anchors.
func TestApply_FragmentAnchorIsSlugified(t *testing.T) {
	conn := openTestDB(t)
	installRoot := t.TempDir()
	spaceRoot := installRoot + "/spaces/test-space"

	canonicalContent := `---
mdpp: "0.1"
id: concept.frag
type: concept
space: hypha://test/space
status: canonical
---

# Title

## Open Decisions

Decision body.

## References

Refs.
`
	canonicalFile := makeCanonicalFile(t,
		installRoot+"/spaces",
		"test-space/concepts/frag.md",
		canonicalContent,
	)

	sporeID := "spore.2026-08-09.test.fragment-slug"
	proposedWritesYAML := `proposed_writes:
  - kind: append_section
    target: "hypha://test/space/concepts/frag#Open Decisions"
    body: |
      Appended through a raw heading fragment.
`
	makeSporeFile(t, spaceRoot, sporeID, "agent://test/frag", "unreviewed", proposedWritesYAML)

	result, err := Apply(conn, installRoot, spaceRoot, sporeID, "identity://odvcencio")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(result.AppliedWrites) != 1 {
		t.Fatalf("AppliedWrites = %d, want 1 (skipped: %+v)", len(result.AppliedWrites), result.SkippedWrites)
	}
	got, _ := os.ReadFile(canonicalFile)
	text := string(got)
	bodyPos := strings.Index(text, "Appended through a raw heading fragment.")
	refsPos := strings.Index(text, "## References")
	if bodyPos < 0 || bodyPos > refsPos {
		t.Fatalf("fragment append landed at %d relative to references %d:\n%s", bodyPos, refsPos, text)
	}
}
