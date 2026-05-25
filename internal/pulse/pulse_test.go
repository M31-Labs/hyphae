package pulse_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/odvcencio/hyphae/internal/db"
	"github.com/odvcencio/hyphae/internal/pulse"
)

// helpers --------------------------------------------------------------------

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func mustExec(t *testing.T, conn *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := conn.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// insertObject inserts a minimal objects row.
func insertObject(t *testing.T, conn *sql.DB, id, typ, spaceID, status, title, updatedAt string) {
	t.Helper()
	mustExec(t, conn,
		`INSERT INTO objects(id, type, space_id, file_id, status, title, updated_at)
		 VALUES (?, ?, ?, 'file.001', ?, ?, ?)`,
		id, typ, spaceID, status, title, updatedAt,
	)
}

// insertEdge inserts a minimal edges row.
func insertEdge(t *testing.T, conn *sql.DB, id, kind, src, dst, createdAt string) {
	t.Helper()
	mustExec(t, conn,
		`INSERT INTO edges(id, kind, src_id, dst_id, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, kind, src, dst, createdAt,
	)
}

// insertReceipt inserts a minimal receipts row.
func insertReceipt(t *testing.T, conn *sql.DB, id, spaceID, action, createdAt string) {
	t.Helper()
	mustExec(t, conn,
		`INSERT INTO receipts(id, space_id, subject_id, subject_kind, action, status, created_at)
		 VALUES (?, ?, 'subj.001', 'spore', ?, 'ok', ?)`,
		id, spaceID, action, createdAt,
	)
}

// Test 1: Compute happy path --------------------------------------------------

func TestComputeHappyPath(t *testing.T) {
	conn := openDB(t)

	now := time.Now().UTC()
	inWindow := now.Add(-5 * 24 * time.Hour).Format(time.RFC3339)   // 5 days ago — within 30d
	outWindow := now.Add(-40 * 24 * time.Hour).Format(time.RFC3339) // 40 days ago — outside 30d
	nowStr := now.Format(time.RFC3339)

	const space = "hypha://acme/test"

	// Two objects: one initiative (active) and one concept.
	insertObject(t, conn, "obj.initiative.1", "initiative", space, "active", "Big Initiative", nowStr)
	insertObject(t, conn, "obj.concept.1", "concept", space, "", "Core Concept", inWindow)

	// 4 edges: 2 in-window, 2 outside.
	insertEdge(t, conn, "e1", "related", "obj.concept.1", "obj.initiative.1", inWindow)
	insertEdge(t, conn, "e2", "derived_from", "obj.concept.1", "obj.initiative.1", inWindow)
	insertEdge(t, conn, "e3", "related", "obj.concept.1", "obj.initiative.1", outWindow)
	insertEdge(t, conn, "e4", "wikilink", "obj.concept.1", "obj.initiative.1", outWindow)

	// 3 receipts: 2 in-window (one spore:create, one graft), 1 outside.
	insertReceipt(t, conn, "r1", space, "spore:create", inWindow)
	insertReceipt(t, conn, "r2", space, "graft", inWindow)
	insertReceipt(t, conn, "r3", space, "spore:create", outWindow)

	p, err := pulse.Compute(conn, space, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	// TopInitiatives: should have at least 1 initiative.
	if len(p.TopInitiatives) == 0 {
		t.Error("expected at least 1 TopInitiative, got 0")
	} else {
		ti := p.TopInitiatives[0]
		if ti.ID != "obj.initiative.1" {
			t.Errorf("TopInitiatives[0].ID = %q, want obj.initiative.1", ti.ID)
		}
		if ti.Title != "Big Initiative" {
			t.Errorf("TopInitiatives[0].Title = %q, want Big Initiative", ti.Title)
		}
		// Initiative has 4 inbound edges total (2 in-window + 2 out-of-window)
		// because TopInitiatives is NOT window-filtered (counts all inbound).
		if ti.InboundEdges != 4 {
			t.Errorf("TopInitiatives[0].InboundEdges = %d, want 4", ti.InboundEdges)
		}
	}

	// Activity counts.
	if p.Activity.SporesSubmitted != 1 {
		t.Errorf("Activity.SporesSubmitted = %d, want 1", p.Activity.SporesSubmitted)
	}
	if p.Activity.GraftsApplied != 1 {
		t.Errorf("Activity.GraftsApplied = %d, want 1", p.Activity.GraftsApplied)
	}
	if p.Activity.NewEdges != 2 {
		t.Errorf("Activity.NewEdges = %d, want 2 (in-window)", p.Activity.NewEdges)
	}
	// obj.concept.1 has updated_at = inWindow (within 30d); obj.initiative.1 updated_at = nowStr (also in window).
	if p.Activity.NewObjects != 2 {
		t.Errorf("Activity.NewObjects = %d, want 2", p.Activity.NewObjects)
	}

	// EdgeKindDist: should have entries (all edges, not just in-window).
	if len(p.EdgeKindDist) == 0 {
		t.Error("expected EdgeKindDist to be non-empty")
	}

	// RecentPressure: should mention the in-window edge kinds.
	if len(p.RecentPressure) == 0 {
		t.Error("expected RecentPressure to be non-empty")
	}
	foundRelated := false
	for _, pr := range p.RecentPressure {
		if pr.Kind == "related" && pr.Topic == "edges" {
			foundRelated = true
		}
	}
	if !foundRelated {
		t.Errorf("RecentPressure missing 'related' edge pressure; got: %+v", p.RecentPressure)
	}

	// TokensUsed should be positive.
	if p.TokensUsed <= 0 {
		t.Errorf("TokensUsed = %d, want > 0", p.TokensUsed)
	}

	// Window metadata.
	if p.Window != "30d" {
		t.Errorf("Window = %q, want 30d", p.Window)
	}
	if p.Space != space {
		t.Errorf("Space = %q, want %q", p.Space, space)
	}
}

// Test 2: Empty DB -----------------------------------------------------------

func TestComputeEmptyDB(t *testing.T) {
	conn := openDB(t)

	p, err := pulse.Compute(conn, "", 30*24*time.Hour)
	if err != nil {
		t.Fatalf("Compute on empty DB: %v", err)
	}

	if len(p.TopInitiatives) != 0 {
		t.Errorf("TopInitiatives: want 0, got %d", len(p.TopInitiatives))
	}
	if len(p.HotZones) != 0 {
		t.Errorf("HotZones: want 0, got %d", len(p.HotZones))
	}
	if len(p.RecentPressure) != 0 {
		t.Errorf("RecentPressure: want 0, got %d", len(p.RecentPressure))
	}
	if len(p.EdgeKindDist) != 0 {
		t.Errorf("EdgeKindDist: want 0, got %d", len(p.EdgeKindDist))
	}
	if p.Activity.SporesSubmitted != 0 {
		t.Errorf("Activity.SporesSubmitted: want 0, got %d", p.Activity.SporesSubmitted)
	}
	if p.Activity.GraftsApplied != 0 {
		t.Errorf("Activity.GraftsApplied: want 0, got %d", p.Activity.GraftsApplied)
	}
	if p.Activity.NewObjects != 0 {
		t.Errorf("Activity.NewObjects: want 0, got %d", p.Activity.NewObjects)
	}
	if p.Activity.NewEdges != 0 {
		t.Errorf("Activity.NewEdges: want 0, got %d", p.Activity.NewEdges)
	}
}

// Test 3: Cache roundtrip ----------------------------------------------------

func TestCacheRoundtrip(t *testing.T) {
	conn := openDB(t)

	// Compute a pulse (empty DB is fine — we're testing cache mechanics).
	p, err := pulse.Compute(conn, "hypha://acme/cache-test", 7*24*time.Hour)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if p.Window != "7d" {
		t.Errorf("Window = %q, want 7d", p.Window)
	}

	ttl := 10 * time.Minute

	// Store the pulse.
	if err := pulse.Store(conn, p, ttl); err != nil {
		t.Fatalf("Store: %v", err)
	}

	// Cached should return the same pulse within TTL.
	got, err := pulse.Cached(conn, "hypha://acme/cache-test", "7d", ttl)
	if err != nil {
		t.Fatalf("Cached (hit expected): %v", err)
	}
	if got.Window != p.Window {
		t.Errorf("cached Window = %q, want %q", got.Window, p.Window)
	}
	if got.Space != p.Space {
		t.Errorf("cached Space = %q, want %q", got.Space, p.Space)
	}
	if got.TokensUsed != p.TokensUsed {
		t.Errorf("cached TokensUsed = %d, want %d", got.TokensUsed, p.TokensUsed)
	}

	// Cached with a tiny TTL (1 nanosecond) should produce ErrNoCache because
	// the computed_at is now in the past relative to the cutoff.
	_, err = pulse.Cached(conn, "hypha://acme/cache-test", "7d", time.Nanosecond)
	if !errors.Is(err, pulse.ErrNoCache) {
		t.Errorf("Cached (stale): want ErrNoCache, got %v", err)
	}

	// Wrong window label — should also be a miss.
	_, err = pulse.Cached(conn, "hypha://acme/cache-test", "30d", ttl)
	if !errors.Is(err, pulse.ErrNoCache) {
		t.Errorf("Cached (wrong window): want ErrNoCache, got %v", err)
	}

	// Wrong space — should also be a miss.
	_, err = pulse.Cached(conn, "hypha://other/space", "7d", ttl)
	if !errors.Is(err, pulse.ErrNoCache) {
		t.Errorf("Cached (wrong space): want ErrNoCache, got %v", err)
	}
}
