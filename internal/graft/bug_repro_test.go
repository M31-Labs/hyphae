package graft

import (
	"os"
	"strings"
	"testing"
)

// TestApply_AppendSection_NonLastSection_BodyPayload is the specific regression
// test for the "silently drops body content" bug: append_section targeting a
// heading that is NOT the last section, using the heading+body payload pattern
// (no URI fragment). The body must land within the target section, before the
// next sibling heading, and the heading must not be duplicated.
func TestApply_AppendSection_NonLastSection_BodyPayload(t *testing.T) {
	conn := openTestDB(t)
	installRoot := t.TempDir()
	spaceRoot := installRoot + "/spaces/test-space"

	// Three-section document; target is section TWO (not the last).
	canonicalContent := `---
mdpp: "0.1"
id: concept.three-sections
type: concept
space: hypha://test/space
status: canonical
---

# Title

## Section One

Content of section one.

## Section Two

Content of section two.

## Section Three

Content of section three.
`
	canonicalFile := makeCanonicalFile(t,
		installRoot+"/spaces",
		"test-space/concepts/three-sections.md",
		canonicalContent,
	)

	sporeID := "spore.2026-06-10.cedar.non-last-body"
	// heading+body (not heading+content), no fragment in target URI.
	proposedWritesYAML := `proposed_writes:
  - kind: append_section
    target: hypha://test/space/concepts/three-sections
    heading: "Section Two"
    body: |
      Body injected into section two via heading payload.
`
	makeSporeFile(t, spaceRoot, sporeID, "agent://cedar/test", "unreviewed", proposedWritesYAML)

	result, err := Apply(conn, installRoot, spaceRoot, sporeID, "identity://odvcencio")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(result.AppliedWrites) != 1 {
		t.Fatalf("AppliedWrites: want 1, got %d (skipped: %+v)", len(result.AppliedWrites), result.SkippedWrites)
	}

	newContent, _ := os.ReadFile(canonicalFile)
	s := string(newContent)

	bodyPos := strings.Index(s, "Body injected into section two")
	sec3Pos := strings.Index(s, "## Section Three")
	sec2Count := strings.Count(s, "## Section Two")

	if bodyPos < 0 {
		t.Fatalf("body was silently dropped; file:\n%s", s)
	}
	if sec3Pos >= 0 && bodyPos > sec3Pos {
		t.Errorf("body landed after ## Section Three (bodyPos=%d, sec3Pos=%d);\nfile:\n%s", bodyPos, sec3Pos, s)
	}
	if sec2Count != 1 {
		t.Errorf("## Section Two duplicated: found %d occurrences, want 1;\nfile:\n%s", sec2Count, s)
	}
}

// TestApply_MultiWrite_NonOverlapping ensures that two proposed_writes in a
// single spore that target DIFFERENT sections of the same file both land
// correctly even though the first write shifts the byte offsets of the second.
// The engine must recompute (or re-read) the file after each write rather than
// applying all writes against the original byte snapshot.
func TestApply_MultiWrite_NonOverlapping(t *testing.T) {
	conn := openTestDB(t)
	installRoot := t.TempDir()
	spaceRoot := installRoot + "/spaces/test-space"

	canonicalContent := `---
mdpp: "0.1"
id: concept.multi
type: concept
space: hypha://test/space
status: canonical
---

# Multi Write Target

## Alpha

Alpha content.

## Beta

Beta content.

## Gamma

Gamma content.
`
	canonicalFile := makeCanonicalFile(t,
		installRoot+"/spaces",
		"test-space/concepts/multi.md",
		canonicalContent,
	)

	sporeID := "spore.2026-06-10.cedar.multi-write"
	// Two non-overlapping writes: Alpha section and Gamma section.
	proposedWritesYAML := `proposed_writes:
  - kind: append_section
    target: hypha://test/space/concepts/multi#alpha
    body: |
      Appended to alpha.
  - kind: append_section
    target: hypha://test/space/concepts/multi#gamma
    body: |
      Appended to gamma.
`
	makeSporeFile(t, spaceRoot, sporeID, "agent://cedar/test", "unreviewed", proposedWritesYAML)

	result, err := Apply(conn, installRoot, spaceRoot, sporeID, "identity://odvcencio")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(result.AppliedWrites) != 2 {
		t.Fatalf("AppliedWrites: want 2, got %d (skipped: %+v)", len(result.AppliedWrites), result.SkippedWrites)
	}
	if result.NewSporeStatus != "accepted" {
		t.Errorf("NewSporeStatus: want 'accepted', got %q", result.NewSporeStatus)
	}

	newContent, _ := os.ReadFile(canonicalFile)
	s := string(newContent)

	alphaAppendPos := strings.Index(s, "Appended to alpha.")
	betaPos := strings.Index(s, "## Beta")
	gammaAppendPos := strings.Index(s, "Appended to gamma.")

	if alphaAppendPos < 0 {
		t.Fatalf("first write (alpha append) was dropped; file:\n%s", s)
	}
	if gammaAppendPos < 0 {
		t.Fatalf("second write (gamma append) was dropped; file:\n%s", s)
	}
	// Alpha append must precede Beta heading.
	if betaPos >= 0 && alphaAppendPos > betaPos {
		t.Errorf("alpha append landed after ## Beta (alphaPos=%d, betaPos=%d);\nfile:\n%s",
			alphaAppendPos, betaPos, s)
	}
	// Both writes present, order preserved.
	if alphaAppendPos > gammaAppendPos {
		t.Errorf("alpha append is after gamma append in final document;\nfile:\n%s", s)
	}
}

