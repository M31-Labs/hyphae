// Slice Y.F — native-Go baseline backend for the parity test.
//
// renderBaseline drives the actual graph_surface.go handlers (compiled
// Go code) against a CPU-side rasterizer wired through gosx's
// NewCanvasFromHostImpl seam. The output is an image.RGBA framebuffer
// that the parity scenario compares to the bytecode-VM candidate via
// SSIM.
//
// Why this is a valid "baseline" — the per-component WASM path
// (internal/buildsurface) also compiles graph_surface.go (via TinyGo
// for wasm/js) and runs the exact same handler bodies in a browser
// CanvasRenderingContext2D. Both backends interpret the same source.
// Native-Go execution lets the parity assertion run in CI without a
// browser dependency, while still exercising the real semantic target
// the bytecode VM must match.

package main

import (
	"math/rand"

	"m31labs.dev/gosx/engine/surface"

	"m31labs.dev/hyphae/cmd/hypha-viz/graphsurface"
)

// renderBaseline produces the native-Go reference framebuffer for the
// scenario. The flow mirrors the engine-surface runtime's mount-and-
// loop sequence — minus the FuncLit-driven StartLoop closure which Y.E
// flagged as a Phase 4 gap.
//
// Per the Y.E retrospective's Option F.b recommendation (handoff notes
// to Y.F), the animation loop is driven from the harness directly:
// the test calls stepLayout + draw N times in test code instead of
// relying on c.StartLoop(func(dt) {...}). This lets the parity
// assertion stay green even though FuncLit closures aren't yet lowered
// in the bytecode VM.
func renderBaseline(s parityScenario) (*canvasRasterizer, error) {
	// Reset graph_surface.go's package-level state — it caches gNodes,
	// gPos, etc. across invocations, which would taint repeated runs.
	graphsurface.ResetForParityTest()

	// Seed the global RNG so initPositions' random jitter is identical
	// for the candidate run.
	rand.Seed(s.Seed)

	host := newCanvasRasterizer(s.Width, s.Height)
	c := surface.NewCanvasFromHostImpl(&hostCanvasAdapter{r: host})

	// Seed state directly — skips the Mount handler's ctx.PropsInto
	// bootstrap so the parity claim covers the drawing pipeline only.
	// See SeedStateForParityTest's doc for the rationale (Mount's
	// `&props` flow requires a sharper `&x` pass-through than Y.E
	// provides).
	graphsurface.SeedStateForParityTest(s.Props, s.Width, s.Height)
	for i := 0; i < s.Steps; i++ {
		graphsurface.StepFrame(c)
	}
	return host, nil
}

// hostCanvasAdapter wraps canvasRasterizer to satisfy
// surface.HostCanvasImpl. Width/Height are stored on the rasterizer;
// the rest are pass-through.
type hostCanvasAdapter struct {
	r *canvasRasterizer
}

func (a *hostCanvasAdapter) Width() int                              { return a.r.w }
func (a *hostCanvasAdapter) Height() int                             { return a.r.h }
func (a *hostCanvasAdapter) Clear()                                  { /* not exercised by graph_surface */ }
func (a *hostCanvasAdapter) ClearRect(x, y, w, h float64)            { a.r.ClearRect(x, y, w, h) }
func (a *hostCanvasAdapter) FillRect(x, y, w, h float64)             { /* unused */ }
func (a *hostCanvasAdapter) BeginPath()                              { a.r.BeginPath() }
func (a *hostCanvasAdapter) MoveTo(x, y float64)                     { a.r.MoveTo(x, y) }
func (a *hostCanvasAdapter) LineTo(x, y float64)                     { a.r.LineTo(x, y) }
func (a *hostCanvasAdapter) Arc(x, y, rad, s, e float64)             { a.r.Arc(x, y, rad, s, e) }
func (a *hostCanvasAdapter) Stroke()                                 { a.r.Stroke() }
func (a *hostCanvasAdapter) Fill()                                   { a.r.Fill() }
func (a *hostCanvasAdapter) FillText(text string, x, y float64)      { a.r.FillText(text, x, y) }
func (a *hostCanvasAdapter) SetFillStyle(css string)                 { a.r.SetFillStyle(css) }
func (a *hostCanvasAdapter) SetStrokeStyle(css string)               { a.r.SetStrokeStyle(css) }
func (a *hostCanvasAdapter) SetLineWidth(w float64)                  { a.r.SetLineWidth(w) }
func (a *hostCanvasAdapter) SetFont(css string)                      { a.r.SetFont(css) }
func (a *hostCanvasAdapter) SetTextAlign(s string)                   { a.r.SetTextAlign(s) }
func (a *hostCanvasAdapter) Save()                                   { a.r.Save() }
func (a *hostCanvasAdapter) Restore()                                { a.r.Restore() }
func (a *hostCanvasAdapter) Translate(x, y float64)                  { a.r.Translate(x, y) }
func (a *hostCanvasAdapter) Scale(x, y float64)                      { a.r.Scale(x, y) }
func (a *hostCanvasAdapter) Rotate(rad float64)                      { /* unused; graph_surface never rotates */ }
func (a *hostCanvasAdapter) SetTransform(p, q, c, d, e, f float64)   { /* unused */ }
func (a *hostCanvasAdapter) RequestFrame()                           { /* harness drives the loop */ }
