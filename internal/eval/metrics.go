// Package eval implements the retrieval-quality eval harness behind
// `hypha eval retrieval`: it scores hyphae's production FTS5 retriever against
// manta BM25 and manta dense on a human-labeled gold set. See
// spec.retrieval-eval-harness in the m31labs/hyphae space.
package eval

import (
	"math"
	"sort"
)

// Metrics are the standard IR retrieval-quality measures for one query (or the
// mean across queries). All are in [0,1]; higher is better.
type Metrics struct {
	NDCGAt10    float64 `json:"ndcg_at_10"`
	MRRAt10     float64 `json:"mrr_at_10"`
	RecallAt10  float64 `json:"recall_at_10"`
	RecallAt100 float64 `json:"recall_at_100"`
}

// Score computes all metrics for one query's ranked object ids against graded
// relevance judgments (id -> relevance; only rel > 0 counts as relevant). The
// recall denominator is the count of positive judgments in qrels, so callers
// that drop unresolvable judgments (corpus drift) simply pass a smaller qrels
// map and recall is not silently deflated.
func Score(ranked []string, qrels map[string]int) Metrics {
	return Metrics{
		NDCGAt10:    ndcgAt(ranked, qrels, 10),
		MRRAt10:     mrrAt(ranked, qrels, 10),
		RecallAt10:  recallAt(ranked, qrels, 10),
		RecallAt100: recallAt(ranked, qrels, 100),
	}
}

// gain is the standard nDCG gain function 2^rel - 1.
func gain(rel int) float64 {
	if rel <= 0 {
		return 0
	}
	return math.Exp2(float64(rel)) - 1
}

func dcgAt(ranked []string, qrels map[string]int, k int) float64 {
	var dcg float64
	for i, id := range ranked {
		if i >= k {
			break
		}
		if g := gain(qrels[id]); g > 0 {
			dcg += g / math.Log2(float64(i+2)) // rank i is 1-indexed → log2(rank+1)
		}
	}
	return dcg
}

func idcgAt(qrels map[string]int, k int) float64 {
	rels := make([]int, 0, len(qrels))
	for _, r := range qrels {
		if r > 0 {
			rels = append(rels, r)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(rels)))
	var idcg float64
	for j, r := range rels {
		if j >= k {
			break
		}
		idcg += gain(r) / math.Log2(float64(j+2))
	}
	return idcg
}

func ndcgAt(ranked []string, qrels map[string]int, k int) float64 {
	idcg := idcgAt(qrels, k)
	if idcg == 0 {
		return 0
	}
	return dcgAt(ranked, qrels, k) / idcg
}

func recallAt(ranked []string, qrels map[string]int, k int) float64 {
	total := 0
	for _, r := range qrels {
		if r > 0 {
			total++
		}
	}
	if total == 0 {
		return 0
	}
	found := 0
	for i, id := range ranked {
		if i >= k {
			break
		}
		if qrels[id] > 0 {
			found++
		}
	}
	return float64(found) / float64(total)
}

func mrrAt(ranked []string, qrels map[string]int, k int) float64 {
	for i, id := range ranked {
		if i >= k {
			break
		}
		if qrels[id] > 0 {
			return 1.0 / float64(i+1)
		}
	}
	return 0
}
