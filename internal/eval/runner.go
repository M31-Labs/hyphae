package eval

import (
	"context"
	"database/sql"
	"fmt"

	"m31labs.dev/hyphae/internal/recall"
)

// Result is the outcome of one eval run: the three retrievers' mean metrics
// over the gold queries, plus coverage and any degradation notes.
type Result struct {
	Space        string   `json:"space"`
	CorpusDocs   int      `json:"corpus_objects"`
	GoldQueries  int      `json:"gold_queries"`
	GoldCoverage float64  `json:"gold_coverage"`
	Model        string   `json:"model,omitempty"`
	HyphaeFTS5   Metrics  `json:"hyphae_fts5"`
	MantaBM25    *Metrics `json:"manta_bm25,omitempty"`
	MantaDense   *Metrics `json:"manta_dense,omitempty"`
	Notes        []string `json:"notes,omitempty"`
}

// RunOpts configures a run.
type RunOpts struct {
	Conn    *sql.DB
	SpaceID string // bare space id, e.g. "m31labs/hyphae"
	GoldDir string // dir holding queries.jsonl + qrels.tsv
	BeirDir string // working dir for the exported BEIR set (manta input)
	Model   string // path to .embedding.mll; empty → skip dense (R3)
	TopK    int    // retrieval depth (default 100)
	Split   string // BEIR qrels split name (default "test")
	Manta   MantaRunner
}

// Run always scores R1 (hyphae FTS5, in-process) and scores R2/R3 (manta BM25 /
// dense) when a MantaRunner is supplied and (for R3) a model is given. Manta or
// dense failures degrade to a note rather than aborting the run.
func Run(ctx context.Context, opts RunOpts) (Result, error) {
	if opts.TopK <= 0 {
		opts.TopK = 100
	}
	if opts.Split == "" {
		opts.Split = "test"
	}

	queries, qrels, err := LoadGold(opts.GoldDir)
	if err != nil {
		return Result{}, err
	}
	corpus, err := ExportCorpus(opts.Conn, opts.SpaceID)
	if err != nil {
		return Result{}, err
	}
	inCorpus := make(map[string]bool, len(corpus))
	for _, d := range corpus {
		inCorpus[d.ID] = true
	}

	res := Result{
		Space:       opts.SpaceID,
		CorpusDocs:  len(corpus),
		GoldQueries: len(queries),
		Model:       opts.Model,
	}

	// R1: hyphae FTS5, in-process. Filter each query's judgments to docs that
	// exist in the corpus so drift never silently deflates recall; track
	// coverage for the report.
	var sum Metrics
	var totalJudgments, resolvedJudgments int
	for _, q := range queries {
		filtered := make(map[string]int)
		for id, rel := range qrels[q.ID] {
			totalJudgments++
			if inCorpus[id] {
				filtered[id] = rel
				resolvedJudgments++
			}
		}
		ranked, err := recall.RankIDs(opts.Conn, opts.SpaceID, q.Text, opts.TopK)
		if err != nil {
			return Result{}, fmt.Errorf("eval: rank %q: %w", q.ID, err)
		}
		ids := make([]string, len(ranked))
		for i, s := range ranked {
			ids[i] = s.ID
		}
		m := Score(ids, filtered)
		sum.NDCGAt10 += m.NDCGAt10
		sum.MRRAt10 += m.MRRAt10
		sum.RecallAt10 += m.RecallAt10
		sum.RecallAt100 += m.RecallAt100
	}
	if n := len(queries); n > 0 {
		res.HyphaeFTS5 = Metrics{
			NDCGAt10:    sum.NDCGAt10 / float64(n),
			MRRAt10:     sum.MRRAt10 / float64(n),
			RecallAt10:  sum.RecallAt10 / float64(n),
			RecallAt100: sum.RecallAt100 / float64(n),
		}
	}
	if totalJudgments > 0 {
		res.GoldCoverage = float64(resolvedJudgments) / float64(totalJudgments)
		if resolvedJudgments < totalJudgments {
			res.Notes = append(res.Notes, fmt.Sprintf(
				"%d/%d gold judgments reference objects missing from the corpus (skipped from recall denominator)",
				totalJudgments-resolvedJudgments, totalJudgments))
		}
	} else {
		res.GoldCoverage = 1
	}

	// R2/R3: manta over the exported BEIR set.
	if opts.Manta == nil {
		res.Notes = append(res.Notes, "manta not available; reported R1 (hyphae FTS5) only")
		return res, nil
	}
	if err := WriteBEIR(opts.BeirDir, corpus, queries, qrels); err != nil {
		return Result{}, err
	}
	if bm25, err := opts.Manta.EvalBM25(ctx, opts.BeirDir, opts.Split, opts.TopK); err != nil {
		res.Notes = append(res.Notes, "manta bm25 (R2) failed: "+err.Error())
	} else {
		res.MantaBM25 = &bm25
	}
	if opts.Model == "" {
		res.Notes = append(res.Notes, "no --model provided; dense (R3) skipped")
	} else if dense, err := opts.Manta.EvalDense(ctx, opts.BeirDir, opts.Model, opts.Split, opts.TopK); err != nil {
		res.Notes = append(res.Notes, "manta dense (R3) failed: "+err.Error())
	} else {
		res.MantaDense = &dense
	}
	return res, nil
}
