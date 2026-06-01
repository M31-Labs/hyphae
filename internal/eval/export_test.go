package eval

import (
	"strings"
	"testing"

	"m31labs.dev/hyphae/internal/db"
	"m31labs.dev/hyphae/internal/recall"
	"m31labs.dev/hyphae/internal/types"
)

func TestExportCorpus(t *testing.T) {
	conn, err := db.Open(t.TempDir() + "/index.db")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer conn.Close()

	objs := []types.Object{
		{ID: "concept.storage", Type: "concept", SpaceID: "m31labs/hyphae", Title: "Storage", Tags: []string{"sqlite", "fts"}, Summary: "how the index is stored", Body: "markdown on disk is canonical"},
		{ID: "concept.other", Type: "concept", SpaceID: "other/space", Title: "Other", Summary: "elsewhere", Body: "different space"},
	}
	if err := recall.IndexBatch(conn, objs); err != nil {
		t.Fatalf("IndexBatch: %v", err)
	}

	docs, err := ExportCorpus(conn, "m31labs/hyphae")
	if err != nil {
		t.Fatalf("ExportCorpus: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 doc for the target space, got %d: %+v", len(docs), docs)
	}
	d := docs[0]
	if d.ID != "concept.storage" || d.Title != "Storage" {
		t.Fatalf("doc id/title = %q/%q", d.ID, d.Title)
	}
	for _, want := range []string{"sqlite", "how the index is stored", "markdown on disk"} {
		if !strings.Contains(d.Text, want) {
			t.Fatalf("Text missing %q: %s", want, d.Text)
		}
	}
	if strings.Contains(d.Text, "Storage") {
		t.Fatalf("Text should carry tags+summary+body only, not the title: %s", d.Text)
	}
}
