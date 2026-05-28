package graft

import (
	"fmt"
	"strings"
)

// RenderDelta returns a unified-diff-ish text rendering of one FileDelta.
// For grafts the change is almost always a single contiguous insert or
// section replacement, so the renderer takes a common-prefix /
// common-suffix shortcut: it strips matching head and tail lines, then
// prints the differing middle with two lines of context on each side.
//
// New files (OldBytes == nil) print every line prefixed with `+`.
func RenderDelta(d FileDelta) string {
	if d.OldBytes == nil {
		var b strings.Builder
		fmt.Fprintf(&b, "--- /dev/null\n+++ %s\n", d.Path)
		for _, line := range strings.Split(strings.TrimRight(string(d.NewBytes), "\n"), "\n") {
			fmt.Fprintf(&b, "+%s\n", line)
		}
		return b.String()
	}

	oldLines := strings.Split(string(d.OldBytes), "\n")
	newLines := strings.Split(string(d.NewBytes), "\n")

	preLen := 0
	for preLen < len(oldLines) && preLen < len(newLines) && oldLines[preLen] == newLines[preLen] {
		preLen++
	}
	sufLen := 0
	for sufLen < len(oldLines)-preLen && sufLen < len(newLines)-preLen &&
		oldLines[len(oldLines)-1-sufLen] == newLines[len(newLines)-1-sufLen] {
		sufLen++
	}

	const ctxLines = 2
	ctxStart := preLen - ctxLines
	if ctxStart < 0 {
		ctxStart = 0
	}
	ctxEndOld := len(oldLines) - sufLen + ctxLines
	if ctxEndOld > len(oldLines) {
		ctxEndOld = len(oldLines)
	}
	ctxEndNew := len(newLines) - sufLen + ctxLines
	if ctxEndNew > len(newLines) {
		ctxEndNew = len(newLines)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\n+++ %s\n", d.Path, d.Path)
	fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n",
		ctxStart+1, ctxEndOld-ctxStart,
		ctxStart+1, ctxEndNew-ctxStart,
	)
	for i := ctxStart; i < preLen; i++ {
		fmt.Fprintf(&b, " %s\n", oldLines[i])
	}
	for i := preLen; i < len(oldLines)-sufLen; i++ {
		fmt.Fprintf(&b, "-%s\n", oldLines[i])
	}
	for i := preLen; i < len(newLines)-sufLen; i++ {
		fmt.Fprintf(&b, "+%s\n", newLines[i])
	}
	for i := len(oldLines) - sufLen; i < ctxEndOld; i++ {
		fmt.Fprintf(&b, " %s\n", oldLines[i])
	}
	return b.String()
}
