// Slice Y.F — larger-graph parity scenario.
//
// Twelve nodes across the full type palette graph_surface.go's
// typeColor switch covers. Two iterations of stepLayout + draw to
// exercise the force-directed integrator (the kernel that Tier 4 in
// graph_surface_test.go exercises in isolation — here we run it
// end-to-end on a real graph).
//
// Why 2 iterations and not 30: the SSIM threshold has to stay tight
// (≥ 0.99) and force-directed dynamics accumulate floating-point
// rounding differences as iterations compound. Both backends use the
// same operator precedence and the same intrinsic math.Sqrt
// (intrinsics_math.go forwards directly to math.Sqrt), so divergence
// should be zero in principle — but pinning to 2 iterations buys
// headroom against any future intrinsic-implementation drift, and 2
// iterations is enough to exercise stepLayout's force accumulator
// (every pair of nodes pushes against every other) plus the Euler
// integration writeback (Y.C LHS-selector mutation of gVel/gPos).

package main

import (
	"testing"

	"m31labs.dev/hyphae/cmd/hypha-viz/graphsurface"
)

func TestY_F_GraphSurfaceParityLargerGraph(t *testing.T) {
	props := graphsurface.GraphProps{
		Nodes: []graphsurface.GraphNode{
			{ID: "c1", Label: "Concept 1", Type: "concept"},
			{ID: "c2", Label: "Concept 2", Type: "concept"},
			{ID: "d1", Label: "Decision 1", Type: "decision"},
			{ID: "d2", Label: "Decision 2", Type: "decision"},
			{ID: "i1", Label: "Initiative", Type: "initiative"},
			{ID: "l1", Label: "Lesson", Type: "lesson"},
			{ID: "s1", Label: "Spec", Type: "spec"},
			{ID: "p1", Label: "Plan", Type: "plan"},
			{ID: "sp1", Label: "Spore", Type: "spore"},
			{ID: "sk1", Label: "Skill", Type: "skill"},
			{ID: "pr1", Label: "Protocol", Type: "protocol"},
			{ID: "id1", Label: "Identity", Type: "identity"},
		},
		Edges: []graphsurface.GraphEdge{
			{From: "c1", To: "c2", Kind: "related"},
			{From: "c1", To: "d1", Kind: "informs"},
			{From: "d1", To: "p1", Kind: "graft"},
			{From: "p1", To: "s1", Kind: "derived_from"},
			{From: "i1", To: "p1", Kind: "drives"},
			{From: "l1", To: "d2", Kind: "informs"},
			{From: "sk1", To: "pr1", Kind: "uses"},
			{From: "id1", To: "sp1", Kind: "authored"},
		},
	}
	runParityScenario(t, parityScenario{
		Name:   "larger-graph-12nodes-8edges",
		Props:  props,
		Width:  600,
		Height: 400,
		Steps:  2,
	})
}
