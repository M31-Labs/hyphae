package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunReportsSpaceAndMissingIndex(t *testing.T) {
	root := t.TempDir()
	spaceRoot := filepath.Join(root, "spaces", "acme-eng")
	writeFile(t, filepath.Join(spaceRoot, "SPACE.md"), `---
id: hypha://acme/eng
type: space
status: active
---
# Acme Engineering

Shared engineering memory.
`)
	writeFile(t, filepath.Join(spaceRoot, "concepts", "caching.md"), `---
id: hypha://acme/eng/concepts/caching
type: concept
status: active
tags:
  - caching
---
# Caching

Cache invalidation policy and operational notes.
`)

	report, err := Run(context.Background(), Options{Root: root})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Status != StatusWarning {
		t.Fatalf("Status = %q, want warning", report.Status)
	}
	if len(report.Spaces) != 1 {
		t.Fatalf("len(Spaces) = %d, want 1", len(report.Spaces))
	}
	space := report.Spaces[0]
	if space.URI != "hypha://acme/eng" {
		t.Fatalf("space URI = %q", space.URI)
	}
	if space.MarkdownFiles != 2 || space.Objects != 2 {
		t.Fatalf("space counts = markdown %d objects %d, want 2/2", space.MarkdownFiles, space.Objects)
	}
	if len(space.ParseErrors) != 0 {
		t.Fatalf("unexpected parse errors: %+v", space.ParseErrors)
	}
	if report.Index.Exists {
		t.Fatalf("index should be reported missing")
	}
	if !containsRecommendation(report.Recommendations, "hypha index rebuild") {
		t.Fatalf("recommendations do not mention rebuild: %#v", report.Recommendations)
	}
}

func TestRunReportsSpaceParseErrors(t *testing.T) {
	root := t.TempDir()
	spaceRoot := filepath.Join(root, "spaces", "acme-eng")
	writeFile(t, filepath.Join(spaceRoot, "SPACE.md"), `---
id: hypha://acme/eng
type: space
status: active
---
# Acme Engineering
`)
	writeFile(t, filepath.Join(spaceRoot, "broken.md"), `# Broken

This file has no frontmatter id.
`)

	report, err := Run(context.Background(), Options{Root: root})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Spaces) != 1 {
		t.Fatalf("len(Spaces) = %d, want 1", len(report.Spaces))
	}
	space := report.Spaces[0]
	if space.Status != StatusWarning {
		t.Fatalf("space status = %q, want warning", space.Status)
	}
	if len(space.ParseErrors) == 0 {
		t.Fatalf("expected parse errors")
	}
	foundBroken := false
	for _, err := range space.ParseErrors {
		if strings.Contains(err.Path, "broken.md") {
			foundBroken = true
			break
		}
	}
	if !foundBroken {
		t.Fatalf("parse errors did not include broken.md: %+v", space.ParseErrors)
	}
}

func TestToolVersionAtLeast(t *testing.T) {
	cases := []struct {
		version string
		want    bool
	}{
		{"canopy version 0.16.2", true},
		{"canopy version v0.16.0", true},
		{"canopy version 0.15.9", false},
		{"unexpected", true},
	}
	for _, c := range cases {
		t.Run(c.version, func(t *testing.T) {
			if got := toolVersionAtLeast(c.version, 0, 16, 0); got != c.want {
				t.Fatalf("toolVersionAtLeast(%q) = %v, want %v", c.version, got, c.want)
			}
		})
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func containsRecommendation(recs []string, needle string) bool {
	for _, rec := range recs {
		if strings.Contains(rec, needle) {
			return true
		}
	}
	return false
}
