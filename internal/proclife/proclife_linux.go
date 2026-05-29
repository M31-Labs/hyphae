//go:build linux

// Package proclife keeps long-running daemons from outliving the session
// that spawned them. A `hypha hub serve` / `hypha mcp serve` orphaned by a
// crashed Claude session would otherwise linger holding every space's CRDT
// doc in memory — the node-`chi` 2026-05-28 OOM was a ~22 GB orphaned hypha
// daemon.
package proclife

import "golang.org/x/sys/unix"

// DieWithParent asks the kernel to send this process SIGTERM the moment its
// parent dies (PR_SET_PDEATHSIG). `hub serve` traps SIGTERM and shuts down
// gracefully; `mcp serve` is read-only and terminates cleanly on it. Linux
// only — a no-op elsewhere.
//
// Inherent race: if the parent already died between fork and this call the
// signal never fires. Callers that block on the parent's stdin (mcp serve)
// get EOF instead, and long-lived listeners should pair this with an idle
// timeout as a backstop.
func DieWithParent() error {
	return unix.Prctl(unix.PR_SET_PDEATHSIG, uintptr(unix.SIGTERM), 0, 0, 0)
}
