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

import "m31labs.dev/gosx/engine/surface"

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
func StepFrame(c *surface.Canvas) {
	if stepLayout(1.0) || gDrag.Active {
		draw(c)
	}
}
