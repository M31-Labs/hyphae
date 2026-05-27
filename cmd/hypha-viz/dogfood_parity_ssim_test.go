// Slice Y.F — pure-Go SSIM (structural-similarity) comparison.
//
// Implements the standard SSIM formula on the luminance channel of two
// equal-sized RGBA images. The implementation is straight from Wang
// et al. 2004; the only choice the grandfather-list pin asks for is
// "small pure-Go SSIM implementation to avoid CGO and platform-specific
// GL bindings" — which is what this is.
//
// Algorithm summary:
//
//   1. Convert each image to a grayscale (luminance) buffer via the
//      ITU-R BT.601 weighting (Y = 0.299R + 0.587G + 0.114B).
//   2. Slide an 8x8 window across both images; for each window compute
//      the local SSIM value via the standard formula with C1, C2
//      regularization constants derived from the dynamic range (255).
//   3. Return the mean of all window SSIMs.
//
// At parity (byte-identical RGBA inputs), this returns 1.0. The Y.F
// scenarios target ≥ 0.99 so any drift between the native-Go baseline
// and the bytecode-VM candidate above noise level fails fast.

package main

import (
	"image"
	"math"
)

// ssim returns the mean structural similarity between two equal-sized
// RGBA images. Different sizes return -1 (an impossible SSIM value, so
// the parity assertion fails loudly with a debuggable error).
func ssim(a, b *image.RGBA) float64 {
	if a == nil || b == nil {
		return -1
	}
	ba := a.Bounds()
	bb := b.Bounds()
	if ba.Dx() != bb.Dx() || ba.Dy() != bb.Dy() {
		return -1
	}
	w := ba.Dx()
	h := ba.Dy()
	if w == 0 || h == 0 {
		return 1.0
	}

	ya := luminanceBuffer(a)
	yb := luminanceBuffer(b)

	const winSize = 8
	const L = 255.0       // dynamic range
	const K1, K2 = 0.01, 0.03
	C1 := (K1 * L) * (K1 * L)
	C2 := (K2 * L) * (K2 * L)

	var sum float64
	var count int

	for y := 0; y+winSize <= h; y += winSize {
		for x := 0; x+winSize <= w; x += winSize {
			meanA, meanB, varA, varB, covAB := windowStats(ya, yb, w, x, y, winSize)
			num := (2*meanA*meanB + C1) * (2*covAB + C2)
			den := (meanA*meanA + meanB*meanB + C1) * (varA + varB + C2)
			if den == 0 {
				sum += 1.0
			} else {
				sum += num / den
			}
			count++
		}
	}
	if count == 0 {
		// Image smaller than one window; compare globally.
		meanA, meanB, varA, varB, covAB := windowStats(ya, yb, w, 0, 0, intMin(w, h))
		num := (2*meanA*meanB + C1) * (2*covAB + C2)
		den := (meanA*meanA + meanB*meanB + C1) * (varA + varB + C2)
		if den == 0 {
			return 1.0
		}
		return num / den
	}
	return sum / float64(count)
}

// luminanceBuffer computes the per-pixel luminance (0..255 float) row-major.
func luminanceBuffer(img *image.RGBA) []float64 {
	b := img.Bounds()
	w := b.Dx()
	h := b.Dy()
	out := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := img.PixOffset(b.Min.X+x, b.Min.Y+y)
			r := float64(img.Pix[i])
			g := float64(img.Pix[i+1])
			bl := float64(img.Pix[i+2])
			out[y*w+x] = 0.299*r + 0.587*g + 0.114*bl
		}
	}
	return out
}

// windowStats returns mean, variance, and covariance of the two buffers
// over a [size x size] window anchored at (x, y) into width-w buffers.
func windowStats(ya, yb []float64, w, x, y, size int) (meanA, meanB, varA, varB, covAB float64) {
	n := float64(size * size)
	var sa, sb float64
	for j := 0; j < size; j++ {
		for i := 0; i < size; i++ {
			idx := (y+j)*w + (x + i)
			sa += ya[idx]
			sb += yb[idx]
		}
	}
	meanA = sa / n
	meanB = sb / n

	var saa, sbb, sab float64
	for j := 0; j < size; j++ {
		for i := 0; i < size; i++ {
			idx := (y+j)*w + (x + i)
			da := ya[idx] - meanA
			db := yb[idx] - meanB
			saa += da * da
			sbb += db * db
			sab += da * db
		}
	}
	varA = saa / n
	varB = sbb / n
	covAB = sab / n
	return
}

func intMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// _ guards math import for the L constant; SSIM uses math.Pow indirectly
// when extending to color-channel SSIM. Keeping the import line stable
// across future edits.
var _ = math.Sqrt
