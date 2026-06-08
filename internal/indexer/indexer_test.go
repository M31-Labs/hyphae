package indexer

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/hyphae/internal/db"
	"m31labs.dev/hyphae/internal/parser"
	"m31labs.dev/hyphae/internal/recall"
	"m31labs.dev/hyphae/internal/types"
)

func TestPromoteFileIndexesCanonicalObject(t *testing.T) {
	root := t.TempDir()
	spaceRoot := filepath.Join(root, "spaces", "acme-eng")
	path := filepath.Join(spaceRoot, "specs", "honesty-gate.md")
	writeFile(t, path, `---
id: spec.acme.honesty-gate
type: spec
space: hypha://acme/eng
status: canonical
tags: [honesty-gate]
---

# Honesty Gate

Backend selection must fail closed instead of silently degrading.
`)

	conn, err := db.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer conn.Close()

	p, err := PromoteFile(conn, spaceRoot, "hypha://acme/eng", path)
	if err != nil {
		t.Fatalf("PromoteFile: %v", err)
	}
	if p.Object.ID != "spec.acme.honesty-gate" {
		t.Fatalf("object id = %q", p.Object.ID)
	}

	resp, err := recall.Recall(conn, "honesty backend", 10, types.DefaultBudget())
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(resp.Hits) == 0 {
		t.Fatalf("expected promoted object to be recall-able")
	}
}

func TestPromoteFilePersistsFrontmatterMetadata(t *testing.T) {
	root := t.TempDir()
	spaceRoot := filepath.Join(root, "spaces", "acme-eng")
	path := filepath.Join(spaceRoot, "SPACE.md")
	writeFile(t, path, `---
id: space.acme-eng
type: space
uri: hypha://acme/eng
status: active
mode: maintenance
priority: 0.7
tags: [fleet, maintenance]
---

# Space: acme/eng

Quiet maintenance-mode space.
`)

	conn, err := db.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer conn.Close()

	if _, err := PromoteFile(conn, spaceRoot, "hypha://acme/eng", path); err != nil {
		t.Fatalf("PromoteFile: %v", err)
	}

	var raw string
	if err := conn.QueryRow(`SELECT metadata_json FROM objects WHERE id = ?`, "space.acme-eng").Scan(&raw); err != nil {
		t.Fatalf("select metadata_json: %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		t.Fatalf("metadata_json is not valid JSON: %v", err)
	}
	if got := metadata["mode"]; got != "maintenance" {
		t.Fatalf("mode metadata = %v, want maintenance", got)
	}
	if got := metadata["priority"]; got != 0.7 {
		t.Fatalf("priority metadata = %v, want 0.7", got)
	}
}

// TestRebuildSpacePrunesDeletedSourceFile is the regression test for the
// orphan-row bug: `index rebuild` upserted current files but never pruned
// objects whose source file was renamed/deleted, so the stale object still
// resolved via `hypha show` and surfaced in `recall` until the DB was wiped by
// hand. After this fix, RebuildSpace is authoritative — the deleted file's
// object (and its FTS row) must be gone, while the surviving object remains.
func TestRebuildSpacePrunesDeletedSourceFile(t *testing.T) {
	spaceRoot := filepath.Join(t.TempDir(), "spaces", "acme-eng")
	keepPath := filepath.Join(spaceRoot, "specs", "keep.md")
	dropPath := filepath.Join(spaceRoot, "specs", "drop.md")
	writeFile(t, keepPath, `---
id: spec.acme.keep
type: spec
space: hypha://acme/eng
status: canonical
tags: [keep]
---

# Keep Me

The honesty gate must fail closed instead of degrading.
`)
	writeFile(t, dropPath, `---
id: spec.acme.drop
type: spec
space: hypha://acme/eng
status: canonical
tags: [drop]
---

# Drop Me

A doomed honesty spec slated for deletion.
`)

	conn, err := db.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer conn.Close()

	// First rebuild: both objects present.
	if _, err := RebuildSpace(conn, spaceRoot, "hypha://acme/eng", parser.DefaultWalkOptions(), DefaultBatchSize); err != nil {
		t.Fatalf("RebuildSpace (initial): %v", err)
	}
	if !objectExists(t, conn, "spec.acme.keep") {
		t.Fatalf("expected spec.acme.keep after initial rebuild")
	}
	if !objectExists(t, conn, "spec.acme.drop") {
		t.Fatalf("expected spec.acme.drop after initial rebuild")
	}

	// Delete one source file, mirroring a rename/delete on disk.
	if err := os.Remove(dropPath); err != nil {
		t.Fatalf("remove drop.md: %v", err)
	}

	// Second rebuild must prune the orphaned object and its derived rows.
	if _, err := RebuildSpace(conn, spaceRoot, "hypha://acme/eng", parser.DefaultWalkOptions(), DefaultBatchSize); err != nil {
		t.Fatalf("RebuildSpace (after delete): %v", err)
	}

	if objectExists(t, conn, "spec.acme.drop") {
		t.Fatalf("spec.acme.drop should be pruned after its source file was deleted")
	}
	if !objectExists(t, conn, "spec.acme.keep") {
		t.Fatalf("spec.acme.keep should survive the rebuild")
	}

	// The FTS row must be gone too, or `recall` would still surface a phantom.
	if ftsRowExists(t, conn, "spec.acme.drop") {
		t.Fatalf("spec.acme.drop FTS row should be pruned")
	}
	resp, err := recall.Recall(conn, "doomed honesty spec", 10, types.DefaultBudget())
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	for _, hit := range resp.Hits {
		if strings.HasSuffix(hit.URI, "spec.acme.drop") {
			t.Fatalf("recall still surfaces pruned object spec.acme.drop (uri %q)", hit.URI)
		}
	}
}

func objectExists(t *testing.T, conn *sql.DB, id string) bool {
	t.Helper()
	var n int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM objects WHERE id = ?`, id).Scan(&n); err != nil {
		t.Fatalf("count objects %q: %v", id, err)
	}
	return n > 0
}

func ftsRowExists(t *testing.T, conn *sql.DB, id string) bool {
	t.Helper()
	var n int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM objects_fts WHERE id = ?`, id).Scan(&n); err != nil {
		t.Fatalf("count fts rows %q: %v", id, err)
	}
	return n > 0
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}
