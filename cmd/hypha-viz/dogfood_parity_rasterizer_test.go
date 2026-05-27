// Slice Y.F — pure-Go canvas rasterizer for the parity test.
//
// canvasRasterizer is a deterministic CPU-side implementation of the
// subset of Canvas2D that graph_surface.go exercises (clear, save,
// restore, translate, scale, begin/move/line, arc, stroke, fill,
// setFillStyle, setStrokeStyle, setLineWidth, setFont, setTextAlign,
// fillText, clearRect). It draws into an image.RGBA framebuffer.
//
// Drawing is intentionally simple, not pixel-perfect against a
// "production" 2D renderer: the only requirement is bit-stability
// across the two backends (native Go and bytecode VM) when both
// produce the same canvas command sequence. SSIM=1.0 in that case;
// any divergence in the canvas command stream surfaces as SSIM<1.0
// at a magnitude proportional to the visual difference.
//
// Three design constraints:
//
//   1. **No external imaging deps.** We use only image, image/color,
//      and stdlib math. golang.org/x/image is already in hyphae's
//      go.mod (indirect) but not needed — keeps the test self-contained.
//
//   2. **Determinism over fidelity.** Anti-aliasing, sub-pixel text
//      rendering, and font shaping are deliberately omitted because
//      each would introduce platform-dependent variation that would
//      defeat the SSIM threshold. Lines are Bresenham; arcs are a
//      polyline approximation; text fills a small bounding box with
//      the current fillStyle.
//
//   3. **Affine transform stack** mirrors Canvas2D semantics: a stack
//      of 2x3 matrices, top of stack applies on draw. translate +
//      scale push composed transforms; save snapshots state; restore
//      pops.

package main

import (
	"image"
	"image/color"
	"math"
)

// canvasRasterizer implements the host-side surface.Canvas API into
// an image.RGBA framebuffer. Both backends bind one of these as the
// "c" host receiver (bytecode path) or the canvas impl (native path).
type canvasRasterizer struct {
	img *image.RGBA

	// Drawing state stack. Save/Restore push/pop.
	stack []rasterState
	state rasterState

	// Current path (sequence of polyline subpaths). BeginPath resets,
	// MoveTo opens a new subpath, LineTo extends, Arc appends a
	// polyline approximation, Stroke/Fill consume.
	subpaths [][]rasterPoint
}

// rasterState holds the transient drawing context layers can stack.
type rasterState struct {
	transform     affine2d
	fillStyle     color.RGBA
	strokeStyle   color.RGBA
	lineWidth     float64
	font          string // unused for layout — recorded for diagnostics only
	textAlign     string // "left", "center", "right"; default ""
}

type rasterPoint struct{ X, Y float64 }

// affine2d is a 2D affine matrix:
//
//	| a c e |
//	| b d f |
//	| 0 0 1 |
type affine2d struct {
	A, B, C, D, E, F float64
}

// identity is the no-op affine.
func identity() affine2d { return affine2d{A: 1, D: 1} }

// translate composes m with a translation by (tx, ty).
func (m affine2d) translate(tx, ty float64) affine2d {
	return m.multiply(affine2d{A: 1, D: 1, E: tx, F: ty})
}

// scale composes m with a scale by (sx, sy).
func (m affine2d) scale(sx, sy float64) affine2d {
	return m.multiply(affine2d{A: sx, D: sy})
}

func (m affine2d) multiply(n affine2d) affine2d {
	return affine2d{
		A: m.A*n.A + m.C*n.B,
		B: m.B*n.A + m.D*n.B,
		C: m.A*n.C + m.C*n.D,
		D: m.B*n.C + m.D*n.D,
		E: m.A*n.E + m.C*n.F + m.E,
		F: m.B*n.E + m.D*n.F + m.F,
	}
}

// apply maps a world-space point through the current transform.
func (m affine2d) apply(x, y float64) (float64, float64) {
	return m.A*x + m.C*y + m.E, m.B*x + m.D*y + m.F
}

// newCanvasRasterizer returns a rasterizer with a white background.
func newCanvasRasterizer(w, h int) *canvasRasterizer {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	bg := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, bg)
		}
	}
	return &canvasRasterizer{
		img:   img,
		state: rasterState{transform: identity(), lineWidth: 1},
	}
}

