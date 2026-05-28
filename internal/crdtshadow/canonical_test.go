package crdtshadow_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/gosx/crdt"
	"m31labs.dev/hyphae/internal/crdtshadow"
)

const sampleDoc = `---
mdpp: "0.1"
id: concept.test
type: concept
space: hypha://test/space
---

# Test Concept

Preamble that lives between frontmatter and first heading.

## Section A

Original A body.

## Section B

Original B body.
`

// editSection replaces the body of one heading section with a new
// body. Returns the modified file bytes. Used to simulate independent
// graft edits.
func editSection(t *testing.T, src []byte, headingSlug, newBody string) []byte {
	t.Helper()
	// Heuristic: find "## <Title>\n", replace everything until the next "##" or EOF.
	heading := "## "
	body := string(src)
	// Find the heading whose slugified text matches.
	idx := -1
	for i := 0; i < len(body); i++ {
		if strings.HasPrefix(body[i:], heading) {
			lineEnd := strings.IndexByte(body[i:], '\n')
			if lineEnd < 0 {
				lineEnd = len(body) - i
			}
			text := strings.TrimSpace(body[i+len(heading) : i+lineEnd])
			if slug := simpleSlug(text); slug == headingSlug {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		t.Fatalf("editSection: heading %q not found in:\n%s", headingSlug, body)
	}
	lineEnd := idx + strings.IndexByte(body[idx:], '\n') + 1
	// Find the next "## " (same level) or EOF.
	rest := body[lineEnd:]
	nextIdx := strings.Index(rest, "\n## ")
	end := len(body)
	if nextIdx >= 0 {
		end = lineEnd + nextIdx + 1 // include the leading newline of the next heading
	}
	return []byte(body[:lineEnd] + "\n" + newBody + "\n\n" + body[end:])
}

func simpleSlug(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	prev := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prev = false
		} else if !prev {
			b.WriteByte('-')
			prev = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func TestCanonicalSectionMergeIndependentRegions(t *testing.T) {
	// Two install roots A and B start from the same canonical file.
	rootA := t.TempDir()
	rootB := t.TempDir()

	path := filepath.Join("concepts", "test.md")
	if err := writeSeed(t, rootA, path, []byte(sampleDoc)); err != nil {
		t.Fatalf("seed A: %v", err)
	}
	if err := writeSeed(t, rootB, path, []byte(sampleDoc)); err != nil {
		t.Fatalf("seed B: %v", err)
	}

	shA, err := crdtshadow.Open(rootA, "hypha://test/space")
	if err != nil {
		t.Fatalf("open A: %v", err)
	}
	defer shA.Close()
	shB, err := crdtshadow.Open(rootB, "hypha://test/space")
	if err != nil {
		t.Fatalf("open B: %v", err)
	}
	defer shB.Close()

	absA := filepath.Join(rootA, path)
	absB := filepath.Join(rootB, path)

	// Both record the original; that brings the sections into the CRDT.
	if err := shA.RecordCanonicalWrite(absA, []byte(sampleDoc)); err != nil {
		t.Fatalf("baseline A: %v", err)
	}
	if err := shB.RecordCanonicalWrite(absB, []byte(sampleDoc)); err != nil {
		t.Fatalf("baseline B: %v", err)
	}

	// A grafts a change to "Section A" only.
	editedA := editSection(t, []byte(sampleDoc), "section-a", "ALICE'S edit to A.")
	if err := os.WriteFile(absA, editedA, 0o644); err != nil {
		t.Fatalf("write A: %v", err)
	}
	if err := shA.RecordCanonicalWrite(absA, editedA); err != nil {
		t.Fatalf("record A edit: %v", err)
	}

	// B grafts a change to "Section B" only.
	editedB := editSection(t, []byte(sampleDoc), "section-b", "BOB'S edit to B.")
	if err := os.WriteFile(absB, editedB, 0o644); err != nil {
		t.Fatalf("write B: %v", err)
	}
	if err := shB.RecordCanonicalWrite(absB, editedB); err != nil {
		t.Fatalf("record B edit: %v", err)
	}

	// Merge A into B (simulate sync).
	if err := shB.Doc().Merge(shA.Doc()); err != nil {
		t.Fatalf("merge A→B: %v", err)
	}

	// Materialize on B and verify BOTH edits survived.
	got, err := shB.MaterializeCanonical(path)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if got == nil {
		t.Fatal("materialize returned nil — expected the merged file")
	}
	s := string(got)
	if !strings.Contains(s, "ALICE'S edit to A.") {
		t.Errorf("Alice's section-A edit missing after merge:\n%s", s)
	}
	if !strings.Contains(s, "BOB'S edit to B.") {
		t.Errorf("Bob's section-B edit missing after merge:\n%s", s)
	}
	// Original bodies of the unedited sections shouldn't remain.
	if strings.Contains(s, "Original A body.") {
		t.Errorf("Alice's edit didn't replace original A body:\n%s", s)
	}
	if strings.Contains(s, "Original B body.") {
		t.Errorf("Bob's edit didn't replace original B body:\n%s", s)
	}
}

func TestCanonicalRoundTripsExactly(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join("concepts", "rt.md")
	if err := writeSeed(t, root, path, []byte(sampleDoc)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	sh, err := crdtshadow.Open(root, "hypha://test/rt")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sh.Close()

	abs := filepath.Join(root, path)
	if err := sh.RecordCanonicalWrite(abs, []byte(sampleDoc)); err != nil {
		t.Fatalf("record: %v", err)
	}

	got, err := sh.MaterializeCanonical(path)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if string(got) != sampleDoc {
		t.Errorf("round-trip differs:\nwant:\n%s\ngot:\n%s", sampleDoc, got)
	}
}

func TestMaterializeAllWritesChangedFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join("concepts", "ma.md")
	if err := writeSeed(t, root, path, []byte(sampleDoc)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	sh, err := crdtshadow.Open(root, "hypha://test/ma")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sh.Close()

	abs := filepath.Join(root, path)
	if err := sh.RecordCanonicalWrite(abs, []byte(sampleDoc)); err != nil {
		t.Fatalf("record: %v", err)
	}
	// Wipe the on-disk file; MaterializeAll should re-create it.
	if err := os.Remove(abs); err != nil {
		t.Fatalf("remove: %v", err)
	}
	changed, err := sh.MaterializeAll()
	if err != nil {
		t.Fatalf("MaterializeAll: %v", err)
	}
	if len(changed) != 1 || changed[0] != path {
		t.Errorf("MaterializeAll returned %v, want [%s]", changed, path)
	}
	got, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if string(got) != sampleDoc {
		t.Errorf("materialized file mismatch:\nwant:\n%s\ngot:\n%s", sampleDoc, got)
	}
	// Second call is a no-op (file matches CRDT).
	changed, err = sh.MaterializeAll()
	if err != nil {
		t.Fatalf("MaterializeAll second: %v", err)
	}
	if len(changed) != 0 {
		t.Errorf("second MaterializeAll should report 0 changes, got %v", changed)
	}
}

// writeSeed plants a file under <root>/<rel> with the right parent dirs.
func writeSeed(t *testing.T, root, rel string, data []byte) error {
	t.Helper()
	abs := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, data, 0o644)
}

// keep the gosx crdt import live in case tests want to inspect Docs.
var _ = crdt.Root
