package read_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"m31labs.dev/hyphae/internal/db"
	"m31labs.dev/hyphae/read"
)

// skipIfNoIndex skips the test if the default hyphae index does not exist on
// disk. This keeps live-index tests from failing in CI where no index exists.
func skipIfNoIndex(t *testing.T) string {
	t.Helper()
	path, err := read.DefaultIndexPath()
	if err != nil {
		t.Skipf("DefaultIndexPath: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("live index not present (%s): %v", path, err)
	}
	return path
}

// TestOpenIndex verifies that OpenIndex can connect to the live user index
// and that Spaces returns at least one row. The index must already exist
// (run `hypha index rebuild` if it doesn't).
func TestOpenIndex(t *testing.T) {
	path := skipIfNoIndex(t)

	r, err := read.OpenIndex(path)
	if err != nil {
		t.Fatalf("OpenIndex(%q): %v", path, err)
	}
	t.Cleanup(func() { _ = r.Close() })

	spaces, err := r.Spaces()
	if err != nil {
		t.Fatalf("Spaces(): %v", err)
	}
	if len(spaces) == 0 {
		t.Fatal("Spaces() returned empty slice; expected ≥1 space")
	}
	t.Logf("found %d space(s), first id=%s", len(spaces), spaces[0].ID)
}

// TestObjects_ByType verifies that filtering by type works and that the
// overstory spec object (type="spec") exists in the index.
func TestObjects_ByType(t *testing.T) {
	path := skipIfNoIndex(t)

	r, err := read.OpenIndex(path)
	if err != nil {
		t.Fatalf("OpenIndex: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	objs, err := r.Objects(read.ObjectFilter{Type: "spec"})
	if err != nil {
		t.Fatalf("Objects(type=spec): %v", err)
	}
	if len(objs) == 0 {
		t.Fatal("Objects(type=spec) returned empty; expected ≥1 spec object")
	}
	t.Logf("found %d spec object(s), first id=%s title=%q", len(objs), objs[0].ID, objs[0].Title)
}

// TestEdges verifies that Edges returns without error for any object id we can
// find. An empty result is acceptable; the contract is no error.
func TestEdges(t *testing.T) {
	path := skipIfNoIndex(t)

	r, err := read.OpenIndex(path)
	if err != nil {
		t.Fatalf("OpenIndex: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	// Grab a real object id to query.
	objs, err := r.Objects(read.ObjectFilter{})
	if err != nil {
		t.Fatalf("Objects: %v", err)
	}
	if len(objs) == 0 {
		t.Skip("no objects in index; skipping edge test")
	}

	edges, err := r.Edges(objs[0].ID)
	if err != nil {
		t.Fatalf("Edges(%q): %v", objs[0].ID, err)
	}
	t.Logf("Edges(%q) returned %d edge(s)", objs[0].ID, len(edges))
}

// TestSpores verifies that Spores returns without error.
func TestSpores(t *testing.T) {
	path := skipIfNoIndex(t)

	r, err := read.OpenIndex(path)
	if err != nil {
		t.Fatalf("OpenIndex: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	spores, err := r.Spores(read.SporeFilter{})
	if err != nil {
		t.Fatalf("Spores(): %v", err)
	}
	t.Logf("Spores() returned %d spore(s)", len(spores))
}

// TestReceipts verifies that Receipts returns without error.
func TestReceipts(t *testing.T) {
	path := skipIfNoIndex(t)

	r, err := read.OpenIndex(path)
	if err != nil {
		t.Fatalf("OpenIndex: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	receipts, err := r.Receipts(read.ReceiptFilter{})
	if err != nil {
		t.Fatalf("Receipts(): %v", err)
	}
	t.Logf("Receipts() returned %d receipt(s)", len(receipts))
}

// TestPulse verifies that Pulse returns without error for the "30d" window.
func TestPulse(t *testing.T) {
	path := skipIfNoIndex(t)

	r, err := read.OpenIndex(path)
	if err != nil {
		t.Fatalf("OpenIndex: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	p, err := r.Pulse("30d")
	if err != nil {
		t.Fatalf("Pulse(30d): %v", err)
	}
	t.Logf("Pulse(30d): window=%s activity=%+v", p.Window, p.Activity)
}

// TestActiveTraces verifies that ActiveTraces returns without error.
// An empty slice is fine when there are no active traces.
func TestActiveTraces(t *testing.T) {
	path := skipIfNoIndex(t)

	r, err := read.OpenIndex(path)
	if err != nil {
		t.Fatalf("OpenIndex: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	traces, err := r.ActiveTraces()
	if err != nil {
		t.Fatalf("ActiveTraces(): %v", err)
	}
	t.Logf("ActiveTraces() returned %d trace(s)", len(traces))
}

// TestHermetic_ReadIndex builds a fixture index in a temp dir using db.Open
// (read-write, to create the schema and insert rows), then reopens it via
// read.OpenIndex (read-only) and asserts that actual field values come back
// correctly. This test is fully self-contained and requires no live index.
func TestHermetic_ReadIndex(t *testing.T) {
	dir := t.TempDir()
	// Replicate the default layout so that OpenIndex can derive hyphaeRoot.
	indexDir := filepath.Join(dir, ".index")
	if err := os.MkdirAll(indexDir, 0o755); err != nil {
		t.Fatalf("mkdir .index: %v", err)
	}
	dbPath := filepath.Join(indexDir, "hyphae.db")

	// Create a space directory so spaceRootPath returns a non-empty value.
	spaceDir := filepath.Join(dir, "spaces", "acme-test")
	if err := os.MkdirAll(spaceDir, 0o755); err != nil {
		t.Fatalf("mkdir spaces/acme-test: %v", err)
	}

	// Bootstrap the schema with the read-write Open.
	rw, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}

	// Insert a space row.
	_, err = rw.Exec(`INSERT INTO spaces(id, uri, authority, scope, visibility, root_path, trust_default, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"acme/test", "hypha://acme/test", "acme", "test", "private", "", "low",
		time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("insert space: %v", err)
	}

	// Insert a file row (needed as FK for object).
	_, err = rw.Exec(`INSERT INTO files(id, space_id, path, content_hash, byte_size, parsed_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		"file-1", "acme/test", "docs/hello.md", "abc123", 42,
		time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("insert file: %v", err)
	}

	// Insert an object row.
	_, err = rw.Exec(`INSERT INTO objects(id, type, space_id, file_id, status, title, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"obj-1", "spec", "acme/test", "file-1", "active", "Hello World",
		time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("insert object: %v", err)
	}

	// Insert an edge row.
	_, err = rw.Exec(`INSERT INTO edges(id, kind, src_id, dst_id, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		"edge-1", "references", "obj-1", "obj-1",
		time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("insert edge: %v", err)
	}

	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}

	// Now open read-only via the package under test.
	r, err := read.OpenIndex(dbPath)
	if err != nil {
		t.Fatalf("read.OpenIndex: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	// --- Spaces ---
	spaces, err := r.Spaces()
	if err != nil {
		t.Fatalf("Spaces(): %v", err)
	}
	if len(spaces) != 1 {
		t.Fatalf("Spaces(): got %d spaces, want 1", len(spaces))
	}
	if spaces[0].ID != "acme/test" {
		t.Errorf("Space.ID = %q, want %q", spaces[0].ID, "acme/test")
	}
	if spaces[0].Authority != "acme" {
		t.Errorf("Space.Authority = %q, want %q", spaces[0].Authority, "acme")
	}
	// RootPath should have been derived from hyphaeRoot/spaces/acme-test.
	if spaces[0].RootPath != spaceDir {
		t.Errorf("Space.RootPath = %q, want %q", spaces[0].RootPath, spaceDir)
	}
	t.Logf("Space: id=%s rootPath=%s", spaces[0].ID, spaces[0].RootPath)

	// --- Objects ---
	objs, err := r.Objects(read.ObjectFilter{Type: "spec"})
	if err != nil {
		t.Fatalf("Objects(): %v", err)
	}
	if len(objs) != 1 {
		t.Fatalf("Objects(): got %d, want 1", len(objs))
	}
	if objs[0].Title != "Hello World" {
		t.Errorf("Object.Title = %q, want %q", objs[0].Title, "Hello World")
	}
	if objs[0].SpaceID != "acme/test" {
		t.Errorf("Object.SpaceID = %q, want %q", objs[0].SpaceID, "acme/test")
	}
	t.Logf("Object: id=%s title=%q", objs[0].ID, objs[0].Title)

	// --- Edges ---
	edges, err := r.Edges("obj-1")
	if err != nil {
		t.Fatalf("Edges(): %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("Edges(): got %d, want 1", len(edges))
	}
	if edges[0].Kind != "references" {
		t.Errorf("Edge.Kind = %q, want %q", edges[0].Kind, "references")
	}
	t.Logf("Edge: id=%s kind=%s", edges[0].ID, edges[0].Kind)
}
