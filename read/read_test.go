package read_test

import (
	"testing"

	"m31labs.dev/hyphae/read"
)

// TestOpenIndex verifies that OpenIndex can connect to the live user index
// and that Spaces returns at least one row. The index must already exist
// (run `hypha index rebuild` if it doesn't).
func TestOpenIndex(t *testing.T) {
	path, err := read.DefaultIndexPath()
	if err != nil {
		t.Fatalf("DefaultIndexPath: %v", err)
	}

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
	path, err := read.DefaultIndexPath()
	if err != nil {
		t.Fatalf("DefaultIndexPath: %v", err)
	}

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
	path, err := read.DefaultIndexPath()
	if err != nil {
		t.Fatalf("DefaultIndexPath: %v", err)
	}

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
	path, err := read.DefaultIndexPath()
	if err != nil {
		t.Fatalf("DefaultIndexPath: %v", err)
	}

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
	path, err := read.DefaultIndexPath()
	if err != nil {
		t.Fatalf("DefaultIndexPath: %v", err)
	}

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
	path, err := read.DefaultIndexPath()
	if err != nil {
		t.Fatalf("DefaultIndexPath: %v", err)
	}

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
	path, err := read.DefaultIndexPath()
	if err != nil {
		t.Fatalf("DefaultIndexPath: %v", err)
	}

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