// TestApply_MultiWrite_Overlapping tests that two replace_block writes targeting
// the SAME section in one spore do NOT silently corrupt the document.
// The engine must either error loudly or recompute ranges correctly.
// The acceptance criterion is: no silent corruption (either both writes apply
// with correct final state, or the second is clearly skipped/errored).
func TestApply_MultiWrite_Overlapping(t *testing.T) {
	conn := openTestDB(t)
	installRoot := t.TempDir()
	spaceRoot := installRoot + "/spaces/test-space"

	canonicalContent := `---
mdpp: "0.1"
id: concept.overlap
type: concept
space: hypha://test/space
status: canonical
---

# Overlap Test

## Target Section

Original content.

## Other Section

Untouched.
`
	canonicalFile := makeCanonicalFile(t,
		installRoot+"/spaces",
		"test-space/concepts/overlap.md",
		canonicalContent,
	)

	sporeID := "spore.2026-06-10.cedar.overlap-writes"
	// Two replace_block writes both targeting the same section anchor.
	proposedWritesYAML := `proposed_writes:
  - kind: replace_block
    target: hypha://test/space/concepts/overlap#target-section
    body: |
      First replacement body.
  - kind: replace_block
    target: hypha://test/space/concepts/overlap#target-section
    body: |
      Second replacement body.
`
	makeSporeFile(t, spaceRoot, sporeID, "agent://cedar/test", "unreviewed", proposedWritesYAML)

	result, err := Apply(conn, installRoot, spaceRoot, sporeID, "identity://odvcencio")
	if err != nil {
		// A fatal error is acceptable (loud failure > silent corruption).
		t.Logf("Apply returned error (acceptable for overlap detection): %v", err)
		return
	}

	newContent, _ := os.ReadFile(canonicalFile)
	s := string(newContent)

	// The critical invariant: the document must NOT be silently corrupted.
	// "Silently corrupted" means: one replacement body is missing BUT the Apply
	// reported success with 2 applied writes.
	firstPresent := strings.Contains(s, "First replacement body.")
	secondPresent := strings.Contains(s, "Second replacement body.")

	if result.NewSporeStatus == "accepted" && len(result.AppliedWrites) == 2 {
		// Both reported as applied — both bodies must actually be in the file.
		if !firstPresent || !secondPresent {
			t.Errorf("silent corruption: Apply reported 2 applied writes but file is missing a body;\nfirstPresent=%v secondPresent=%v;\nfile:\n%s",
				firstPresent, secondPresent, s)
		}
	}
	// If only 1 applied, that's partial — acceptable (not silent corruption).
	// If 0 applied, that's also acceptable (loud skip).
	// The only unacceptable outcome is 2 reported applied + missing content.
	t.Logf("applied=%d skipped=%d status=%s firstPresent=%v secondPresent=%v",
		len(result.AppliedWrites), len(result.SkippedWrites), result.NewSporeStatus,
		firstPresent, secondPresent)
}
