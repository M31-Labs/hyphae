package eval

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"
)

// ResultSchema is the versioned schema string for the results JSON.
const ResultSchema = "hyphae.retrieval_eval.v1"

// Decision is the go/no-go output of a run.
type Decision struct {
	Recommendation  string  `json:"recommendation"` // "build_hybrid" | "defer_embeddings"
	NDCGDelta       float64 `json:"ndcg_delta"`
	RecallAt10Delta float64 `json:"recall_at_10_delta"`
	NDCGThreshold   float64 `json:"ndcg_threshold"`
}

// Outcome bundles a run Result with its Decision and the on-disk results path.
// It is what the CLI emits (JSON) and renders (text).
type Outcome struct {
	Result     Result   `json:"result"`
	Decision   Decision `json:"decision"`
	ResultPath string   `json:"result_path,omitempty"`
}

// Decide applies the go/no-go rule: dense retrieval is justified only if it
// beats hyphae FTS5 by >= ndcgThreshold on nDCG@10 AND improves recall@10.
// With no dense result, the recommendation is always to defer.
func Decide(r Result, ndcgThreshold float64) Decision {
	d := Decision{Recommendation: "defer_embeddings", NDCGThreshold: ndcgThreshold}
	if r.MantaDense == nil {
		return d
	}
	d.NDCGDelta = r.MantaDense.NDCGAt10 - r.HyphaeFTS5.NDCGAt10
	d.RecallAt10Delta = r.MantaDense.RecallAt10 - r.HyphaeFTS5.RecallAt10
	if d.NDCGDelta >= ndcgThreshold && d.RecallAt10Delta > 0 {
		d.Recommendation = "build_hybrid"
	}
	return d
}

type resultDoc struct {
	Schema        string             `json:"schema"`
	Space         string             `json:"space"`
	Created       string             `json:"created"`
	CorpusObjects int                `json:"corpus_objects"`
	GoldQueries   int                `json:"gold_queries"`
	GoldCoverage  float64            `json:"gold_coverage"`
	Model         string             `json:"model,omitempty"`
	Retrievers    map[string]Metrics `json:"retrievers"`
	Decision      Decision           `json:"decision"`
	Notes         []string           `json:"notes,omitempty"`
}

// WriteResult writes the run result + decision to
// <spaceRoot>/.analyses/eval/<date>-retrieval.json (the space's .gitignore
// already covers .analyses/, matching the canopy analyze pattern) and returns
// the path. date is supplied by the caller (no wall-clock here, for testability).
func WriteResult(spaceRoot, date string, r Result, d Decision) (string, error) {
	retr := map[string]Metrics{"hyphae_fts5": r.HyphaeFTS5}
	if r.MantaBM25 != nil {
		retr["manta_bm25"] = *r.MantaBM25
	}
	if r.MantaDense != nil {
		retr["manta_dense"] = *r.MantaDense
	}
	doc := resultDoc{
		Schema:        ResultSchema,
		Space:         r.Space,
		Created:       date,
		CorpusObjects: r.CorpusDocs,
		GoldQueries:   r.GoldQueries,
		GoldCoverage:  r.GoldCoverage,
		Model:         r.Model,
		Retrievers:    retr,
		Decision:      d,
		Notes:         r.Notes,
	}
	dir := filepath.Join(spaceRoot, ".analyses", "eval")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("eval: mkdir results: %w", err)
	}
	path := filepath.Join(dir, date+"-retrieval.json")
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", fmt.Errorf("eval: marshal result: %w", err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("eval: write result: %w", err)
	}
	return path, nil
}

// RenderTable renders an Outcome as a human-readable table. It satisfies the
// envelope.TextRenderer signature (func(io.Writer, any) error) so `emit` can
// call it directly.
func RenderTable(w io.Writer, data any) error {
	o, ok := toOutcome(data)
	if !ok {
		return fmt.Errorf("eval: RenderTable expects Outcome, got %T", data)
	}
	fmt.Fprintf(w, "space %s · corpus %d objects · gold %d queries · coverage %.0f%%\n\n",
		o.Result.Space, o.Result.CorpusDocs, o.Result.GoldQueries, o.Result.GoldCoverage*100)

	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "retriever\tnDCG@10\tMRR@10\tR@10\tR@100")
	row := func(name string, m Metrics) {
		fmt.Fprintf(tw, "%s\t%.4f\t%.4f\t%.4f\t%.4f\n", name, m.NDCGAt10, m.MRRAt10, m.RecallAt10, m.RecallAt100)
	}
	row("hyphae_fts5", o.Result.HyphaeFTS5)
	if o.Result.MantaBM25 != nil {
		row("manta_bm25", *o.Result.MantaBM25)
	}
	if o.Result.MantaDense != nil {
		row("manta_dense", *o.Result.MantaDense)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	fmt.Fprintf(w, "\nDecision: %s (Δndcg@10=%+.4f, Δrecall@10=%+.4f, threshold %.2f)\n",
		o.Decision.Recommendation, o.Decision.NDCGDelta, o.Decision.RecallAt10Delta, o.Decision.NDCGThreshold)
	for _, n := range o.Result.Notes {
		fmt.Fprintf(w, "note: %s\n", n)
	}
	return nil
}

func toOutcome(data any) (Outcome, bool) {
	switch v := data.(type) {
	case Outcome:
		return v, true
	case *Outcome:
		return *v, true
	default:
		return Outcome{}, false
	}
}
