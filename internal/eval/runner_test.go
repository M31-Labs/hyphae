package eval

import (
	"context"
	"database/sql"
	"testing"

	"m31labs.dev/hyphae/internal/db"
	"m31labs.dev/hyphae/internal/recall"
	"m31labs.dev/hyphae/internal/types"
)

type fakeManta struct {
	bm25, dense Metrics
	denseCalled bool
}

func (f *fakeManta) EvalBM25(ctx context.Context, beirDir, split string, topK int) (Metrics, error) {
	return f.bm25, nil
}

func (f *fakeManta) EvalDense(ctx context.Context, beirDir, model, split string, topK int) (Metrics, error) {
	f.denseCalled = true
	return f.dense, nil
}

func indexFixtures(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := db.Open(t.TempDir() + "/index.db")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	objs := []types.Object{
		{ID: "concept.storage", Type: "concept", SpaceID: "m31labs/hyphae", Title: "Storage", Summary: "how the index is stored on disk", Body: "sqlite fts5 derived index rebuildable"},
		{ID: "concept.spore", Type: "concept", SpaceID: "m31labs/hyphae", Title: "Spore", Summary: "agent contribution unit", Body: "spore lands in the inbox for review"},
	}
	if err := recall.IndexBatch(conn, objs); err != nil {
		t.Fatalf("IndexBatch: %v", err)
	}
	return conn
}

func TestRunWithManta(t *testing.T) {
	conn := indexFixtures(t)
	defer conn.Close()

	gold := t.TempDir()
	writeFileT(t, gold+"/queries.jsonl",
		`{"_id":"q1","text":"how is the index stored"}`+"\n"+
			`{"_id":"q2","text":"spore inbox review"}`+"\n")
	writeFileT(t, gold+"/qrels.tsv",
		"q1\tconcept.storage\t2\n"+
			"q2\tconcept.spore\t2\n"+
			"q1\tconcept.missing\t1\n") // drift: not in corpus → skipped

	fake := &fakeManta{bm25: Metrics{NDCGAt10: 0.40}, dense: Metrics{NDCGAt10: 0.55}}
	res, err := Run(context.Background(), RunOpts{
		Conn: conn, SpaceID: "m31labs/hyphae", GoldDir: gold,
		BeirDir: t.TempDir(), Model: "fake.embedding.mll", TopK: 10, Manta: fake,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.CorpusDocs != 2 || res.GoldQueries != 2 {
		t.Fatalf("res = %+v", res)
	}
	if res.HyphaeFTS5.NDCGAt10 <= 0 {
		t.Fatalf("expected R1 nDCG@10 > 0, got %v", res.HyphaeFTS5.NDCGAt10)
	}
	if res.GoldCoverage < 0.66 || res.GoldCoverage > 0.67 { // 2 of 3 judgments resolved
		t.Fatalf("coverage = %v, want ~0.667", res.GoldCoverage)
	}
	if res.MantaBM25 == nil || res.MantaBM25.NDCGAt10 != 0.40 {
		t.Fatalf("bm25 = %+v", res.MantaBM25)
	}
	if res.MantaDense == nil || res.MantaDense.NDCGAt10 != 0.55 || !fake.denseCalled {
		t.Fatalf("dense = %+v (called=%v)", res.MantaDense, fake.denseCalled)
	}
}

func TestRunR1Only(t *testing.T) {
	conn := indexFixtures(t)
	defer conn.Close()

	gold := t.TempDir()
	writeFileT(t, gold+"/queries.jsonl", `{"_id":"q1","text":"how is the index stored"}`+"\n")
	writeFileT(t, gold+"/qrels.tsv", "q1\tconcept.storage\t2\n")

	res, err := Run(context.Background(), RunOpts{
		Conn: conn, SpaceID: "m31labs/hyphae", GoldDir: gold, BeirDir: t.TempDir(), TopK: 10, Manta: nil,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.MantaBM25 != nil || res.MantaDense != nil {
		t.Fatalf("expected no manta metrics in R1-only mode")
	}
	if res.HyphaeFTS5.RecallAt10 <= 0 {
		t.Fatalf("expected R1 recall@10 > 0, got %v", res.HyphaeFTS5.RecallAt10)
	}
}
