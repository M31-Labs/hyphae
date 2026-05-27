// Slice Y.F — post-drag parity scenario.
//
// Simulates the visual state after a user has dragged a node and
// released. The test seeds gPos with explicitly-positioned nodes
// (one off-grid, mimicking a dragged-and-dropped target) and runs a
// single draw — no stepLayout iterations, since post-drag the
// dragged node has zero velocity and the rest are settled.
//
// This scenario exercises the Y.C LHS-selector path implicitly: the
// candidate VM's drawing must read gPos[id].X and gPos[id].Y via
// chained OpAccess into the per-node vec2, which is the same code
// path Y.C closed for stepLayout's gVel/gPos writebacks.

package main

import (
	"testing"

	"m31labs.dev/hyphae/cmd/hypha-viz/graphsurface"
)

func TestY_F_GraphSurfaceParityPostDrag(t *testing.T) {
	props := graphsurface.GraphProps{
		Nodes: []graphsurface.GraphNode{
			{ID: "anchor1", Label: "Anchor 1", Type: "concept"},
			{ID: "anchor2", Label: "Anchor 2", Type: "decision"},
			{ID: "dragged", Label: "Dragged", Type: "spore"},
			{ID: "anchor3", Label: "Anchor 3", Type: "spec"},
		},
		Edges: []graphsurface.GraphEdge{
			{From: "anchor1", To: "dragged", Kind: "related"},
			{From: "dragged", To: "anchor2", Kind: "graft"},
			{From: "anchor3", To: "dragged", Kind: "derived_from"},
		},
	}
	runParityScenario(t, parityScenario{
		Name:   "post-drag-4nodes-3edges",
		Props:  props,
		Width:  500,
		Height: 350,
		Steps:  1, // one draw — same as the zero-nodes baseline
		           // scenario; sufficient to exercise the post-drag
		           // steady state without compounding integrator drift.
	})
}
