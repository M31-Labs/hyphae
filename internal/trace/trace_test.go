package trace_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/hyphae/internal/trace"
	"m31labs.dev/hyphae/internal/types"
)

func tempSpace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// Mirror the canonical space layout enough for trace storage.
	if err := os.MkdirAll(filepath.Join(dir, "inbox", "agents"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	return dir
}

func TestStart_CreatesFileWithFrontmatter(t *testing.T) {
	space := tempSpace(t)
	tr, err := trace.Start(trace.StartOpts{
		SpaceRoot: space,
		SpaceID:   "hypha://m31labs/test",
		AgentID:   "agent://claude-code/walnut",
		TaskRef:   "task#25",
		Phase:     "writing surface",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !strings.HasPrefix(tr.ID, "trace.") {
		t.Errorf("ID = %q, want prefix 'trace.'", tr.ID)
	}
	if tr.Status != types.TraceStatusOpen {
		t.Errorf("Status = %q, want %q", tr.Status, types.TraceStatusOpen)
	}
	if tr.Started.IsZero() || tr.LastTick.IsZero() {
		t.Errorf("Started or LastTick zero")
	}
	if _, err := os.Stat(tr.FilePath); err != nil {
		t.Errorf("trace file not on disk at %s: %v", tr.FilePath, err)
	}
	content, _ := os.ReadFile(tr.FilePath)
	for _, must := range []string{
		"type: trace",
		"status: open",
		"agent://claude-code/walnut",
		"task_ref: task#25",
		"phase: writing surface",
	} {
		if !strings.Contains(string(content), must) {
			t.Errorf("file missing %q\n---\n%s", must, content)
		}
	}
}

func TestStart_RejectsMissingFields(t *testing.T) {
	cases := []struct {
		name string
		opts trace.StartOpts
		want string
	}{
		{"no-space-root", trace.StartOpts{SpaceID: "hypha://x/y", AgentID: "agent://x/y"}, "space_root"},
		{"no-space-id", trace.StartOpts{SpaceRoot: t.TempDir(), AgentID: "agent://x/y"}, "space"},
		{"no-agent", trace.StartOpts{SpaceRoot: t.TempDir(), SpaceID: "hypha://x/y"}, "agent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := trace.Start(tc.opts)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestTick_AppendsCheckpoint(t *testing.T) {
	space := tempSpace(t)
	tr, err := trace.Start(trace.StartOpts{
		SpaceRoot: space,
		SpaceID:   "hypha://m31labs/test",
		AgentID:   "agent://claude-code/walnut",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	earlierLastTick := tr.LastTick

	time.Sleep(2100 * time.Millisecond) // trace timestamps are RFC3339 second-precision; >2s avoids boundary flake

	if err := trace.Tick(space, tr.ID, "draw loop wired"); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if err := trace.Tick(space, tr.ID, "graft works end-to-end"); err != nil {
		t.Fatalf("Tick #2: %v", err)
	}

	got, err := trace.LoadByID(space, tr.ID)
	if err != nil {
		t.Fatalf("LoadByID: %v", err)
	}
	if len(got.Ticks) != 2 {
		t.Fatalf("len(Ticks) = %d, want 2", len(got.Ticks))
	}
	if got.Ticks[0].Message != "draw loop wired" {
		t.Errorf("Tick[0] = %q, want 'draw loop wired'", got.Ticks[0].Message)
	}
	if got.Ticks[1].Message != "graft works end-to-end" {
		t.Errorf("Tick[1] = %q", got.Ticks[1].Message)
	}
	if !got.LastTick.After(earlierLastTick) {
		t.Errorf("LastTick %v did not advance from %v", got.LastTick, earlierLastTick)
	}
	if got.Status != types.TraceStatusOpen {
		t.Errorf("Status = %q after ticks; should still be open", got.Status)
	}
}

func TestTick_RejectsAfterDone(t *testing.T) {
	space := tempSpace(t)
	tr, _ := trace.Start(trace.StartOpts{
		SpaceRoot: space,
		SpaceID:   "hypha://m31labs/test",
		AgentID:   "agent://claude-code/walnut",
	})
	if _, err := trace.Done(space, tr.ID, types.TraceStatusSucceeded, ""); err != nil {
		t.Fatalf("Done: %v", err)
	}
	if err := trace.Tick(space, tr.ID, "too late"); err == nil {
		t.Fatal("expected Tick to reject a closed trace, got nil error")
	}
}

func TestDone_CompactsTicksIntoWorkLog(t *testing.T) {
	space := tempSpace(t)
	tr, _ := trace.Start(trace.StartOpts{
		SpaceRoot: space,
		SpaceID:   "hypha://m31labs/test",
		AgentID:   "agent://claude-code/walnut",
	})
	for _, msg := range []string{"start math port", "found unexported field", "draw loop wired"} {
		if err := trace.Tick(space, tr.ID, msg); err != nil {
			t.Fatalf("Tick: %v", err)
		}
	}
	closed, err := trace.Done(space, tr.ID, types.TraceStatusSucceeded, "spore.2026-05-25.x.y")
	if err != nil {
		t.Fatalf("Done: %v", err)
	}
	if closed.Status != types.TraceStatusSucceeded {
		t.Errorf("Status = %q, want succeeded", closed.Status)
	}
	if closed.LinkedSpore != "spore.2026-05-25.x.y" {
		t.Errorf("LinkedSpore = %q", closed.LinkedSpore)
	}
	content, _ := os.ReadFile(closed.FilePath)
	body := string(content)
	for _, must := range []string{
		"status: succeeded",
		"linked_spore: spore.2026-05-25.x.y",
		"## Work log",
		"start math port",
		"found unexported field",
		"draw loop wired",
	} {
		if !strings.Contains(body, must) {
			t.Errorf("compacted body missing %q\n---\n%s", must, body)
		}
	}
}

func TestDone_RejectsBadStatus(t *testing.T) {
	space := tempSpace(t)
	tr, _ := trace.Start(trace.StartOpts{
		SpaceRoot: space,
		SpaceID:   "hypha://m31labs/test",
		AgentID:   "agent://claude-code/walnut",
	})
	_, err := trace.Done(space, tr.ID, "bogus", "")
	if err == nil {
		t.Fatal("expected error for invalid status, got nil")
	}
}

func TestList_FiltersByActive(t *testing.T) {
	space := tempSpace(t)
	openOne, _ := trace.Start(trace.StartOpts{
		SpaceRoot: space, SpaceID: "hypha://m31labs/test",
		AgentID: "agent://claude-code/walnut",
	})
	closedOne, _ := trace.Start(trace.StartOpts{
		SpaceRoot: space, SpaceID: "hypha://m31labs/test",
		AgentID: "agent://claude-code/birch",
	})
	if _, err := trace.Done(space, closedOne.ID, types.TraceStatusSucceeded, ""); err != nil {
		t.Fatalf("Done: %v", err)
	}

	all, err := trace.List(space, trace.ListFilter{})
	if err != nil {
		t.Fatalf("List(all): %v", err)
	}
	if len(all) != 2 {
		t.Errorf("len(all) = %d, want 2", len(all))
	}

	active, err := trace.List(space, trace.ListFilter{ActiveOnly: true})
	if err != nil {
		t.Fatalf("List(active): %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("len(active) = %d, want 1", len(active))
	}
	if active[0].ID != openOne.ID {
		t.Errorf("active[0].ID = %q, want %q", active[0].ID, openOne.ID)
	}
}

func TestList_FiltersByAgent(t *testing.T) {
	space := tempSpace(t)
	walnut, _ := trace.Start(trace.StartOpts{
		SpaceRoot: space, SpaceID: "hypha://m31labs/test",
		AgentID: "agent://claude-code/walnut",
	})
	_, _ = trace.Start(trace.StartOpts{
		SpaceRoot: space, SpaceID: "hypha://m31labs/test",
		AgentID: "agent://claude-code/birch",
	})

	got, err := trace.List(space, trace.ListFilter{Agent: "agent://claude-code/walnut"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != walnut.ID {
		t.Errorf("filter mismatch: got %d traces, want 1 (walnut)", len(got))
	}
}

func TestStoragePathPattern(t *testing.T) {
	space := tempSpace(t)
	tr, _ := trace.Start(trace.StartOpts{
		SpaceRoot: space, SpaceID: "hypha://m31labs/test",
		AgentID: "agent://claude-code/walnut",
	})
	rel, _ := filepath.Rel(space, tr.FilePath)
	// Expected pattern: .trace/<YYYY-MM-DD>/<trace-id>.md
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) != 3 || parts[0] != ".trace" {
		t.Fatalf("path %q does not match .trace/<date>/<id>.md", rel)
	}
	if _, err := time.Parse("2006-01-02", parts[1]); err != nil {
		t.Errorf("date segment %q not a date", parts[1])
	}
}
