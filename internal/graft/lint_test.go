package graft

import (
	"testing"

	"m31labs.dev/hyphae/internal/types"
)

func TestLintWrites_ReportsPerWriteOutcome(t *testing.T) {
	installRoot := t.TempDir()
	makeCanonicalFile(t,
		installRoot+"/spaces",
		"test-space/concepts/target.md",
		`---
mdpp: "0.1"
id: concept.target
type: concept
space: hypha://test/space
status: canonical
---

# Title

## Real Section

Body.
`,
	)

	writes := []types.ProposedWrite{
		{Kind: "append_section", Target: "hypha://test/space/concepts/target", Payload: map[string]any{
			"heading": "Real Section",
			"body":    "Appended.\n",
		}},
		{Kind: "append_section", Target: "hypha://test/space/concepts/missing", Payload: map[string]any{
			"heading": "Anywhere",
			"body":    "Never lands.\n",
		}},
		{Kind: "replace_block", Target: "hypha://test/space/concepts/target", Payload: map[string]any{}},
	}

	findings := LintWrites(installRoot, "hypha://test/space", writes)
	if len(findings) != 3 {
		t.Fatalf("findings = %d, want 3", len(findings))
	}
	if !findings[0].Applies || findings[0].Reason != "" {
		t.Fatalf("good write = %+v, want applies", findings[0])
	}
	if findings[1].Applies || findings[1].Reason == "" {
		t.Fatalf("missing-target write = %+v, want skip with reason", findings[1])
	}
	if findings[2].Applies || findings[2].Reason == "" {
		t.Fatalf("empty replace_block = %+v, want skip with reason", findings[2])
	}

	// Lint never touches disk: the target file must be unchanged.
	second := LintWrites(installRoot, "hypha://test/space", writes[:1])
	if !second[0].Applies {
		t.Fatalf("relint = %+v, want same outcome (no persisted writes)", second[0])
	}
}
