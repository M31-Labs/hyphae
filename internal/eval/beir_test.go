package eval

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFileT(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestLoadGold(t *testing.T) {
	dir := t.TempDir()
	writeFileT(t, filepath.Join(dir, "queries.jsonl"),
		`{"_id":"q1","text":"how is the index stored"}`+"\n"+
			`{"_id":"q2","text":"what is a spore"}`+"\n")
	writeFileT(t, filepath.Join(dir, "qrels.tsv"),
		"query-id\tcorpus-id\tscore\n"+ // header → skipped
			"q1\tconcept.storage\t2\n"+
			"q1\tconcept.index\t1\n"+
			"q2\tconcept.spore\t2\n"+
			"q2\tconcept.bogus\t0\n") // score 0 → dropped

	queries, qrels, err := LoadGold(dir)
	if err != nil {
		t.Fatalf("LoadGold: %v", err)
	}
	if len(queries) != 2 || queries[0].ID != "q1" || queries[1].Text != "what is a spore" {
		t.Fatalf("queries = %+v", queries)
	}
	if qrels["q1"]["concept.storage"] != 2 || qrels["q1"]["concept.index"] != 1 {
		t.Fatalf("q1 qrels = %+v", qrels["q1"])
	}
	if _, ok := qrels["q2"]["concept.bogus"]; ok {
		t.Fatalf("score-0 judgment should be dropped")
	}
}

func TestWriteBEIRRoundTrip(t *testing.T) {
	dir := t.TempDir()
	corpus := []CorpusDoc{{ID: "d1", Title: "T1", Text: "body one"}}
	queries := []Query{{ID: "q1", Text: "find one"}}
	qrels := Qrels{"q1": {"d1": 1}}

	if err := WriteBEIR(dir, corpus, queries, qrels); err != nil {
		t.Fatalf("WriteBEIR: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "corpus.jsonl"))
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	if got := string(raw); !strings.Contains(got, `"_id":"d1"`) || !strings.Contains(got, "body one") {
		t.Fatalf("corpus jsonl missing expected content: %s", got)
	}
	gotQ, err := readQueries(filepath.Join(dir, "queries.jsonl"))
	if err != nil || !reflect.DeepEqual(gotQ, queries) {
		t.Fatalf("queries round-trip: err=%v got=%+v", err, gotQ)
	}
	gotQr, err := readQrels(filepath.Join(dir, "qrels", "test.tsv"))
	if err != nil || gotQr["q1"]["d1"] != 1 {
		t.Fatalf("qrels round-trip: err=%v got=%+v", err, gotQr)
	}
}
