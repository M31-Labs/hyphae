package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

// ErrMantaMissing is returned when the manta binary is not on PATH. Callers
// degrade to R1-only (hyphae FTS5) rather than failing the whole run.
var ErrMantaMissing = errors.New("eval: manta binary not found on PATH")

// MantaRunner scores a BEIR directory with manta's dense embedder and BM25
// baseline. It is an interface so the runner can be unit-tested with a fake
// (no manta install required).
type MantaRunner interface {
	EvalDense(ctx context.Context, beirDir, model, split string, topK int) (Metrics, error)
	EvalBM25(ctx context.Context, beirDir, split string, topK int) (Metrics, error)
}

// execMantaRunner shells out to the manta CLI on PATH.
type execMantaRunner struct{ bin string }

// NewMantaRunner resolves the manta binary on PATH, returning ErrMantaMissing
// if it is absent.
func NewMantaRunner() (MantaRunner, error) {
	bin, err := exec.LookPath("manta")
	if err != nil {
		return nil, ErrMantaMissing
	}
	return &execMantaRunner{bin: bin}, nil
}

func (r *execMantaRunner) EvalDense(ctx context.Context, beirDir, model, split string, topK int) (Metrics, error) {
	// manta eval-retrieval [flags] <artifact.mll> <beir-dir>  (flags before positionals)
	return r.runEval(ctx, "eval-retrieval",
		[]string{"--top-k", strconv.Itoa(topK), "--split", split, model, beirDir})
}

func (r *execMantaRunner) EvalBM25(ctx context.Context, beirDir, split string, topK int) (Metrics, error) {
	return r.runEval(ctx, "eval-retrieval-bm25",
		[]string{"--top-k", strconv.Itoa(topK), "--split", split, beirDir})
}

func (r *execMantaRunner) runEval(ctx context.Context, sub string, tailArgs []string) (Metrics, error) {
	tmp, err := os.CreateTemp("", "manta-metrics-*.json")
	if err != nil {
		return Metrics{}, fmt.Errorf("eval: temp metrics file: %w", err)
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	args := append([]string{sub, "--metrics-json", tmp.Name()}, tailArgs...)
	cmd := exec.CommandContext(ctx, r.bin, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return Metrics{}, fmt.Errorf("eval: manta %s: %w: %s", sub, err, string(out))
	}

	b, err := os.ReadFile(tmp.Name())
	if err != nil {
		return Metrics{}, fmt.Errorf("eval: read manta metrics: %w", err)
	}
	return parseMantaMetrics(b)
}

// parseMantaMetrics extracts the quality block from a manta
// `manta.embedding_retrieval_metrics.v1` JSON document.
func parseMantaMetrics(b []byte) (Metrics, error) {
	var doc struct {
		Quality struct {
			NDCGAt10    float64 `json:"ndcg_at_10"`
			MRRAt10     float64 `json:"mrr_at_10"`
			RecallAt10  float64 `json:"recall_at_10"`
			RecallAt100 float64 `json:"recall_at_100"`
		} `json:"quality"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return Metrics{}, fmt.Errorf("eval: parse manta metrics json: %w", err)
	}
	return Metrics{
		NDCGAt10:    doc.Quality.NDCGAt10,
		MRRAt10:     doc.Quality.MRRAt10,
		RecallAt10:  doc.Quality.RecallAt10,
		RecallAt100: doc.Quality.RecallAt100,
	}, nil
}
