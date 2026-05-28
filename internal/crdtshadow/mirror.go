package crdtshadow

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"m31labs.dev/hyphae/internal/types"
)

// Package-level mirror helpers wrap the Default registry so call sites
// in cmd/hypha and internal/mcp stay one-line. All helpers are
// **best-effort**: they log warnings to stderr but never return errors
// the way the underlying mutation does, because the shadow is a
// side-channel and a shadow failure should never unwind a successful
// canonical write.
//
// Set Verbose to control whether warnings get logged; off by default
// keeps test output clean.

// Verbose toggles the stderr warning when a mirror call fails.
var Verbose atomic.Bool

func warn(format string, args ...any) {
	if !Verbose.Load() {
		return
	}
	fmt.Fprintf(os.Stderr, "warn: shadow: "+format+"\n", args...)
}

// MirrorReceipt records r into the shadow for r.SpaceID. No-op when
// r.SpaceID is empty.
func MirrorReceipt(installRoot string, r types.Receipt) {
	if r.SpaceID == "" {
		return
	}
	s, err := openShadowForURI(installRoot, r.SpaceID)
	if err != nil {
		warn("receipt %s: %v", r.ID, err)
		return
	}
	if err := s.RecordReceipt(r); err != nil {
		warn("receipt %s: %v", r.ID, err)
	}
}

// MirrorEdge records e under the given spaceURI.
func MirrorEdge(installRoot, spaceURI string, e types.Edge) {
	if spaceURI == "" {
		return
	}
	s, err := openShadowForURI(installRoot, spaceURI)
	if err != nil {
		warn("edge %s: %v", e.ID, err)
		return
	}
	if err := s.RecordEdge(e); err != nil {
		warn("edge %s: %v", e.ID, err)
	}
}

// MirrorSpore records the full spore (or its current state) using the
// canonical SporeSummary fields.
func MirrorSpore(installRoot string, sp types.Spore) {
	if sp.SpaceID == "" {
		return
	}
	s, err := openShadowForURI(installRoot, sp.SpaceID)
	if err != nil {
		warn("spore %s: %v", sp.ID, err)
		return
	}
	sum := SporeSummary{
		ID:          sp.ID,
		SpaceID:     sp.SpaceID,
		Status:      sp.Status,
		Path:        sp.FilePath,
		ContentHash: sp.ContentHash,
		AgentID:     sp.AgentID,
	}
	if !sp.SubmittedAt.IsZero() {
		sum.SubmittedAt = sp.SubmittedAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	if err := s.RecordSpore(sum); err != nil {
		warn("spore %s: %v", sp.ID, err)
	}
}

// MirrorSporeStatus mirrors a status flip; used by the review path
// which only has the spore id + new status in scope.
func MirrorSporeStatus(installRoot, spaceURI, sporeID, newStatus string) {
	if spaceURI == "" || sporeID == "" {
		return
	}
	s, err := openShadowForURI(installRoot, spaceURI)
	if err != nil {
		warn("spore-status %s: %v", sporeID, err)
		return
	}
	if err := s.RecordSporeStatus(sporeID, newStatus); err != nil {
		warn("spore-status %s: %v", sporeID, err)
	}
}

// MirrorTrace records a trace open/tick/done; the caller passes the
// post-mutation Trace as types.Trace.
func MirrorTrace(installRoot string, t types.Trace) {
	if t.SpaceID == "" {
		return
	}
	s, err := openShadowForURI(installRoot, t.SpaceID)
	if err != nil {
		warn("trace %s: %v", t.ID, err)
		return
	}
	sum := TraceSummary{
		ID:          t.ID,
		SpaceID:     t.SpaceID,
		AgentID:     t.AgentID,
		Status:      t.Status,
		TaskRef:     t.TaskRef,
		Phase:       t.Phase,
		LinkedSpore: t.LinkedSpore,
		FilePath:    t.FilePath,
	}
	if !t.Started.IsZero() {
		sum.Started = t.Started.UTC().Format("2006-01-02T15:04:05Z")
	}
	if !t.LastTick.IsZero() {
		sum.LastTick = t.LastTick.UTC().Format("2006-01-02T15:04:05Z")
	}
	for _, tick := range t.Ticks {
		sum.Ticks = append(sum.Ticks, TickSummary{
			At:      tick.At.UTC().Format("2006-01-02T15:04:05Z"),
			Message: tick.Message,
		})
	}
	if err := s.RecordTrace(sum); err != nil {
		warn("trace %s: %v", t.ID, err)
	}
}

// MirrorCanonical reads each path from disk and records its post-write
// bytes into the shadow for spaceURI. Called from graft post-success.
func MirrorCanonical(installRoot, spaceURI string, paths []string) {
	if spaceURI == "" || len(paths) == 0 {
		return
	}
	s, err := openShadowForURI(installRoot, spaceURI)
	if err != nil {
		warn("canonical: %v", err)
		return
	}
	for _, p := range paths {
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			warn("canonical read %s: %v", p, rerr)
			continue
		}
		if err := s.RecordCanonicalWrite(p, data); err != nil {
			warn("canonical record %s: %v", p, err)
		}
	}
}

// SpaceURIToPath converts hypha://<authority>/<name> to
// <installRoot>/spaces/<authority>-<name>. Strips any anchor.
func SpaceURIToPath(installRoot, spaceURI string) (string, error) {
	rest := strings.TrimPrefix(spaceURI, "hypha://")
	if idx := strings.LastIndex(rest, "#"); idx >= 0 {
		rest = rest[:idx]
	}
	rest = strings.TrimRight(rest, "/")
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 {
		return "", fmt.Errorf("space URI must have authority/name, got %q", spaceURI)
	}
	dir := fmt.Sprintf("%s-%s", parts[0], parts[1])
	return filepath.Join(installRoot, "spaces", dir), nil
}

func openShadowForURI(installRoot, spaceURI string) (*Shadow, error) {
	spaceRoot, err := SpaceURIToPath(installRoot, spaceURI)
	if err != nil {
		return nil, err
	}
	return Default.Get(spaceRoot, spaceURI)
}
