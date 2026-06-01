package eval

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecideBuildHybrid(t *testing.T) {
	r := Result{
		HyphaeFTS5: Metrics{NDCGAt10: 0.50, RecallAt10: 0.60},
		MantaDense: &Metrics{NDCGAt10: 0.55, RecallAt10: 0.65},
	}
	if got := Decide(r, 0.03).Recommendation; got != "build_hybrid" {
		t.Fatalf("recommendation = %q, want build_hybrid", got)
	}
}

func TestDecideDefer(t *testing.T) {
	// Within threshold → defer.
	within := Result{
		HyphaeFTS5: Metrics{NDCGAt10: 0.50, RecallAt10: 0.60},
		MantaDense: &Metrics{NDCGAt10: 0.515, RecallAt10: 0.62},
	}
	if got := Decide(within, 0.03).Recommendation; got != "defer_embeddings" {
		t.Fatalf("within-threshold recommendation = %q, want defer_embeddings", got)
	}
	// nDCG clears threshold but recall regresses → defer.
	recallDown := Result{
		HyphaeFTS5: Metrics{NDCGAt10: 0.50, RecallAt10: 0.60},
		MantaDense: &Metrics{NDCGAt10: 0.55, RecallAt10: 0.59},
	}
	if got := Decide(recallDown, 0.03).Recommendation; got != "defer_embeddings" {
		t.Fatalf("recall-regression recommendation = %q, want defer_embeddings", got)
	}
	// No dense result → defer.
	if got := Decide(Result{HyphaeFTS5: Metrics{NDCGAt10: 0.5}}, 0.03).Recommendation; got != "defer_embeddings" {
		t.Fatalf("no-dense recommendation = %q, want defer_embeddings", got)
	}
}

func TestWriteResult(t *testing.T) {
	root := t.TempDir()
	r := Result{
		Space: "m31labs/hyphae", CorpusDocs: 3, GoldQueries: 2, GoldCoverage: 1,
		HyphaeFTS5: Metrics{NDCGAt10: 0.5}, MantaBM25: &Metrics{NDCGAt10: 0.4},
	}
	d := Decide(r, 0.03)
	path, err := WriteResult(root, "2026-06-01", r, d)
	if err != nil {
		t.Fatalf("WriteResult: %v", err)
	}
	if filepath.Base(path) != "2026-06-01-retrieval.json" {
		t.Fatalf("path = %s", path)
	}
	if filepath.Dir(path) != filepath.Join(root, ".analyses", "eval") {
		t.Fatalf("results not under <spaceRoot>/.analyses/eval: %s", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		`"schema": "hyphae.retrieval_eval.v1"`,
		`"hyphae_fts5"`,
		`"manta_bm25"`,
		`"recommendation": "defer_embeddings"`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("result json missing %q:\n%s", want, s)
		}
	}
}

func TestRenderTable(t *testing.T) {
	o := Outcome{
		Result: Result{
			Space: "m31labs/hyphae", CorpusDocs: 3, GoldQueries: 2, GoldCoverage: 1,
			HyphaeFTS5: Metrics{NDCGAt10: 0.50, RecallAt10: 0.60},
			MantaBM25:  &Metrics{NDCGAt10: 0.40},
			MantaDense: &Metrics{NDCGAt10: 0.55, RecallAt10: 0.65},
		},
		Decision: Decision{Recommendation: "build_hybrid", NDCGDelta: 0.05, RecallAt10Delta: 0.05, NDCGThreshold: 0.03},
	}
	var buf bytes.Buffer
	if err := RenderTable(&buf, o); err != nil {
		t.Fatalf("RenderTable: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"hyphae_fts5", "manta_bm25", "manta_dense", "Decision", "build_hybrid"} {
		if !strings.Contains(out, want) {
			t.Fatalf("table missing %q:\n%s", want, out)
		}
	}
}
