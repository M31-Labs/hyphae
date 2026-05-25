package graft

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/odvcencio/hyphae/internal/db"
)

// openTestDB opens an in-process SQLite DB via db.Open at a temp path.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	conn, err := db.Open(path)
	if err != nil {
		t.Fatalf("openTestDB: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// makeSporeFile writes a spore .md file into spaceRoot/inbox/agents/<filename>.
func makeSporeFile(t *testing.T, spaceRoot, sporeID, agentID, status string, proposedWrites string) string {
	t.Helper()
	inboxDir := filepath.Join(spaceRoot, "inbox", "agents")
	if err := os.MkdirAll(inboxDir, 0o755); err != nil {
		t.Fatalf("mkdir inbox: %v", err)
	}
	content := `---
mdpp: "0.1"
id: ` + sporeID + `
type: spore
space: hypha://test/space
status: ` + status + `
created: ` + time.Now().UTC().Format("2006-01-02T15:04:05Z") + `

agent:
  id: ` + agentID + `
  kind: ephemeral

confidence: high

source_refs:
  - hypha://test/space/concepts/test
` + proposedWrites + `
---

# Test spore body

Some content.
`
	filename := strings.ReplaceAll(strings.TrimPrefix(sporeID, "spore."), ".", "-") + ".md"
	path := filepath.Join(inboxDir, filename)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write spore file: %v", err)
	}
	return path
}

// makeCanonicalFile writes a canonical markdown file at installRoot/spaces/test-space/<relPath>.
func makeCanonicalFile(t *testing.T, spacesDir, relPath, content string) string {
	t.Helper()
	abs := filepath.Join(spacesDir, relPath)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir canonical: %v", err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write canonical: %v", err)
	}
	return abs
}

// ─── Test 1: append_section happy path ────────────────────────────────────────

