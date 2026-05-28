package crdtshadow_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/gosx/crdt"
	"m31labs.dev/hyphae/internal/crdtshadow"
	"m31labs.dev/hyphae/internal/types"
)

func TestShadowRoundTripsAllRecorders(t *testing.T) {
	root := t.TempDir()

	s, err := crdtshadow.Open(root, "hypha://test/space")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if err := s.RecordReceipt(types.Receipt{
		ID:      "hypha-receipt:test:1",
		SpaceID: "hypha://test/space",
		Action:  "spore:create",
	}); err != nil {
		t.Fatalf("RecordReceipt: %v", err)
	}
	if err := s.RecordEdge(types.Edge{
		ID:    "edge:test:1",
		Kind:  "derived_from",
		SrcID: "concept.a",
		DstID: "concept.b",
	}); err != nil {
		t.Fatalf("RecordEdge: %v", err)
	}
	if err := s.RecordSpore(crdtshadow.SporeSummary{
		ID:      "spore.test.1",
		SpaceID: "hypha://test/space",
		Status:  "unreviewed",
		Path:    "/tmp/spore.md",
	}); err != nil {
		t.Fatalf("RecordSpore: %v", err)
	}
	if err := s.RecordSporeStatus("spore.test.1", "accepted"); err != nil {
		t.Fatalf("RecordSporeStatus: %v", err)
	}
	if err := s.RecordTrace(crdtshadow.TraceSummary{
		ID:      "trace.test.1",
		SpaceID: "hypha://test/space",
		AgentID: "agent://test/me",
		Status:  "open",
	}); err != nil {
		t.Fatalf("RecordTrace: %v", err)
	}
	canonicalFile := filepath.Join(root, "concepts", "x.md")
	if err := s.RecordCanonicalWrite(canonicalFile, []byte("# X\n\nBody.\n")); err != nil {
		t.Fatalf("RecordCanonicalWrite: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Re-open and verify the snapshot loaded cleanly.
	s2, err := crdtshadow.Open(root, "hypha://test/space")
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer s2.Close()

	got := dumpDoc(t, s2.Doc())

	for _, want := range []string{
		"receipts", "edges", "spores", "traces", "canonical",
		"hypha-receipt:test:1",
		"edge:test:1",
		"spore.test.1",
		"trace.test.1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("re-opened Doc missing %q\nfull dump:\n%s", want, got)
		}
	}

	// Spore status should be "accepted" after the LWW flip.
	sporeRaw := extractMapBlob(t, s2.Doc(), "spores", "spore.test.1")
	var sum crdtshadow.SporeSummary
	if err := json.Unmarshal(sporeRaw, &sum); err != nil {
		t.Fatalf("unmarshal spore summary: %v", err)
	}
	if sum.Status != "accepted" {
		t.Errorf("spore status = %q, want accepted", sum.Status)
	}
}

func TestShadowChangeLogLandsOnDisk(t *testing.T) {
	root := t.TempDir()
	s, err := crdtshadow.Open(root, "hypha://test/space")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.RecordReceipt(types.Receipt{ID: "r1", Action: "test"}); err != nil {
		t.Fatalf("RecordReceipt: %v", err)
	}

	dbPath := s.DBPath()
	if !strings.HasSuffix(dbPath, crdtshadow.DBFilename) {
		t.Errorf("DBPath = %q, want suffix %q", dbPath, crdtshadow.DBFilename)
	}
	if data, err := readFile(dbPath); err != nil || len(data) == 0 {
		t.Errorf("change log not written or empty (len=%d, err=%v)", len(data), err)
	}
	n, err := s.Store().CountChanges()
	if err != nil {
		t.Fatalf("CountChanges: %v", err)
	}
	if n < 1 {
		t.Errorf("expected ≥1 change row after RecordReceipt, got %d", n)
	}
}

func TestShadowMigratesLegacyDat(t *testing.T) {
	root := t.TempDir()
	// Build a Phase-1-style snapshot at .crdt.dat with one Map entry.
	{
		s, err := crdtshadow.Open(root, "hypha://test/legacy")
		if err != nil {
			t.Fatalf("seed Open: %v", err)
		}
		if err := s.RecordReceipt(types.Receipt{ID: "legacy-receipt", Action: "test"}); err != nil {
			t.Fatalf("seed RecordReceipt: %v", err)
		}
		_ = s.Close()
		// Now write a fake legacy .dat the migration must absorb. Use the
		// store's current Doc state via the public Save() path.
		// (For this test we'll simulate by reopening, exporting via Doc,
		// and saving as .dat — exercises the migrate-on-open path.)
	}
	// At this point .crdt.db exists. Drop a .crdt.dat next to it to
	// trigger migration on the next Open by saving the current Doc.
	{
		s2, err := crdtshadow.Open(root, "hypha://test/legacy")
		if err != nil {
			t.Fatalf("reopen for legacy snapshot creation: %v", err)
		}
		data, err := s2.Doc().Save()
		if err != nil {
			t.Fatalf("doc.Save: %v", err)
		}
		legacy := filepath.Join(root, crdtshadow.LegacySnapshotFilename)
		if err := writeFile(legacy, data); err != nil {
			t.Fatalf("write legacy: %v", err)
		}
		_ = s2.Close()
	}
	// Reopen — the migration should consume the .dat and remove it.
	s3, err := crdtshadow.Open(root, "hypha://test/legacy")
	if err != nil {
		t.Fatalf("Open after legacy seed: %v", err)
	}
	defer s3.Close()
	if _, err := readFile(filepath.Join(root, crdtshadow.LegacySnapshotFilename)); err == nil {
		t.Errorf("legacy .crdt.dat was not removed after migration")
	}
}

func TestRegistryCachesByRoot(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	r := crdtshadow.NewRegistry()
	defer r.Close()

	a1, err := r.Get(rootA, "hypha://test/a")
	if err != nil {
		t.Fatalf("get rootA: %v", err)
	}
	a2, err := r.Get(rootA, "hypha://test/a")
	if err != nil {
		t.Fatalf("get rootA again: %v", err)
	}
	if a1 != a2 {
		t.Errorf("registry should return the same Shadow for the same root")
	}
	b1, err := r.Get(rootB, "hypha://test/b")
	if err != nil {
		t.Fatalf("get rootB: %v", err)
	}
	if a1 == b1 {
		t.Errorf("different roots should return different Shadows")
	}
}

// ─── helpers ────────────────────────────────────────────────────────────

func dumpDoc(t *testing.T, doc *crdt.Doc) string {
	t.Helper()
	data, err := doc.Save()
	if err != nil {
		t.Fatalf("dump Save: %v", err)
	}
	return string(data)
}

func extractMapBlob(t *testing.T, doc *crdt.Doc, topProp, key string) []byte {
	t.Helper()
	_, parent, err := doc.Get(crdt.Root, crdt.Prop(topProp))
	if err != nil {
		t.Fatalf("get top %q: %v", topProp, err)
	}
	val, _, err := doc.Get(parent, crdt.Prop(key))
	if err != nil {
		t.Fatalf("get %q.%q: %v", topProp, key, err)
	}
	if val.Kind != crdt.ValueKindBytes {
		t.Fatalf("%q.%q kind = %v, want bytes", topProp, key, val.Kind)
	}
	return val.Bytes
}

func readFile(path string) ([]byte, error) {
	return readIfExistsTest(path)
}

func readIfExistsTest(path string) ([]byte, error) {
	// Mirror the package-internal readIfExists for the test;
	// avoids exposing an internal helper as API.
	return readSnapshotFile(path)
}
