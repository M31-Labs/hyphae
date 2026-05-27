// Slice Y.F — shared parity scaffolding for the SSIM dogfood test.
//
// The framework has three responsibilities, each cleanly separable
// so individual scenarios stay short:
//
//   1. Define a deterministic input (parityScenario).
//   2. Render the input twice — once via native-Go graph_surface
//      handlers, once via gosx-VM bytecode lowering of the same source
//      — into two RGBA framebuffers.
//   3. Compare the framebuffers via SSIM and fail when they diverge
//      beyond the scenario-specific threshold (default 0.99).
//
// Both backends drive a single canvasRasterizer per render so the
// "shape" of canvas operations is identical when the handlers are
// semantically equivalent. The rasterizer is a small pure-Go raster
// engine sufficient to render the graph dogfood (lines, arcs, fills,
// labels) deterministically with no GPU dependency.

package main

import (
	"testing"

	"m31labs.dev/hyphae/cmd/hypha-viz/graphsurface"
)

// parityScenario is one comparable render. Authors define a scenario,
// drive both backends through runParityScenario, and the scenario
// captures any per-test overrides (canvas size, frame count, SSIM
// threshold, post-render hooks like simulated drag positions).
type parityScenario struct {
	// Name is used in the failure message + the optional artifact
	// dump (golden images get filed under testdata/<name>/).
	Name string

	// Props is the input payload — the same shape graph_surface.Mount
	// receives via ctx.PropsInto.
	Props graphsurface.GraphProps

	// Width and Height define the canvas dimensions in CSS pixels.
	// Defaults are 400 x 300 when zero.
	Width, Height int

	// Steps is how many stepLayout + draw iterations the harness runs.
	// 1 = draw once (a static snapshot — what you want for the
	// zero-nodes scenario). Higher counts settle the force-directed
	// layout; ~30 iterations usually converges for small graphs.
	Steps int

	// SSIMThreshold is the floor that the candidate-vs-baseline
	// similarity must clear. Defaults to 0.99 when zero (matches the
	// task's stated minimum bar).
	SSIMThreshold float64

	// Seed lets the test pin rand.Float64() output for initPositions
	// so both backends pick identical starting positions. Defaults
	// to 42 when zero.
	Seed int64
}

// runParityScenario is the single entry point each scenario test
// calls. Defaults apply for any zero field; both backends are driven
// through canvasRasterizer to produce comparable image.RGBA outputs.
//
// On divergence the test logs the SSIM score plus a one-line summary
// of the first mismatched canvas operation (sourced from the
// rasterizer's command log) so the failure is actionable.
//
// The function panics at scaffolding time when the framework can't
// even build the inputs — that's a test-bug, not a parity violation.
func runParityScenario(t *testing.T, s parityScenario) {
	t.Helper()
	// Defaults — keep the call sites short.
	if s.Width == 0 {
		s.Width = 400
	}
	if s.Height == 0 {
		s.Height = 300
	}
	if s.Steps == 0 {
		s.Steps = 1
	}
	if s.SSIMThreshold == 0 {
		s.SSIMThreshold = 0.99
	}
	if s.Seed == 0 {
		s.Seed = 42
	}

	baseline, baseErr := renderBaseline(s)
	if baseErr != nil {
		t.Fatalf("scenario %q: renderBaseline failed: %v", s.Name, baseErr)
	}
	candidate, candErr := renderCandidate(s)
	if candErr != nil {
		t.Fatalf("scenario %q: renderCandidate failed: %v", s.Name, candErr)
	}

	score := ssim(baseline.Image(), candidate.Image())
	if testing.Verbose() {
		t.Logf("scenario %q: SSIM = %.6f (threshold %.4f)", s.Name, score, s.SSIMThreshold)
	}
	if score < s.SSIMThreshold {
		dumpFramebuffer("testdata/parity-failures", s.Name+"-baseline", baseline.Image())
		dumpFramebuffer("testdata/parity-failures", s.Name+"-candidate", candidate.Image())
		t.Logf("scenario %q: candidate canvas host call log: %v", s.Name, lastCandidateCallLog())
		t.Errorf("scenario %q: SSIM %.4f below threshold %.4f", s.Name, score, s.SSIMThreshold)
	}
}

// lastCandidateCallLog returns the call log from the most-recent
// candidate render. A diagnostic helper: when SSIM fails, we log it
// to make "the bytecode VM never invoked draw" vs "it invoked draw
// but in a different order" cleanly distinguishable.
func lastCandidateCallLog() []string {
	if lastCandidateHost == nil {
		return nil
	}
	return lastCandidateHost.callLog
}

// lastCandidateHost retains a reference to the most-recently created
// candidate host for diagnostic logging. Tests are sequential so this
// is safe.
var lastCandidateHost *candidateCanvasHost