func TestApply_AppendSection(t *testing.T) {
	conn := openTestDB(t)
	installRoot := t.TempDir()
	spaceRoot := filepath.Join(installRoot, "spaces", "test-space")

	// Build canonical file with two H2 sections.
	canonicalContent := `---
mdpp: "0.1"
id: concept.target
type: concept
space: hypha://test/space
status: canonical
---

# Target Concept

## Section One

Content of section one.

## Section Two

Content of section two.
`
	canonicalFile := makeCanonicalFile(t,
		filepath.Join(installRoot, "spaces"),
		"test-space/concepts/target.md",
		canonicalContent,
	)

	sporeID := "spore.2026-05-25.test.agent01"
	agentID := "agent://test/agent"
	proposedWritesYAML := `proposed_writes:
  - kind: append_section
    target: hypha://test/space/concepts/target#section-one
    body: |
      This text was appended by the graft engine.
`

	makeSporeFile(t, spaceRoot, sporeID, agentID, "unreviewed", proposedWritesYAML)

	result, err := Apply(conn, installRoot, spaceRoot, sporeID, "identity://odvcencio")
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	// ── Assertions ────────────────────────────────────────────────────────────

	if result.SporeID != sporeID {
		t.Errorf("SporeID: want %q, got %q", sporeID, result.SporeID)
	}
	if result.NewSporeStatus != "accepted" {
		t.Errorf("NewSporeStatus: want 'accepted', got %q", result.NewSporeStatus)
	}
	if len(result.AppliedWrites) != 1 {
		t.Fatalf("AppliedWrites: want 1, got %d", len(result.AppliedWrites))
	}
	if len(result.SkippedWrites) != 0 {
		t.Errorf("SkippedWrites: want 0, got %d: %+v", len(result.SkippedWrites), result.SkippedWrites)
	}
	if len(result.TouchedFiles) != 1 || result.TouchedFiles[0] != canonicalFile {
		t.Errorf("TouchedFiles: want [%s], got %v", canonicalFile, result.TouchedFiles)
	}

	// Verify the canonical file contains the inserted text.
	newContent, err := os.ReadFile(canonicalFile)
	if err != nil {
		t.Fatalf("read canonical file: %v", err)
	}
	if !strings.Contains(string(newContent), "This text was appended by the graft engine.") {
		t.Errorf("canonical file missing inserted text; content:\n%s", newContent)
	}

	// The inserted text should appear before "## Section Two".
	insertPos := strings.Index(string(newContent), "This text was appended")
	sectionTwoPos := strings.Index(string(newContent), "## Section Two")
	if insertPos < 0 {
		t.Error("inserted text not found in canonical file")
	} else if sectionTwoPos < 0 {
		t.Error("## Section Two not found in canonical file")
	} else if insertPos > sectionTwoPos {
		t.Errorf("inserted text appears after ## Section Two (insertPos=%d, sectionTwoPos=%d)", insertPos, sectionTwoPos)
	}

	// Verify spore frontmatter updated to accepted.
	inboxPath := filepath.Join(spaceRoot, "inbox", "agents")
	entries, _ := os.ReadDir(inboxPath)
	var sporeBytes []byte
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			data, _ := os.ReadFile(filepath.Join(inboxPath, e.Name()))
			if strings.Contains(string(data), sporeID) {
				sporeBytes = data
				break
			}
		}
	}
	if sporeBytes == nil {
		t.Fatal("could not find spore file after Apply")
	}
	if !strings.Contains(string(sporeBytes), "status: accepted") {
		t.Errorf("spore status not updated to 'accepted'; frontmatter:\n%s", sporeBytes[:min(len(sporeBytes), 500)])
	}

	// Verify derived_from edge in DB.
	row := conn.QueryRow(`SELECT kind, src_id, dst_id, derivation FROM edges WHERE dst_id = ?`, sporeID)
	var kind, src, dst, derivation string
	if err := row.Scan(&kind, &src, &dst, &derivation); err != nil {
		t.Fatalf("edge not found in DB: %v", err)
	}
	if kind != "derived_from" {
		t.Errorf("edge kind: want 'derived_from', got %q", kind)
	}
	if dst != sporeID {
		t.Errorf("edge dst: want %q, got %q", sporeID, dst)
	}
	if derivation != "graft" {
		t.Errorf("edge derivation: want 'graft', got %q", derivation)
	}

	// Receipt checks.
	if result.Receipt.Action != "graft" {
		t.Errorf("Receipt.Action: want 'graft', got %q", result.Receipt.Action)
	}
	if result.Receipt.SubjectID != "identity://odvcencio" {
		t.Errorf("Receipt.SubjectID: want 'identity://odvcencio', got %q", result.Receipt.SubjectID)
	}
	if result.Receipt.SubjectKind != "human" {
		t.Errorf("Receipt.SubjectKind: want 'human', got %q", result.Receipt.SubjectKind)
	}
	if result.Receipt.Status != "accepted" {
		t.Errorf("Receipt.Status: want 'accepted', got %q", result.Receipt.Status)
	}
	if result.Receipt.ContentHash == "" {
		t.Error("Receipt.ContentHash: expected non-empty")
	}
	if result.Receipt.NextState != "canonical" {
		t.Errorf("Receipt.NextState: want 'canonical', got %q", result.Receipt.NextState)
	}
}

// ─── Test 2: missing target file → partial outcome ────────────────────────────

