// Slice Y.F — small-graph parity scenario.
//
// A 3-node, 2-edge graph runs both backends through a single draw
// (Steps=1 — no force-directed motion would have settled meaningfully
// in one iteration anyway; the parity claim is per-frame
// deterministic identity). This is the first scenario that exercises
// the non-trivial portions of graph_surface.go:
//
//   - composite literals (Y.A): vec2{X: ..., Y: ...} in initPositions,
//     GraphNode/GraphEdge unmarshalling from props.
//   - multi-value assignment (Y.B): _, ok := gPos[id] in initPositions.
//   - LHS selectors (Y.C): gPos[nd.ID] = vec2{...}.
//   - user-function calls (Y.D): Mount → initPositions; draw → typeColor.
//   - canvas host dispatch (Y.E): every c.MoveTo / c.LineTo / c.Arc /
//     c.SetFillStyle / etc. in draw.
//
// If SSIM ≥ 0.99 holds here, the Y.A-Y.E lowering chain matches
// native-Go semantics for the canonical engine-surface workload.

package main

import (
	"testing"

	"m31labs.dev/hyphae/cmd/hypha-viz/graphsurface"
)

func TestY_F_GraphSurfaceParitySmallGraph(t *testing.T) {
	props := graphsurface.GraphProps{
		Nodes: []graphsurface.GraphNode{
			{ID: "a", Label: "Concept A", Type: "concept"},
			{ID: "b", Label: "Decision B", Type: "decision"},
			{ID: "c", Label: "Lesson C", Type: "lesson"},
		},
		Edges: []graphsurface.GraphEdge{
			{From: "a", To: "b", Kind: "related"},
			{From: "b", To: "c", Kind: "derived_from"},
		},
	}
	runParityScenario(t, parityScenario{
		Name:   "small-graph-3nodes-2edges",
		Props:  props,
		Width:  400,
		Height: 300,
		Steps:  1,
	})
}
