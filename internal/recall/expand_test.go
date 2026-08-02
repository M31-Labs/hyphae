package recall_test

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/hyphae/internal/db"
	"m31labs.dev/hyphae/internal/recall"
	"m31labs.dev/hyphae/internal/types"
)

func insertEdge(t *testing.T, conn *sql.DB, id, kind, src, dst string) {
	t.Helper()
	_, err := conn.Exec(
		`INSERT INTO edges (id, kind, src_id, dst_id, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, kind, src, dst, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("insert edge %s: %v", id, err)
	}
}

// TestRecall_GraphExpansionAddsLinkedNeighbor locks G-B's core behavior: a
// document that matches no query term still surfaces when a typed edge
// links it to a BM25 seed, ranked behind the seed, with Via naming the
// edge kind and the seed.
func TestRecall_GraphExpansionAddsLinkedNeighbor(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer conn.Close()

	objs := makeObjects()
	// A lesson that never mentions "webhook" but derives from the billing
	// webhooks concept.
	objs = append(objs, types.Object{
		ID:        "obj-lesson-001",
		Type:      types.TypeLesson,
		SpaceID:   "acme/platform",
		Title:     "Retry Storm Postmortem",
		Summary:   "Lesson from the retry storm incident.",
		Body:      "Exponential backoff must cap at one hour. Unbounded retries amplified the outage.",
		UpdatedAt: time.Now(),
	})
	if err := recall.IndexBatch(conn, objs); err != nil {
		t.Fatalf("IndexBatch: %v", err)
	}
	insertEdge(t, conn, "edge-1", "derived_from", "obj-lesson-001", "obj-billing-001")

	resp, err := recall.Recall(conn, "billing webhooks", 12, types.Budget{})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}

	var seedIdx, linkedIdx = -1, -1
	var linked recall.Hit
	for i, h := range resp.Hits {
		if strings.Contains(h.URI, "obj-billing-001") {
			seedIdx = i
		}
		if strings.Contains(h.URI, "obj-lesson-001") {
			linkedIdx = i
			linked = h
		}
	}
	if seedIdx == -1 {
		t.Fatalf("seed obj-billing-001 missing from hits: %+v", resp.Hits)
	}
	if linkedIdx == -1 {
		t.Fatalf("linked obj-lesson-001 missing from hits: %+v", resp.Hits)
	}
	if linkedIdx < seedIdx {
		t.Fatalf("linked hit ranked before its seed: linked=%d seed=%d", linkedIdx, seedIdx)
	}
	if linked.Via != "derived_from from obj-billing-001" {
		t.Fatalf("linked Via = %q, want derivation provenance", linked.Via)
	}
	if !strings.Contains(resp.Summary, "(+1 linked)") {
		t.Fatalf("summary %q missing linked note", resp.Summary)
	}

	// Direct-match count stays honest: the linked neighbor must not
	// inflate "Found N matches".
	if !strings.Contains(resp.Summary, "Found ") {
		t.Fatalf("summary %q lost its match count", resp.Summary)
	}
}

// TestRecall_GraphExpansionSecondHop proves the walk reaches depth two:
// seed -> lesson -> decision over supports/wikilink edges.
func TestRecall_GraphExpansionSecondHop(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer conn.Close()

	objs := makeObjects()
	objs = append(objs,
		types.Object{
			ID: "obj-lesson-001", Type: types.TypeLesson, SpaceID: "acme/platform",
			Title: "Retry Storm Postmortem", Body: "Cap the backoff.", UpdatedAt: time.Now(),
		},
		types.Object{
			ID: "obj-decision-009", Type: types.TypeDecision, SpaceID: "acme/platform",
			Title: "Backoff Ceiling Decision", Body: "One hour ceiling adopted.", UpdatedAt: time.Now(),
		},
	)
	if err := recall.IndexBatch(conn, objs); err != nil {
		t.Fatalf("IndexBatch: %v", err)
	}
	insertEdge(t, conn, "edge-1", "supports", "obj-lesson-001", "obj-billing-001")
	insertEdge(t, conn, "edge-2", "wikilink", "obj-lesson-001", "obj-decision-009")

	resp, err := recall.Recall(conn, "billing webhooks", 12, types.Budget{})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}

	var hopOne, hopTwo bool
	for _, h := range resp.Hits {
		if strings.Contains(h.URI, "obj-lesson-001") {
			hopOne = true
		}
		if strings.Contains(h.URI, "obj-decision-009") {
			hopTwo = true
			if h.Via != "wikilink from obj-lesson-001" {
				t.Fatalf("hop-two Via = %q, want wikilink from obj-lesson-001", h.Via)
			}
		}
	}
	if !hopOne || !hopTwo {
		t.Fatalf("expected both hop-one and hop-two neighbors (got hopOne=%v hopTwo=%v): %+v", hopOne, hopTwo, resp.Hits)
	}
}

// TestRecall_GraphExpansionIgnoresUntypedEdges proves the walk follows
// only wikilink, derived_from, and supports: related and source_ref edges
// add nothing.
func TestRecall_GraphExpansionIgnoresUntypedEdges(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer conn.Close()

	if err := recall.IndexBatch(conn, makeObjects()); err != nil {
		t.Fatalf("IndexBatch: %v", err)
	}
	insertEdge(t, conn, "edge-1", "related", "obj-frontend-001", "obj-billing-001")
	insertEdge(t, conn, "edge-2", "source_ref", "obj-deploy-001", "obj-billing-001")

	resp, err := recall.Recall(conn, "billing webhooks", 12, types.Budget{})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	for _, h := range resp.Hits {
		if h.Via != "" {
			t.Fatalf("unexpected graph hit %+v over a non-walked edge kind", h)
		}
	}
	if strings.Contains(resp.Summary, "linked") {
		t.Fatalf("summary %q reports linked hits with no walked edges", resp.Summary)
	}
}

// TestRecall_DisableGraphOption locks the opt-out: with DisableGraph the
// response is pure BM25 even when walkable edges exist.
func TestRecall_DisableGraphOption(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer conn.Close()

	objs := append(makeObjects(), types.Object{
		ID: "obj-lesson-001", Type: types.TypeLesson, SpaceID: "acme/platform",
		Title: "Retry Storm Postmortem", Body: "Cap the backoff.", UpdatedAt: time.Now(),
	})
	if err := recall.IndexBatch(conn, objs); err != nil {
		t.Fatalf("IndexBatch: %v", err)
	}
	insertEdge(t, conn, "edge-1", "derived_from", "obj-lesson-001", "obj-billing-001")

	resp, err := recall.RecallWithOptions(conn, "billing webhooks", 12, types.Budget{}, recall.Options{DisableGraph: true})
	if err != nil {
		t.Fatalf("RecallWithOptions: %v", err)
	}
	for _, h := range resp.Hits {
		if strings.Contains(h.URI, "obj-lesson-001") || h.Via != "" {
			t.Fatalf("graph hit present with DisableGraph: %+v", h)
		}
	}
}

// TestRecall_GraphExpansionSkipsExternalEndpoints proves an edge whose
// other end is not an indexed object (an external URI) drops out cleanly.
func TestRecall_GraphExpansionSkipsExternalEndpoints(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer conn.Close()

	if err := recall.IndexBatch(conn, makeObjects()); err != nil {
		t.Fatalf("IndexBatch: %v", err)
	}
	insertEdge(t, conn, "edge-1", "wikilink", "obj-billing-001", "https://example.com/spec")

	resp, err := recall.Recall(conn, "billing webhooks", 12, types.Budget{})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	for _, h := range resp.Hits {
		if h.Via != "" {
			t.Fatalf("external endpoint surfaced as a hit: %+v", h)
		}
	}
}