func TestApply_MissingTargetFile(t *testing.T) {
	// Convention documented: when ALL proposed_writes are skipped (zero applied),
	// Apply returns status="partial" (not "rejected"). "Rejected" is a separate
	// human-initiated decision operation outside the graft engine. "partial"
	// correctly signals that zero canonical writes landed, without conflating it
	// with the deliberate rejection workflow.

	conn := openTestDB(t)
	installRoot := t.TempDir()
	spaceRoot := filepath.Join(installRoot, "spaces", "test-space")

	// DO NOT create concepts/missing.md — the target file should not exist.
	if err := os.MkdirAll(filepath.Join(installRoot, "spaces", "test-space", "concepts"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	sporeID := "spore.2026-05-25.test.agent02"
	agentID := "agent://test/agent"
	proposedWritesYAML := `proposed_writes:
  - kind: append_section
    target: hypha://test/space/concepts/missing#some-section
    body: |
      Text that will never land.
`

	makeSporeFile(t, spaceRoot, sporeID, agentID, "unreviewed", proposedWritesYAML)

	result, err := Apply(conn, installRoot, spaceRoot, sporeID, "identity://odvcencio")
	if err != nil {
		t.Fatalf("Apply returned unexpected error: %v", err)
	}

	// All writes skipped → partial.
	if result.NewSporeStatus != "partial" {
		t.Errorf("NewSporeStatus: want 'partial', got %q", result.NewSporeStatus)
	}
	if len(result.AppliedWrites) != 0 {
		t.Errorf("AppliedWrites: want 0, got %d", len(result.AppliedWrites))
	}
	if len(result.SkippedWrites) != 1 {
		t.Fatalf("SkippedWrites: want 1, got %d", len(result.SkippedWrites))
	}
	skipped := result.SkippedWrites[0]
	if !strings.Contains(skipped.Reason, "target file not found") {
		t.Errorf("SkippedWrites[0].Reason: want 'target file not found', got %q", skipped.Reason)
	}
	if len(result.TouchedFiles) != 0 {
		t.Errorf("TouchedFiles: want 0, got %v", result.TouchedFiles)
	}

	// Spore status should be updated to "partial" on disk.
	inboxPath := filepath.Join(spaceRoot, "inbox", "agents")
	entries, _ := os.ReadDir(inboxPath)
	var sporeBytes []byte
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			data, _ := os.ReadFile(filepath.Join(inboxPath, e.Name()))
			if strings.Contains(string(data), sporeID) {
				sporeBytes = data
				break
			}
		}
	}
	if sporeBytes == nil {
		t.Fatal("could not find spore file after Apply")
	}
	if !strings.Contains(string(sporeBytes), "status: partial") {
		t.Errorf("spore status not updated to 'partial'; content:\n%s", sporeBytes[:min(len(sporeBytes), 500)])
	}

	// No edges written for skipped writes.
	var count int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM edges WHERE dst_id = ?`, sporeID).Scan(&count); err != nil {
		t.Fatalf("edge count query: %v", err)
	}
	if count != 0 {
		t.Errorf("edges in DB for skipped spore: want 0, got %d", count)
	}
}

// ─── Test 3: unsupported write kinds → skipped ───────────────────────────────

func TestApply_UnsupportedWriteKind(t *testing.T) {
	conn := openTestDB(t)
	installRoot := t.TempDir()
	spaceRoot := filepath.Join(installRoot, "spaces", "test-space")

	sporeID := "spore.2026-05-25.test.agent03"
	agentID := "agent://test/agent"
	proposedWritesYAML := `proposed_writes:
  - kind: replace_block
    target: hypha://test/space/concepts/target#section-one
    body: replacement
  - kind: create_file
    target: hypha://test/space/concepts/new
    body: new file
  - kind: add_tag
    target: hypha://test/space/concepts/target
    body: mytag
`
	makeSporeFile(t, spaceRoot, sporeID, agentID, "unreviewed", proposedWritesYAML)

	result, err := Apply(conn, installRoot, spaceRoot, sporeID, "identity://odvcencio")
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	if len(result.SkippedWrites) != 3 {
		t.Errorf("SkippedWrites: want 3, got %d", len(result.SkippedWrites))
	}
	for _, sw := range result.SkippedWrites {
		if !strings.Contains(sw.Reason, "unsupported write kind in v0.1.1") {
			t.Errorf("SkippedWrite %q Reason: want 'unsupported write kind in v0.1.1', got %q", sw.Kind, sw.Reason)
		}
	}
	if result.NewSporeStatus != "partial" {
		t.Errorf("NewSporeStatus: want 'partial', got %q", result.NewSporeStatus)
	}
}

// min returns the smaller of a, b.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