// Image returns the current framebuffer (caller-owned for read-only
// inspection; the rasterizer continues to mutate it).
func (r *canvasRasterizer) Image() *image.RGBA { return r.img }

// --- State stack ---

// Save pushes the current state. Mirrors Canvas2D.save().
func (r *canvasRasterizer) Save() { r.stack = append(r.stack, r.state) }

// Restore pops the most recent saved state. Mirrors Canvas2D.restore().
// Underflow is a no-op (matches browser leniency).
func (r *canvasRasterizer) Restore() {
	if len(r.stack) == 0 {
		return
	}
	r.state = r.stack[len(r.stack)-1]
	r.stack = r.stack[:len(r.stack)-1]
}

// Translate composes a translation onto the current transform.
func (r *canvasRasterizer) Translate(tx, ty float64) {
	r.state.transform = r.state.transform.translate(tx, ty)
}

// Scale composes a uniform scale onto the current transform.
func (r *canvasRasterizer) Scale(sx, sy float64) {
	r.state.transform = r.state.transform.scale(sx, sy)
}

// --- Styles ---

// SetFillStyle parses a CSS color string into a fill color.
func (r *canvasRasterizer) SetFillStyle(css string) {
	r.state.fillStyle = parseCSSColor(css)
}

// SetStrokeStyle parses a CSS color string into a stroke color.
func (r *canvasRasterizer) SetStrokeStyle(css string) {
	r.state.strokeStyle = parseCSSColor(css)
}

// SetLineWidth sets the stroke line width (rasterized as 1px regardless;
// thickness is recorded so future widening can be added without changing
// the call sequence).
func (r *canvasRasterizer) SetLineWidth(w float64) { r.state.lineWidth = w }

// SetFont records the font (no layout impact in this rasterizer).
func (r *canvasRasterizer) SetFont(css string) { r.state.font = css }

// SetTextAlign records the text alignment.
func (r *canvasRasterizer) SetTextAlign(a string) { r.state.textAlign = a }

// --- Clearing ---

// ClearRect fills the rect with white (background). Coordinates are in
// the current-transform space.
func (r *canvasRasterizer) ClearRect(x, y, w, h float64) {
	r.fillBox(x, y, w, h, color.RGBA{R: 255, G: 255, B: 255, A: 255})
}

// --- Path building ---

// BeginPath drops any in-flight subpaths.
func (r *canvasRasterizer) BeginPath() { r.subpaths = nil }

// MoveTo opens a new subpath at (x, y).
func (r *canvasRasterizer) MoveTo(x, y float64) {
	r.subpaths = append(r.subpaths, []rasterPoint{{X: x, Y: y}})
}

// LineTo appends (x, y) to the current subpath. With no subpath open
// it opens one (matches Canvas2D's lenient behavior).
func (r *canvasRasterizer) LineTo(x, y float64) {
	if len(r.subpaths) == 0 {
		r.MoveTo(x, y)
		return
	}
	last := len(r.subpaths) - 1
	r.subpaths[last] = append(r.subpaths[last], rasterPoint{X: x, Y: y})
}

// Arc appends a polyline approximation of an arc to the current
// subpath (32 segments is enough for visual parity at the node
// radii graph_surface uses).
func (r *canvasRasterizer) Arc(cx, cy, radius, startAngle, endAngle float64) {
	if radius <= 0 {
		return
	}
	const segs = 32
	span := endAngle - startAngle
	if math.Abs(span) < 1e-9 {
		return
	}
	if len(r.subpaths) == 0 {
		r.subpaths = append(r.subpaths, nil)
	}
	last := len(r.subpaths) - 1
	for i := 0; i <= segs; i++ {
		t := float64(i) / float64(segs)
		theta := startAngle + span*t
		px := cx + radius*math.Cos(theta)
		py := cy + radius*math.Sin(theta)
		r.subpaths[last] = append(r.subpaths[last], rasterPoint{X: px, Y: py})
	}
}

// Stroke renders each subpath as a polyline in the current stroke color.
func (r *canvasRasterizer) Stroke() {
	for _, sp := range r.subpaths {
		for i := 1; i < len(sp); i++ {
			x1, y1 := r.state.transform.apply(sp[i-1].X, sp[i-1].Y)
			x2, y2 := r.state.transform.apply(sp[i].X, sp[i].Y)
			r.drawLineBresenham(int(math.Round(x1)), int(math.Round(y1)), int(math.Round(x2)), int(math.Round(y2)), r.state.strokeStyle)
		}
	}
}

