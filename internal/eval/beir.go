package eval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// CorpusDoc is one BEIR corpus document. ID is the bare object id.
type CorpusDoc struct {
	ID    string `json:"_id"`
	Title string `json:"title"`
	Text  string `json:"text"`
}

// Query is one BEIR query (gold-set or exported).
type Query struct {
	ID   string `json:"_id"`
	Text string `json:"text"`
}

// Qrels maps query id -> (object id -> relevance). Only positive judgments are
// retained.
type Qrels map[string]map[string]int

// LoadGold reads the human-authored gold set: queries.jsonl ({"_id","text"})
// plus a flat qrels.tsv (query-id<TAB>object-id<TAB>score). A header row whose
// first field contains "query" is skipped; score<=0 judgments are dropped.
func LoadGold(dir string) ([]Query, Qrels, error) {
	queries, err := readQueries(filepath.Join(dir, "queries.jsonl"))
	if err != nil {
		return nil, nil, err
	}
	qrels, err := readQrels(filepath.Join(dir, "qrels.tsv"))
	if err != nil {
		return nil, nil, err
	}
	return queries, qrels, nil
}

func readQueries(path string) ([]Query, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("eval: open queries %s: %w", path, err)
	}
	defer f.Close()
	var out []Query
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var q Query
		if err := json.Unmarshal([]byte(line), &q); err != nil {
			return nil, fmt.Errorf("eval: parse query %q: %w", line, err)
		}
		out = append(out, q)
	}
	return out, sc.Err()
}

func readQrels(path string) (Qrels, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("eval: open qrels %s: %w", path, err)
	}
	defer f.Close()
	qrels := Qrels{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			continue
		}
		if strings.Contains(strings.ToLower(fields[0]), "query") {
			continue // header row
		}
		qid, docid := fields[0], fields[1]
		score, err := strconv.Atoi(strings.TrimSpace(fields[2]))
		if err != nil {
			return nil, fmt.Errorf("eval: parse qrels score %q: %w", line, err)
		}
		if score <= 0 {
			continue
		}
		if qrels[qid] == nil {
			qrels[qid] = map[string]int{}
		}
		qrels[qid][docid] = score
	}
	return qrels, sc.Err()
}

// WriteBEIR writes a manta-compatible BEIR directory: corpus.jsonl,
// queries.jsonl, and qrels/test.tsv.
func WriteBEIR(dir string, corpus []CorpusDoc, queries []Query, qrels Qrels) error {
	if err := os.MkdirAll(filepath.Join(dir, "qrels"), 0o755); err != nil {
		return fmt.Errorf("eval: mkdir beir: %w", err)
	}
	if err := writeJSONL(filepath.Join(dir, "corpus.jsonl"), len(corpus), func(i int) any { return corpus[i] }); err != nil {
		return err
	}
	if err := writeJSONL(filepath.Join(dir, "queries.jsonl"), len(queries), func(i int) any { return queries[i] }); err != nil {
		return err
	}
	return writeQrels(filepath.Join(dir, "qrels", "test.tsv"), qrels)
}

func writeJSONL(path string, n int, get func(int) any) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("eval: create %s: %w", path, err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for i := range n {
		if err := enc.Encode(get(i)); err != nil {
			return fmt.Errorf("eval: encode %s: %w", path, err)
		}
	}
	return w.Flush()
}

func writeQrels(path string, qrels Qrels) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("eval: create qrels: %w", err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	fmt.Fprintln(w, "query-id\tcorpus-id\tscore")
	for qid, docs := range qrels {
		for docid, score := range docs {
			fmt.Fprintf(w, "%s\t%s\t%d\n", qid, docid, score)
		}
	}
	return w.Flush()
}
