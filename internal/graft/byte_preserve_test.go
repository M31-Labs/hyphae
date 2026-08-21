package graft

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyWithOptsAppendPreservesUnrelatedBytesAndPreviewMatchesApply(t *testing.T) {
	conn := openTestDB(t)
	installRoot := t.TempDir()
	spaceRoot := filepath.Join(installRoot, "spaces", "test-space")

	// This document is intentionally not formatter-clean. Graft owns only the
	// insertion point and must not unwrap prose, normalize blank lines, or move
	// any pre-existing byte as a side effect of the append.
	canonicalContent := "---\n" +
		"mdpp: \"0.1\"\n" +
		"id: concept.target\n" +
		"type: concept\n" +
		"space: hypha://test/space\n" +
		"status: canonical\n" +
		"---\n\n" +
		"# Target\n\n" +
		"## Section One\n\n" +
		"A deliberately wrapped paragraph whose existing\n" +
		"line break is outside this graft's authority.\n\n\n" +
		"## Section Two\n\n" +
		"-  existing list spacing stays exact\n"
	canonicalFile := makeCanonicalFile(t,
		filepath.Join(installRoot, "spaces"),
		"test-space/concepts/target.md",
		canonicalContent,
	)
	before := []byte(canonicalContent)

	sporeID := "spore.2026-08-21.test.byte-preserve"
	makeSporeFile(t, spaceRoot, sporeID, "agent://test/agent", "unreviewed", `proposed_writes:
  - kind: append_section
    target: hypha://test/space/concepts/target#section-one
    body: |
      Preserved append marker.
`)

	dry, err := ApplyWithOpts(conn, installRoot, spaceRoot, sporeID, "identity://odvcencio", ApplyOpts{DryRun: true})
	if err != nil {
		t.Fatalf("dry-run append: %v", err)
	}
	if len(dry.Deltas) != 1 {
		t.Fatalf("dry-run deltas = %d, want 1", len(dry.Deltas))
	}
	if !bytes.Equal(dry.Deltas[0].OldBytes, before) {
		t.Fatal("dry-run preimage differs from exact canonical bytes")
	}

	insertAt := bytes.Index(before, []byte("## Section Two"))
	if insertAt < 0 {
		t.Fatal("test fixture lacks second section")
	}
	expected := make([]byte, 0, len(before)+len("Preserved append marker.\n"))
	expected = append(expected, before[:insertAt]...)
	expected = append(expected, []byte("Preserved append marker.\n")...)
	expected = append(expected, before[insertAt:]...)
	if !bytes.Equal(dry.Deltas[0].NewBytes, expected) {
		t.Fatalf("dry-run changed bytes outside insertion range\nwant: %q\n got: %q", expected, dry.Deltas[0].NewBytes)
	}

	onDisk, err := os.ReadFile(canonicalFile)
	if err != nil {
		t.Fatalf("read after dry-run: %v", err)
	}
	if !bytes.Equal(onDisk, before) {
		t.Fatal("dry-run mutated canonical file")
	}

	applied, err := ApplyWithOpts(conn, installRoot, spaceRoot, sporeID, "identity://odvcencio", ApplyOpts{})
	if err != nil {
		t.Fatalf("apply append: %v", err)
	}
	if len(applied.Deltas) != 1 {
		t.Fatalf("apply deltas = %d, want 1", len(applied.Deltas))
	}
	onDisk, err = os.ReadFile(canonicalFile)
	if err != nil {
		t.Fatalf("read after apply: %v", err)
	}
	if !bytes.Equal(onDisk, dry.Deltas[0].NewBytes) {
		t.Fatal("apply bytes differ from dry-run preview")
	}
	if !bytes.Equal(applied.Deltas[0].NewBytes, dry.Deltas[0].NewBytes) {
		t.Fatal("apply delta differs from dry-run delta")
	}
}