// Fill renders each subpath as a filled polygon. The implementation
// uses a scanline-fill over each subpath's screen-space bounding box.
func (r *canvasRasterizer) Fill() {
	for _, sp := range r.subpaths {
		if len(sp) < 3 {
			continue
		}
		// Transform points to screen space.
		screen := make([]rasterPoint, len(sp))
		for i, p := range sp {
			x, y := r.state.transform.apply(p.X, p.Y)
			screen[i] = rasterPoint{X: x, Y: y}
		}
		r.fillPolygon(screen, r.state.fillStyle)
	}
}

// FillText fills a small bounding box near (x, y) with the current
// fillStyle, approximating the text's visual footprint. The width is
// proportional to len(text); the height is fixed. Center alignment
// shifts left by half the width.
//
// This is not text rendering — it's a deterministic visual hash so
// labels appear in the same place across backends. Real text shaping
// would require a font file embedded in the test, which is overkill
// for parity comparison.
func (r *canvasRasterizer) FillText(text string, x, y float64) {
	if len(text) == 0 {
		return
	}
	const charW = 5.0
	const charH = 10.0
	w := charW * float64(len([]rune(text)))
	originX := x
	switch r.state.textAlign {
	case "center":
		originX -= w / 2
	case "right":
		originX -= w
	}
	originY := y - charH
	r.fillBox(originX, originY, w, charH, r.state.fillStyle)
}

// --- Internal raster helpers ---

func (r *canvasRasterizer) fillBox(x, y, w, h float64, c color.RGBA) {
	// Apply transform to corners (only translation + scale used, so
	// axis-aligned remains axis-aligned).
	x1, y1 := r.state.transform.apply(x, y)
	x2, y2 := r.state.transform.apply(x+w, y+h)
	if x2 < x1 {
		x1, x2 = x2, x1
	}
	if y2 < y1 {
		y1, y2 = y2, y1
	}
	ix1 := int(math.Floor(x1))
	iy1 := int(math.Floor(y1))
	ix2 := int(math.Ceil(x2))
	iy2 := int(math.Ceil(y2))
	bounds := r.img.Bounds()
	if ix1 < bounds.Min.X {
		ix1 = bounds.Min.X
	}
	if iy1 < bounds.Min.Y {
		iy1 = bounds.Min.Y
	}
	if ix2 > bounds.Max.X {
		ix2 = bounds.Max.X
	}
	if iy2 > bounds.Max.Y {
		iy2 = bounds.Max.Y
	}
	for yy := iy1; yy < iy2; yy++ {
		for xx := ix1; xx < ix2; xx++ {
			r.img.SetRGBA(xx, yy, c)
		}
	}
}

// drawLineBresenham draws a 1-pixel line between two integer points.
func (r *canvasRasterizer) drawLineBresenham(x0, y0, x1, y1 int, c color.RGBA) {
	dx := x1 - x0
	if dx < 0 {
		dx = -dx
	}
	dy := y1 - y0
	if dy < 0 {
		dy = -dy
	}
	sx := 1
	if x0 >= x1 {
		sx = -1
	}
	sy := 1
	if y0 >= y1 {
		sy = -1
	}
	err := dx - dy
	bounds := r.img.Bounds()
	for {
		if x0 >= bounds.Min.X && x0 < bounds.Max.X && y0 >= bounds.Min.Y && y0 < bounds.Max.Y {
			r.img.SetRGBA(x0, y0, c)
		}
		if x0 == x1 && y0 == y1 {
			break
		}
		e2 := 2 * err
		if e2 > -dy {
			err -= dy
			x0 += sx
		}
		if e2 < dx {
			err += dx
			y0 += sy
		}
	}
}

