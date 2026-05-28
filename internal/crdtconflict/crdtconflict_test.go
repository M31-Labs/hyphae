package crdtconflict_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/hyphae/internal/crdtconflict"
	"m31labs.dev/hyphae/internal/crdtshadow"
	"m31labs.dev/hyphae/internal/types"
)

func mkdirAll(dir string) error             { return os.MkdirAll(dir, 0o755) }
func writeAll(path string, data []byte) error { return os.WriteFile(path, data, 0o644) }

const sample = `---
mdpp: "0.1"
id: c.x
type: concept
space: hypha://test/space
---

# Header

## Shared

Original shared body.
`

func writeFile(t *testing.T, root, rel string, data []byte) {
	t.Helper()
	abs := filepath.Join(root, rel)
	if err := mkdirAll(filepath.Dir(abs)); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := writeAll(abs, data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestDetectFlagsConcurrentSameKeyWrites(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	rel := filepath.Join("concepts", "x.md")
	writeFile(t, rootA, rel, []byte(sample))
	writeFile(t, rootB, rel, []byte(sample))

	shA, err := crdtshadow.Open(rootA, "hypha://test/conflict")
	if err != nil {
		t.Fatalf("open A: %v", err)
	}
	defer shA.Close()
	shB, err := crdtshadow.Open(rootB, "hypha://test/conflict")
	if err != nil {
		t.Fatalf("open B: %v", err)
	}
	defer shB.Close()

	absA := filepath.Join(rootA, rel)
	absB := filepath.Join(rootB, rel)
	// Baseline on both.
	if err := shA.RecordCanonicalWrite(absA, []byte(sample)); err != nil {
		t.Fatalf("baseline A: %v", err)
	}
	if err := shB.RecordCanonicalWrite(absB, []byte(sample)); err != nil {
		t.Fatalf("baseline B: %v", err)
	}

	// Both grafters edit the SAME section "shared" differently.
	editedA := strings.Replace(sample, "Original shared body.", "ALICE edit.", 1)
	editedB := strings.Replace(sample, "Original shared body.", "BOB edit.", 1)
	if err := shA.RecordCanonicalWrite(absA, []byte(editedA)); err != nil {
		t.Fatalf("A edit: %v", err)
	}
	if err := shB.RecordCanonicalWrite(absB, []byte(editedB)); err != nil {
		t.Fatalf("B edit: %v", err)
	}

	// Also mirror a non-conflicting receipt to confirm we don't flag it.
	_ = shA.RecordReceipt(types.Receipt{ID: "r-A", Action: "test"})
	_ = shB.RecordReceipt(types.Receipt{ID: "r-B", Action: "test"})

	// Merge A → B (simulate sync from A onto B).
	if err := shB.Doc().Merge(shA.Doc()); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if _, err := shB.Store().AppendChangesFromDoc(shB.Doc()); err != nil {
		t.Fatalf("persist: %v", err)
	}

	conflicts, err := crdtconflict.Detect(shB.Store())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(conflicts) == 0 {
		t.Fatal("expected at least one conflict on the shared section")
	}
	// Find the shared-section conflict.
	var c *crdtconflict.Conflict
	for i := range conflicts {
		if strings.Contains(conflicts[i].Tail, "shared") {
			c = &conflicts[i]
			break
		}
	}
	if c == nil {
		t.Fatalf("did not find shared-section conflict; got: %v", conflicts)
	}
	if len(c.Entries) != 2 {
		t.Errorf("conflict has %d entries, want 2", len(c.Entries))
	}

	// Resolve by keeping A's value.
	entry, err := crdtconflict.PickEntry(*c, c.Entries[0].ActorID[:4])
	if err != nil {
		t.Fatalf("PickEntry: %v", err)
	}
	if err := shB.ResolveConflict(c.Key, entry.Value); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, err := shB.Store().AppendChangesFromDoc(shB.Doc()); err != nil {
		t.Fatalf("persist after resolve: %v", err)
	}

	// After resolve, every actor's latest write should have the same
	// value (the resolver gave the chosen actor's bytes a fresh op).
	conflicts2, err := crdtconflict.Detect(shB.Store())
	if err != nil {
		t.Fatalf("Detect after resolve: %v", err)
	}
	for _, c := range conflicts2 {
		if strings.Contains(c.Tail, "shared") {
			t.Errorf("shared-section conflict should be resolved; still: %+v", c)
		}
	}
}
