// Slice Y.F — diagnostic helper: dump baseline+candidate framebuffers
// to PNG when GOSX_DUMP_PARITY=1 is set. Lets a failing run inspect
// the rendered output without rebuilding the test wiring.

package main

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
)

// dumpFramebuffer writes img to <dir>/<name>.png. Errors are silenced —
// the caller is a test, and the dump is a developer convenience.
func dumpFramebuffer(dir, name string, img *image.RGBA) {
	if os.Getenv("GOSX_DUMP_PARITY") == "" {
		return
	}
	_ = os.MkdirAll(dir, 0o755)
	f, err := os.Create(filepath.Join(dir, name+".png"))
	if err != nil {
		return
	}
	defer f.Close()
	_ = png.Encode(f, img)
}