// fillPolygon fills the polygon defined by pts using scanline rasterization.
func (r *canvasRasterizer) fillPolygon(pts []rasterPoint, c color.RGBA) {
	if len(pts) < 3 {
		return
	}
	// Compute y-bounds.
	minY, maxY := pts[0].Y, pts[0].Y
	for _, p := range pts {
		if p.Y < minY {
			minY = p.Y
		}
		if p.Y > maxY {
			maxY = p.Y
		}
	}
	bounds := r.img.Bounds()
	yStart := int(math.Floor(minY))
	yEnd := int(math.Ceil(maxY))
	if yStart < bounds.Min.Y {
		yStart = bounds.Min.Y
	}
	if yEnd > bounds.Max.Y {
		yEnd = bounds.Max.Y
	}
	for y := yStart; y < yEnd; y++ {
		// Find intersections of horizontal scanline at y+0.5 with polygon edges.
		yScan := float64(y) + 0.5
		xs := []float64{}
		for i := 0; i < len(pts); i++ {
			j := (i + 1) % len(pts)
			y1 := pts[i].Y
			y2 := pts[j].Y
			if (y1 <= yScan && y2 > yScan) || (y2 <= yScan && y1 > yScan) {
				t := (yScan - y1) / (y2 - y1)
				x := pts[i].X + t*(pts[j].X-pts[i].X)
				xs = append(xs, x)
			}
		}
		// Sort intersection x values (small list; insertion sort).
		for i := 1; i < len(xs); i++ {
			for j := i; j > 0 && xs[j-1] > xs[j]; j-- {
				xs[j-1], xs[j] = xs[j], xs[j-1]
			}
		}
		// Fill pairs.
		for i := 0; i+1 < len(xs); i += 2 {
			x1 := int(math.Floor(xs[i]))
			x2 := int(math.Ceil(xs[i+1]))
			if x1 < bounds.Min.X {
				x1 = bounds.Min.X
			}
			if x2 > bounds.Max.X {
				x2 = bounds.Max.X
			}
			for x := x1; x < x2; x++ {
				r.img.SetRGBA(x, y, c)
			}
		}
	}
}

// --- CSS color parsing ---

// parseCSSColor accepts the subset of CSS color syntax graph_surface.go
// uses: 7- or 9-char hex ("#7b5c3a", "#7b5c3acc") and rgba() functional
// notation ("rgba(120,100,75,0.25)"). Anything else falls back to opaque
// black.
func parseCSSColor(s string) color.RGBA {
	if len(s) >= 7 && s[0] == '#' {
		r := hex2(s[1:3])
		g := hex2(s[3:5])
		b := hex2(s[5:7])
		a := uint8(255)
		if len(s) >= 9 {
			a = hex2(s[7:9])
		}
		return color.RGBA{R: r, G: g, B: b, A: a}
	}
	if len(s) > 5 && s[:5] == "rgba(" && s[len(s)-1] == ')' {
		body := s[5 : len(s)-1]
		var r, g, b uint8
		var alpha float64
		var i int
		for f := 0; f < 4; f++ {
			// Skip whitespace.
			for i < len(body) && (body[i] == ' ' || body[i] == '\t') {
				i++
			}
			start := i
			for i < len(body) && body[i] != ',' {
				i++
			}
			tok := body[start:i]
			i++ // skip ','
			switch f {
			case 0:
				r = uint8(parseInt(tok))
			case 1:
				g = uint8(parseInt(tok))
			case 2:
				b = uint8(parseInt(tok))
			case 3:
				alpha = parseFloat(tok)
			}
		}
		a := uint8(math.Round(alpha * 255))
		return color.RGBA{R: r, G: g, B: b, A: a}
	}
	return color.RGBA{A: 255}
}

func hex2(s string) uint8 {
	if len(s) < 2 {
		return 0
	}
	return hex1(s[0])<<4 | hex1(s[1])
}

func hex1(b byte) uint8 {
	switch {
	case b >= '0' && b <= '9':
		return uint8(b - '0')
	case b >= 'a' && b <= 'f':
		return uint8(b - 'a' + 10)
	case b >= 'A' && b <= 'F':
		return uint8(b - 'A' + 10)
	}
	return 0
}

func parseInt(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			continue
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func parseFloat(s string) float64 {
	var n float64
	var fracDiv float64 = 1
	frac := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '.':
			frac = true
		case c >= '0' && c <= '9':
			if frac {
				fracDiv *= 10
				n += float64(c-'0') / fracDiv
			} else {
				n = n*10 + float64(c-'0')
			}
		}
	}
	return n
}
