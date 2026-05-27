// Slice Y.F — exported helpers the dogfood_parity_test consumes.
//
// The parity test in cmd/hypha-viz needs three things that
// graph_surface.go's production lifecycle (Mount → StartLoop closure
// → step) makes opaque:
//
//   1. A way to reset the package-level state (gNodes, gPos, gVel,
//      gDrag, gTx, gSelected) between scenarios so each run starts
//      from a clean slate.
//
//   2. A way to drive one stepLayout + draw iteration without going
//      through StartLoop's FuncLit closure (the residual gap from
//      Y.E's exit report — closures aren't yet lowered by the
//      bytecode VM). Per the Y.E retrospective's Option F.b, the
//      harness drives the loop from test code.
//
//   3. Both exports must use the existing private symbols (stepLayout,
//      draw) — duplicating the logic would defeat the parity claim.
//      So this file is a thin re-export layer, no new logic.
//
// The file is named with the _helpers suffix (not _test.go) because
// the symbols must be visible from another test binary
// (cmd/hypha-viz/dogfood_parity_test.go). Go's test packaging only
// exposes _test.go symbols within the same package; for cross-package
// test consumption the helpers must live in a regular source file.
//
// The Mount export name shadows the existing public Mount handler —
// it's already exported as Mount(ctx, c) per the engine-surface
// authoring contract. We don't re-declare it here.

package graphsurface

import (
	"math"

	"m31labs.dev/gosx/engine/surface"
)

// ResetForParityTest clears every package-level state slot so the next
// Mount call starts from the zero state. Safe to call repeatedly.
//
// Production paths never call this — the graph surface is mounted
// exactly once per page load. The parity test calls it before each
// scenario so per-scenario behavior is reproducible.
func ResetForParityTest() {
	gNodes = nil
	gEdges = nil
	gPos = map[string]vec2{}
	gVel = map[string]vec2{}
	gSelected = ""
	gTx = mat2d{Scale: 1.0}
	gDrag = dragState{}
}

// StepFrame runs one stepLayout + draw iteration. Mirrors the body of
// Mount's StartLoop closure exactly:
//
//	if stepLayout(dt) || gDrag.Active {
//	    draw(c)
//	}
//
// Driving this from the parity test harness lets the harness pin a
// deterministic number of frames (Y.E.handoff Option F.b — avoids the
// FuncLit closure gap that's still open in the bytecode VM).
//
// dt is passed as 1.0 (one tick per frame); stepLayout only uses dt
// today as a placeholder for future time-based motion, so the choice
// has no observable effect on the layout it produces.
//
// IMPORTANT — StepFrame always calls draw, regardless of stepLayout's
// "moving" return. The parity test compares per-frame canvas output
// directly; if one backend skipped draw on a converged frame and the
// other didn't, SSIM would diverge for layout reasons, not drawing
// reasons. Calling draw unconditionally normalizes that.
func StepFrame(c *surface.Canvas) {
	_ = stepLayout(1.0)
	draw(c)
}

// SeedStateForParityTest is the bootstrap step Mount would have
// performed in production: bind props.Nodes / props.Edges into the
// package-level signal slots and seed initial positions for every
// node. The parity test calls this on the baseline side so the
// baseline matches the candidate's "post-bootstrap" state — the
// candidate can't run Mount end-to-end yet because Mount's
// ctx.PropsInto(&props) flow depends on a sharper `&x` pass-through
// than Y.E provides (the receiver's Fields map is nil when props is
// a fresh `var props GraphProps`, so host-side mutation can't
// propagate back to the local). Skipping Mount in both backends
// preserves the parity claim for the drawing pipeline, which is the
// portion the SSIM threshold actually measures.
//
// Positions are seeded DETERMINISTICALLY (no rand.Float64() jitter)
// because the gosx VM keeps its own *rand.Rand source separate from
// Go's global — using rand.Float64() in initPositions would diverge
// the two backends on otherwise-identical scenarios. The deterministic
// seed places each node on a regular ring matching initPositions'
// shape without the jitter term.
func SeedStateForParityTest(props GraphProps, width, height int) {
	gNodes = props.Nodes
	gEdges = props.Edges
	gPos = map[string]vec2{}
	gVel = map[string]vec2{}
	seedDeterministicPositions(width, height)
}

// seedDeterministicPositions places nodes on a regular ring around the
// canvas center. Same shape as initPositions but with the rand-jitter
// term zeroed out so both backends start from byte-identical state.
func seedDeterministicPositions(w, h int) {
	fw, fh := float64(w), float64(h)
	if fw == 0 {
		fw = 800
	}
	if fh == 0 {
		fh = 600
	}
	n := len(gNodes)
	r := fw
	if fh < fw {
		r = fh
	}
	r *= 0.3
	denom := float64(n)
	if denom < 1 {
		denom = 1
	}
	for i, nd := range gNodes {
		if _, ok := gPos[nd.ID]; !ok {
			angle := float64(i) / denom * 2 * math.Pi
			gPos[nd.ID] = vec2{
				X: fw/2 + r*math.Cos(angle),
				Y: fh/2 + r*math.Sin(angle),
			}
		}
		if _, ok := gVel[nd.ID]; !ok {
			gVel[nd.ID] = vec2{}
		}
	}
}
