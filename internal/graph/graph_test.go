package graph

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"m31labs.dev/hyphae/internal/db"
	"m31labs.dev/hyphae/internal/types"
)

// insertTestEdge inserts a minimal edge row for testing.
func insertTestEdge(t *testing.T, conn *sql.DB, id, kind, src, dst, now string) {
	t.Helper()
	_, err := conn.Exec(
		`INSERT INTO edges(id, kind, src_id, dst_id, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, kind, src, dst, now,
	)
	if err != nil {
		t.Fatalf("insert edge %q: %v", id, err)
	}
}

// TestBacklinksAndForwardLinks verifies basic directional edge queries.
func TestBacklinksAndForwardLinks(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()

	now := time.Now().UTC().Format(time.RFC3339)

	// Graph: A→B (related), A→C (related), D→A (cites)
	insertTestEdge(t, conn, "e1", "related", "obj.A", "obj.B", now)
	insertTestEdge(t, conn, "e2", "related", "obj.A", "obj.C", now)
	insertTestEdge(t, conn, "e3", "cites", "obj.D", "obj.A", now)

	// ForwardLinks from A: should return 2 (A→B and A→C).
	fwd, err := ForwardLinks(conn, "obj.A", nil, 50)
	if err != nil {
		t.Fatalf("ForwardLinks: %v", err)
	}
	if len(fwd) != 2 {
		t.Errorf("ForwardLinks(A): want 2, got %d", len(fwd))
	}
	for _, n := range fwd {
		if n.Edge.SrcID != "obj.A" {
			t.Errorf("ForwardLinks: expected SrcID=obj.A, got %q", n.Edge.SrcID)
		}
	}

	// Backlinks to A: should return 1 (D→A).
	back, err := Backlinks(conn, "obj.A", nil, 50)
	if err != nil {
		t.Fatalf("Backlinks: %v", err)
	}
	if len(back) != 1 {
		t.Errorf("Backlinks(A): want 1, got %d", len(back))
	}
	if len(back) > 0 && back[0].Edge.SrcID != "obj.D" {
		t.Errorf("Backlinks: expected SrcID=obj.D, got %q", back[0].Edge.SrcID)
	}

	// Kind filter: ForwardLinks(A, [cites]) should return 0 (A has no cites edges outward).
	fwdFiltered, err := ForwardLinks(conn, "obj.A", []types.EdgeKind{types.EdgeCites}, 50)
	if err != nil {
		t.Fatalf("ForwardLinks (filtered): %v", err)
	}
	if len(fwdFiltered) != 0 {
		t.Errorf("ForwardLinks(A, cites): want 0, got %d", len(fwdFiltered))
	}

	// Kind filter: Backlinks(A, [cites]) should return 1 (D→A is a cites edge).
	backFiltered, err := Backlinks(conn, "obj.A", []types.EdgeKind{types.EdgeCites}, 50)
	if err != nil {
		t.Fatalf("Backlinks (filtered): %v", err)
	}
	if len(backFiltered) != 1 {
		t.Errorf("Backlinks(A, cites): want 1, got %d", len(backFiltered))
	}
}

// TestRelatedDedupe verifies that Related dedupes on (kind, endpoint) but
// preserves distinct (kind, endpoint) pairs across different kinds.
//
// Dedupe policy: two entries are considered duplicate only if they share both
// the same EdgeKind AND the same endpoint. A→B via "related" and A→B via
// "cites" are DIFFERENT relations and both appear. This mirrors how citation
// graphs work: the same document can have multiple relationship types to the
// same target.
func TestRelatedDedupe(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()

	now := time.Now().UTC().Format(time.RFC3339)

	// Graph: A→B (related), A→C (related), D→A (cites), A→B (cites).
	// Four distinct (kind, endpoint) pairs from A's perspective:
	//   (related, obj.B), (related, obj.C), (cites, obj.B), (cites, obj.A)
	insertTestEdge(t, conn, "e1", "related", "obj.A", "obj.B", now)
	insertTestEdge(t, conn, "e2", "related", "obj.A", "obj.C", now)
	insertTestEdge(t, conn, "e3", "cites", "obj.D", "obj.A", now)
	insertTestEdge(t, conn, "e4", "cites", "obj.A", "obj.B", now)

	related, err := Related(conn, "obj.A", nil, 50)
	if err != nil {
		t.Fatalf("Related: %v", err)
	}
	if len(related) != 4 {
		t.Errorf("Related(A): want 4 distinct (kind,endpoint) pairs, got %d", len(related))
		for i, n := range related {
			t.Logf("  [%d] kind=%s endpoint=%s", i, n.Edge.Kind, n.Endpoint)
		}
	}

	// Add a true duplicate: another "related" A→B edge (same kind, same endpoint).
	// Related should still return 4 — the duplicate (related, obj.B) is suppressed.
	insertTestEdge(t, conn, "e5", "related", "obj.A", "obj.B", now)

	related2, err := Related(conn, "obj.A", nil, 50)
	if err != nil {
		t.Fatalf("Related (with dup): %v", err)
	}
	if len(related2) != 4 {
		t.Errorf("Related(A) with dup: want 4 (dup suppressed), got %d", len(related2))
		for i, n := range related2 {
			t.Logf("  [%d] kind=%s endpoint=%s", i, n.Edge.Kind, n.Endpoint)
		}
	}
}

// TestTraceChain verifies BFS derivation tracing and cycle safety.
func TestTraceChain(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()

	now := time.Now().UTC().Format(time.RFC3339)

	// Chain A←B←C where ← means derived_from (dst points toward origin).
	//   Edge t1: src=C, dst=B  (C was derived from B)
	//   Edge t2: src=B, dst=A  (B was derived from A)
	//
	// Trace(C, [derived_from], 3) follows src→dst:
	//   Hop 1: src=C → edges leaving C → (t1: C→B), step{From:C, To:B, depth:1}
	//   Hop 2: src=B → edges leaving B → (t2: B→A), step{From:B, To:A, depth:2}
	// Expected: 2 steps.
	insertTestEdge(t, conn, "t1", "derived_from", "chain.C", "chain.B", now)
	insertTestEdge(t, conn, "t2", "derived_from", "chain.B", "chain.A", now)

	steps, err := Trace(conn, "chain.C", []types.EdgeKind{types.EdgeDerivedFrom}, 3)
	if err != nil {
		t.Fatalf("Trace: %v", err)
	}
	if len(steps) != 2 {
		t.Errorf("Trace(C, derived_from, 3): want 2 steps, got %d", len(steps))
		for i, s := range steps {
			t.Logf("  step[%d]: From=%s To=%s depth=%d edge=%s", i, s.From, s.To, s.HopDepth, s.Edge.ID)
		}
	}

	if len(steps) >= 1 {
		s := steps[0]
		if s.From != "chain.C" || s.To != "chain.B" || s.HopDepth != 1 {
			t.Errorf("step[0]: want From=chain.C To=chain.B depth=1, got From=%s To=%s depth=%d",
				s.From, s.To, s.HopDepth)
		}
	}
	if len(steps) >= 2 {
		s := steps[1]
		if s.From != "chain.B" || s.To != "chain.A" || s.HopDepth != 2 {
			t.Errorf("step[1]: want From=chain.B To=chain.A depth=2, got From=%s To=%s depth=%d",
				s.From, s.To, s.HopDepth)
		}
	}

	// Cycle safety: add back-edge A→C to form A←B←C←A cycle.
	// Re-trace from C and confirm no infinite loop, no edge appears twice.
	insertTestEdge(t, conn, "t3", "derived_from", "chain.A", "chain.C", now)

	stepsCyclic, err := Trace(conn, "chain.C", []types.EdgeKind{types.EdgeDerivedFrom}, 3)
	if err != nil {
		t.Fatalf("Trace (cyclic): %v", err)
	}

	seenEdgeID := make(map[string]int)
	for _, s := range stepsCyclic {
		seenEdgeID[s.Edge.ID]++
	}
	for edgeID, count := range seenEdgeID {
		if count > 1 {
			t.Errorf("cycle safety: edge %q appeared %d times in trace", edgeID, count)
		}
	}

	if len(stepsCyclic) > 3 {
		t.Errorf("cycle safety: expected at most 3 steps with maxDepth=3, got %d", len(stepsCyclic))
	}

	t.Logf("Cyclic trace returned %d steps (cycle-safe, maxDepth=3)", len(stepsCyclic))
}
