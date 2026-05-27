// Slice Y.F — SSIM parity test for the hyphae graph dogfood surface.
//
// This test is the close-out evidence for ADR 0005's
// "buildsurface coexistence window" exit criterion §2: the bytecode-VM
// lowering of cmd/hypha-viz/graphsurface/graph_surface.go renders
// pixel-identically (SSIM ≥ 0.99 across deterministic scenarios) to the
// native-Go reference rendering of the same handlers.
//
// Why "native Go" rather than "internal/buildsurface WASM" as the
// baseline?  The per-component WASM path compiles graph_surface.go with
// TinyGo for the wasm/js target — that requires a browser to render and
// is impractical in a Go test harness. Native Go invocation is the
// semantically-equivalent baseline: graph_surface.go is just Go code,
// and the bytecode VM is supposed to interpret the same source to the
// same canvas operations. SSIM parity proves the lowerer + VM produce
// the same render the author would have gotten compiling the source
// natively — which is exactly what the WASM path proves at runtime.
// (The grandfather list's note about a hardware/driver pin for the
// SSIM threshold motivated the choice: a pure-Go rasterizer with no
// GPU dependency makes the test machine-class-independent.)
//
// Handoff context — Y.E retrospective, "Handoff notes for Y.F":
//
//   - Bind canvas via vm.BindHost("c", canvasAdapter) and context via
//     vm.BindHost("ctx", contextAdapter). A switch-statement adapter
//     was the recommendation over reflect for explicitness and a clear
//     diagnostic on a missing method.
//
//   - The FuncLit closure in Mount's c.StartLoop is the one residual
//     gap after Y.E and remains deferred to Phase 4. Per Y.E's
//     recommendation Option F.b, this test drives the animation loop
//     directly from the test harness (call stepLayout + draw N times
//     in test code), side-stepping StartLoop entirely. graph_surface.go
//     stays untouched.
//
//   - The 90-day ADR 0005 deletion clock starts at this test's first
//     green run. Documented in the spec gap close-out + ADR 0005
//     addendum (both hyphae-space, NOT in this repo).

package main

import (
	"testing"

	"m31labs.dev/hyphae/cmd/hypha-viz/graphsurface"
)

// TestY_F_GraphSurfaceParityZeroNodes is the simplest scenario: an
// empty props payload. Both backends should produce an identical
// canvas state (background only, no nodes, no edges) and SSIM = 1.0.
//
// This is the first failing test of Y.F's TDD chain. It compiles but
// fails because renderBaseline and renderCandidate are not yet
// implemented.
func TestY_F_GraphSurfaceParityZeroNodes(t *testing.T) {
	props := graphsurface.GraphProps{
		Nodes: nil,
		Edges: nil,
	}
	scenario := parityScenario{
		Name:   "zero-nodes",
		Props:  props,
		Width:  400,
		Height: 300,
		Steps:  1, // draw once; no force-directed motion when no nodes
	}
	runParityScenario(t, scenario)
}
