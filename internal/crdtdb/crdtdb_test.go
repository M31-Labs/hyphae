package crdtdb_test

import (
	"path/filepath"
	"testing"

	"m31labs.dev/gosx/crdt"
	"m31labs.dev/hyphae/internal/crdtdb"
)

func TestStoreRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "crdt.db")
	st, err := crdtdb.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	// Empty store: zero rows, no replay artifacts.
	if n, _ := st.CountChanges(); n != 0 {
		t.Errorf("fresh store should have 0 changes, got %d", n)
	}

	// Build a Doc with several commits.
	doc := crdt.NewDoc()
	if err := doc.Put(crdt.Root, "k1", crdt.StringValue("v1")); err != nil {
		t.Fatalf("put k1: %v", err)
	}
	if _, err := doc.Commit("c1"); err != nil {
		t.Fatalf("commit c1: %v", err)
	}
	if err := doc.Put(crdt.Root, "k2", crdt.StringValue("v2")); err != nil {
		t.Fatalf("put k2: %v", err)
	}
	if _, err := doc.Commit("c2"); err != nil {
		t.Fatalf("commit c2: %v", err)
	}

	inserted, err := st.AppendChangesFromDoc(doc)
	if err != nil {
		t.Fatalf("AppendChangesFromDoc: %v", err)
	}
	if inserted != 2 {
		t.Errorf("first append inserted %d, want 2", inserted)
	}

	// Idempotent second append.
	again, err := st.AppendChangesFromDoc(doc)
	if err != nil {
		t.Fatalf("second AppendChangesFromDoc: %v", err)
	}
	if again != 0 {
		t.Errorf("second append should insert 0 (idempotent), got %d", again)
	}

	heads, err := st.Heads()
	if err != nil {
		t.Fatalf("Heads: %v", err)
	}
	if len(heads) != 1 {
		t.Errorf("heads: got %d entries, want 1", len(heads))
	}

	hist, err := st.History(0)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(hist) != 2 {
		t.Errorf("history: got %d, want 2", len(hist))
	}

	// Reopen + replay.
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	st2, err := crdtdb.Open(dbPath)
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer st2.Close()

	fresh := crdt.NewDoc()
	n, err := st2.LoadAllInto(fresh)
	if err != nil {
		t.Fatalf("LoadAllInto: %v", err)
	}
	if n != 2 {
		t.Errorf("LoadAllInto: replayed %d, want 2", n)
	}

	// Verify replay reconstructed both keys.
	val, _, err := fresh.Get(crdt.Root, "k1")
	if err != nil || val.Kind != crdt.ValueKindString || val.Str != "v1" {
		t.Errorf("after replay k1 = %v %q, want v1", val.Kind, val.Str)
	}
	val, _, err = fresh.Get(crdt.Root, "k2")
	if err != nil || val.Kind != crdt.ValueKindString || val.Str != "v2" {
		t.Errorf("after replay k2 = %v %q, want v2", val.Kind, val.Str)
	}
}

func TestCompactWritesSnapshotRow(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "crdt.db")
	st, err := crdtdb.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	doc := crdt.NewDoc()
	_ = doc.Put(crdt.Root, "k", crdt.StringValue("v"))
	_, _ = doc.Commit("c1")
	_, _ = st.AppendChangesFromDoc(doc)

	if err := st.Compact(doc); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// Reopen and confirm the snapshot row landed.
	st2, _ := crdtdb.Open(dbPath)
	defer st2.Close()
	n, err := st2.CountSnapshots()
	if err != nil {
		t.Fatalf("CountSnapshots: %v", err)
	}
	if n != 1 {
		t.Errorf("snapshot rows = %d, want 1", n)
	}
}
