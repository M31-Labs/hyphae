package recall

import (
	"testing"

	"m31labs.dev/hyphae/internal/db"
	"m31labs.dev/hyphae/internal/types"
)

func TestRankIDs(t *testing.T) {
	conn, err := db.Open(t.TempDir() + "/index.db")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer conn.Close()

	objs := []types.Object{
		{ID: "concept.alpha", Type: "concept", SpaceID: "m31labs/hyphae", Title: "Alpha widget design", Summary: "the alpha widget", Body: "alpha alpha alpha widget"},
		{ID: "concept.beta", Type: "concept", SpaceID: "m31labs/hyphae", Title: "Beta gadget", Summary: "beta gadget notes", Body: "beta gadget"},
		{ID: "concept.gamma", Type: "concept", SpaceID: "other/space", Title: "Alpha in another space", Summary: "alpha elsewhere", Body: "alpha widget elsewhere"},
	}
	if err := IndexBatch(conn, objs); err != nil {
		t.Fatalf("IndexBatch: %v", err)
	}

	// Ranking returns bare ids, ordered best-first, scoped to the space.
	got, err := RankIDs(conn, "m31labs/hyphae", "alpha widget", 10)
	if err != nil {
		t.Fatalf("RankIDs: %v", err)
	}
	if len(got) == 0 {
		t.Fatalf("expected hits, got none")
	}
	if got[0].ID != "concept.alpha" {
		t.Fatalf("top id = %q, want concept.alpha", got[0].ID)
	}
	for _, s := range got {
		if s.ID == "concept.gamma" {
			t.Fatalf("space filter leaked: other/space object returned")
		}
	}

	// Punctuation-only query sanitizes to zero terms → empty slice, nil error.
	empty, err := RankIDs(conn, "m31labs/hyphae", "?!", 10)
	if err != nil {
		t.Fatalf("RankIDs empty-query: %v", err)
	}
	if empty != nil {
		t.Fatalf("expected nil slice for no-term query, got %v", empty)
	}
}

func TestRankIDsORSemantics(t *testing.T) {
	conn, err := db.Open(t.TempDir() + "/index.db")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer conn.Close()

	objs := []types.Object{
		{ID: "concept.spore", Type: "concept", SpaceID: "m31labs/hyphae", Title: "Spore", Summary: "agent contribution unit", Body: "a spore is submitted to the inbox for review"},
		{ID: "concept.graft", Type: "concept", SpaceID: "m31labs/hyphae", Title: "Graft", Summary: "accepting a spore", Body: "graft applies proposed writes to canonical files"},
	}
	if err := IndexBatch(conn, objs); err != nil {
		t.Fatalf("IndexBatch: %v", err)
	}

	// Natural-language query: only "spore" appears in any doc; "explain" and
	// "lifecycle" appear nowhere. Under implicit-AND this matches zero rows;
	// OR semantics must still surface concept.spore.
	got, err := RankIDs(conn, "m31labs/hyphae", "explain the spore lifecycle", 10)
	if err != nil {
		t.Fatalf("RankIDs: %v", err)
	}
	if len(got) == 0 {
		t.Fatalf("OR-mode should return matches for a partial-term NL query; got none (implicit-AND?)")
	}
	if got[0].ID != "concept.spore" {
		t.Fatalf("top id = %q, want concept.spore", got[0].ID)
	}
}
