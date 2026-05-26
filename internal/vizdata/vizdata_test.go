package vizdata_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"m31labs.dev/hyphae/internal/db"
	"m31labs.dev/hyphae/internal/vizdata"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	conn, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestFullGraphEmpty(t *testing.T) {
	conn := openTestDB(t)

	resp, err := vizdata.FullGraph(conn, nil, 500)
	if err != nil {
		t.Fatalf("FullGraph on empty db: %v", err)
	}
	if resp.Nodes == nil {
		t.Error("FullGraph: expected non-nil nodes slice")
	}
	if resp.Edges == nil {
		t.Error("FullGraph: expected non-nil edges slice")
	}
	if len(resp.Nodes) != 0 {
		t.Errorf("FullGraph on empty db: expected 0 nodes, got %d", len(resp.Nodes))
	}
}

func TestSubgraphEmpty(t *testing.T) {
	conn := openTestDB(t)

	resp, err := vizdata.Subgraph(conn, "nonexistent.id", 2, nil)
	if err != nil {
		t.Fatalf("Subgraph on empty db: %v", err)
	}
	// Should return at least the center node (as a stub).
	if len(resp.Nodes) == 0 {
		t.Error("Subgraph: expected at least the center node in result")
	}
}

func TestGetObjectRowMissing(t *testing.T) {
	conn := openTestDB(t)

	obj, err := vizdata.GetObjectRow(conn, "does.not.exist")
	if err != nil {
		t.Fatalf("GetObjectRow for missing id: unexpected error: %v", err)
	}
	if obj != nil {
		t.Error("GetObjectRow for missing id: expected nil, got non-nil")
	}
}

func TestGetObjectDetailMissing(t *testing.T) {
	conn := openTestDB(t)

	detail, err := vizdata.GetObjectDetail(conn, "does.not.exist")
	if err != nil {
		t.Fatalf("GetObjectDetail for missing id: unexpected error: %v", err)
	}
	if detail == nil {
		t.Fatal("GetObjectDetail: expected non-nil detail (with nil Object field)")
	}
	if detail.Object != nil {
		t.Error("GetObjectDetail for missing id: expected nil Object")
	}
}
