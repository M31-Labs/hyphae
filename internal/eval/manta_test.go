package eval

import (
	"errors"
	"testing"
)

func TestParseMantaMetrics(t *testing.T) {
	js := []byte(`{
		"schema": "manta.embedding_retrieval_metrics.v1",
		"backend": "cuda",
		"quality": {"ndcg_at_10": 0.7543, "mrr_at_10": 0.812, "recall_at_10": 0.654, "recall_at_100": 0.876}
	}`)
	m, err := parseMantaMetrics(js)
	if err != nil {
		t.Fatalf("parseMantaMetrics: %v", err)
	}
	if m.NDCGAt10 != 0.7543 || m.MRRAt10 != 0.812 || m.RecallAt10 != 0.654 || m.RecallAt100 != 0.876 {
		t.Fatalf("parsed metrics = %+v", m)
	}
}

func TestNewMantaRunnerDegrades(t *testing.T) {
	// Whether or not manta is installed, NewMantaRunner must either succeed or
	// return ErrMantaMissing — never an unexpected error or panic.
	r, err := NewMantaRunner()
	if err != nil && !errors.Is(err, ErrMantaMissing) {
		t.Fatalf("unexpected error: %v", err)
	}
	if err == nil && r == nil {
		t.Fatalf("nil runner with nil error")
	}
}
