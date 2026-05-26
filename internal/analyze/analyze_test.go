package analyze_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/hyphae/internal/analyze"
	"m31labs.dev/hyphae/internal/types"
)

// fakeSpace mirrors a real hyphae space layout enough for analyze to write
// into it: SPACE.md, .analyses/ ready to be created.
func fakeSpace(t *testing.T, sourcePath string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// fakeImpactJSON is a stand-in for canopy graph impact --json output, used by
// tests so they don't depend on canopy being installed.
func fakeImpactJSON() string {
	return `{
		"changed": ["Submit"],
		"affected": [
			{"name":"cmdSporeSubmit","file":"cmd/hypha/main.go","kind":"function_definition","start_line":586,"end_line":673,"distance":1,"risk":0.5},
			{"name":"TestHappyPath","file":"internal/spore/spore_test.go","kind":"function_definition","start_line":67,"end_line":178,"distance":1,"risk":0.5},
			{"name":"TestDuplicateSubmit","file":"internal/spore/spore_test.go","kind":"function_definition","start_line":182,"end_line":200,"distance":1,"risk":0.5}
		],
		"affected_files": ["cmd/hypha/main.go", "internal/spore/spore_test.go"],
		"total_affected": 3
	}`
}

func TestParseImpact_ExtractsCountsAndTopFiles(t *testing.T) {
	a, err := analyze.ParseImpact([]byte(fakeImpactJSON()))
	if err != nil {
		t.Fatalf("ParseImpact: %v", err)
	}
	if a.TotalAffected != 3 {
		t.Errorf("TotalAffected = %d, want 3", a.TotalAffected)
	}
	if len(a.TopFiles) == 0 {
		t.Fatal("TopFiles empty")
	}
	// "internal/spore/spore_test.go" should rank above "cmd/hypha/main.go"
	// because it has 2 affected symbols vs 1.
	if a.TopFiles[0] != "internal/spore/spore_test.go" {
		t.Errorf("TopFiles[0] = %q, want internal/spore/spore_test.go", a.TopFiles[0])
	}
	if len(a.TopSymbols) == 0 {
		t.Error("TopSymbols empty")
	}
}

func TestSerialize_RoundTripsThroughFile(t *testing.T) {
	space := fakeSpace(t, "")
	a := types.Analysis{
		ID:            "analysis.impact.cmd-hypha-main.a6f1fc0",
		Kind:          types.AnalysisKindImpact,
		SpaceID:       "hypha://m31labs/test",
		Target:        "cmd/hypha/main.go",
		TargetFiles:   []string{"cmd/hypha/main.go", "internal/types/types.go"},
		Commit:        "a6f1fc0",
		Tool:          "canopy",
		ToolVersion:   "0.x.y",
		TotalAffected: 3,
		TopFiles:      []string{"internal/spore/spore_test.go", "cmd/hypha/main.go"},
		TopSymbols:    []string{"cmdSporeSubmit", "TestHappyPath"},
		RawJSON:       fakeImpactJSON(),
	}
	if err := analyze.Write(space, &a); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if a.FilePath == "" {
		t.Fatal("Write did not set FilePath")
	}
	if _, err := os.Stat(a.FilePath); err != nil {
		t.Fatalf("file missing: %v", err)
	}

	loaded, err := analyze.Load(a.FilePath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.ID != a.ID {
		t.Errorf("ID = %q, want %q", loaded.ID, a.ID)
	}
	if loaded.Kind != a.Kind {
		t.Errorf("Kind = %q, want %q", loaded.Kind, a.Kind)
	}
	if loaded.Commit != a.Commit {
		t.Errorf("Commit = %q, want %q", loaded.Commit, a.Commit)
	}
	if loaded.TotalAffected != a.TotalAffected {
		t.Errorf("TotalAffected = %d, want %d", loaded.TotalAffected, a.TotalAffected)
	}
}

func TestFilePathPattern(t *testing.T) {
	space := fakeSpace(t, "")
	a := types.Analysis{
		ID:          "analysis.impact.cmd-hypha-main.a6f1fc0",
		Kind:        types.AnalysisKindImpact,
		Target:      "cmd/hypha/main.go",
		Commit:      "a6f1fc0",
		TargetFiles: []string{"cmd/hypha/main.go"},
	}
	if err := analyze.Write(space, &a); err != nil {
		t.Fatalf("Write: %v", err)
	}
	rel, _ := filepath.Rel(space, a.FilePath)
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) != 3 || parts[0] != ".analyses" {
		t.Fatalf("path %q does not match .analyses/<kind>/<file>.md", rel)
	}
	if parts[1] != "impact" {
		t.Errorf("kind segment = %q, want impact", parts[1])
	}
	if !strings.HasSuffix(parts[2], ".md") {
		t.Errorf("file does not end in .md: %q", parts[2])
	}
}

func TestStalenessByCommit(t *testing.T) {
	space := fakeSpace(t, "")
	a := types.Analysis{
		ID:          "analysis.impact.x.aaaa111",
		Kind:        types.AnalysisKindImpact,
		Target:      "x.go",
		Commit:      "aaaa111",
		TargetFiles: []string{"x.go"},
	}
	if err := analyze.Write(space, &a); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Different head → stale.
	freshness := analyze.CheckFreshness(&a, analyze.FreshnessInputs{
		CurrentCommit: "bbbb222",
	})
	if !freshness.Stale {
		t.Errorf("expected stale=true when commit differs")
	}
	if freshness.Reason != analyze.StaleReasonCommit {
		t.Errorf("reason = %q, want %q", freshness.Reason, analyze.StaleReasonCommit)
	}

	// Same head, no mtimes provided → fresh.
	freshness = analyze.CheckFreshness(&a, analyze.FreshnessInputs{
		CurrentCommit: "aaaa111",
	})
	if freshness.Stale {
		t.Errorf("expected fresh when commit matches and no mtimes given")
	}
}

func TestListByKind(t *testing.T) {
	space := fakeSpace(t, "")
	mustWrite := func(kind, target, commit string) {
		a := types.Analysis{
			ID:          "analysis." + kind + "." + target + "." + commit,
			Kind:        kind,
			Target:      target,
			Commit:      commit,
			TargetFiles: []string{target},
		}
		if err := analyze.Write(space, &a); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	mustWrite(types.AnalysisKindImpact, "x.go", "aaa1111")
	mustWrite(types.AnalysisKindImpact, "y.go", "bbb2222")
	mustWrite(types.AnalysisKindCallgraph, "MyFunc", "ccc3333")

	impacts, err := analyze.List(space, analyze.ListFilter{Kind: types.AnalysisKindImpact})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(impacts) != 2 {
		t.Errorf("len(impacts) = %d, want 2", len(impacts))
	}
	all, err := analyze.List(space, analyze.ListFilter{})
	if err != nil {
		t.Fatalf("List(all): %v", err)
	}
	if len(all) != 3 {
		t.Errorf("len(all) = %d, want 3", len(all))
	}
}

func TestListByTargetFile(t *testing.T) {
	space := fakeSpace(t, "")
	a1 := types.Analysis{
		ID:          "analysis.impact.foo.aaa1111",
		Kind:        types.AnalysisKindImpact,
		Target:      "foo.go",
		Commit:      "aaa1111",
		TargetFiles: []string{"cmd/hypha/main.go", "internal/spore/spore.go"},
	}
	a2 := types.Analysis{
		ID:          "analysis.impact.bar.bbb2222",
		Kind:        types.AnalysisKindImpact,
		Target:      "bar.go",
		Commit:      "bbb2222",
		TargetFiles: []string{"internal/types/types.go"},
	}
	if err := analyze.Write(space, &a1); err != nil {
		t.Fatal(err)
	}
	if err := analyze.Write(space, &a2); err != nil {
		t.Fatal(err)
	}
	got, err := analyze.List(space, analyze.ListFilter{TargetFile: "cmd/hypha/main.go"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != a1.ID {
		t.Errorf("expected 1 match (a1), got %d", len(got))
	}
}

// verify that the RawJSON survives via the body block (HTML comment).
func TestRawJSONRoundtrips(t *testing.T) {
	space := fakeSpace(t, "")
	a := types.Analysis{
		ID:          "analysis.impact.x.a6f1fc0",
		Kind:        types.AnalysisKindImpact,
		Target:      "x.go",
		Commit:      "a6f1fc0",
		TargetFiles: []string{"x.go"},
		RawJSON:     fakeImpactJSON(),
	}
	if err := analyze.Write(space, &a); err != nil {
		t.Fatal(err)
	}
	loaded, err := analyze.Load(a.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal([]byte(loaded.RawJSON), &got); err != nil {
		t.Fatalf("loaded RawJSON not valid JSON: %v\n%s", err, loaded.RawJSON)
	}
	if err := json.Unmarshal([]byte(fakeImpactJSON()), &want); err != nil {
		t.Fatal(err)
	}
	// Compare via re-marshal to handle key ordering.
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(want)
	if string(gb) != string(wb) {
		t.Errorf("RawJSON differs:\ngot  %s\nwant %s", gb, wb)
	}
}
