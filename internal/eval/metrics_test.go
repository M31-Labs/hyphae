package eval

import (
	"math"
	"testing"
)

func almost(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestScorePerfectRanking(t *testing.T) {
	qrels := map[string]int{"d1": 2, "d2": 1}
	got := Score([]string{"d1", "d2", "d3"}, qrels)
	if !almost(got.NDCGAt10, 1.0) {
		t.Fatalf("perfect nDCG@10 = %v, want 1.0", got.NDCGAt10)
	}
	if !almost(got.RecallAt10, 1.0) {
		t.Fatalf("recall@10 = %v, want 1.0 (both relevant retrieved)", got.RecallAt10)
	}
	if !almost(got.MRRAt10, 1.0) {
		t.Fatalf("MRR@10 = %v, want 1.0 (relevant at rank 1)", got.MRRAt10)
	}
}

func TestScoreSuboptimalIsBetweenZeroAndPerfect(t *testing.T) {
	qrels := map[string]int{"d1": 2, "d2": 1}
	sub := Score([]string{"d2", "d1", "d3"}, qrels) // higher-rel doc ranked lower
	if !(sub.NDCGAt10 > 0 && sub.NDCGAt10 < 1.0) {
		t.Fatalf("suboptimal nDCG@10 = %v, want 0 < x < 1", sub.NDCGAt10)
	}
}

func TestRecallFraction(t *testing.T) {
	qrels := map[string]int{"a": 1, "b": 1, "c": 1} // 3 relevant; c never retrieved
	got := Score([]string{"a", "x", "b", "y"}, qrels)
	if !almost(got.RecallAt10, 2.0/3.0) {
		t.Fatalf("recall@10 = %v, want 2/3", got.RecallAt10)
	}
}

func TestMRRRankPosition(t *testing.T) {
	qrels := map[string]int{"a": 1, "b": 1, "c": 1}
	got := Score([]string{"x", "y", "a"}, qrels) // first relevant at rank 3
	if !almost(got.MRRAt10, 1.0/3.0) {
		t.Fatalf("MRR@10 = %v, want 1/3", got.MRRAt10)
	}
}

func TestZeroRelevant(t *testing.T) {
	got := Score([]string{"x", "y"}, map[string]int{})
	if got.NDCGAt10 != 0 || got.RecallAt10 != 0 || got.MRRAt10 != 0 || got.RecallAt100 != 0 {
		t.Fatalf("empty qrels should yield all-zero metrics, got %+v", got)
	}
}
