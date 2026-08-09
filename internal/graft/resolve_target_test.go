package graft

import (
	"path/filepath"
	"testing"
)

// TestResolveTarget_MdExtensionTolerance verifies that resolveTarget accepts
// target URIs whose file path already carries a ".md" extension (the natural
// way to write them, matching source_refs URI conventions) and resolves them
// to the same on-disk file as the extension-less form.
func TestResolveTarget_MdExtensionTolerance(t *testing.T) {
	installRoot := t.TempDir()
	wantFile := filepath.Join(installRoot, "spaces", "m31labs-buckley", "concepts", "cli.md")

	cases := []struct {
		name          string
		targetURI     string
		wantAnchor    string
		wantCanonical string
	}{
		{
			name:          "bare path",
			targetURI:     "hypha://m31labs/buckley/concepts/cli",
			wantAnchor:    "",
			wantCanonical: "hypha://m31labs/buckley/concepts/cli",
		},
		{
			name:          "path with .md extension",
			targetURI:     "hypha://m31labs/buckley/concepts/cli.md",
			wantAnchor:    "",
			wantCanonical: "hypha://m31labs/buckley/concepts/cli",
		},
		{
			name:          "path with anchor",
			targetURI:     "hypha://m31labs/buckley/concepts/cli#usage",
			wantAnchor:    "usage",
			wantCanonical: "hypha://m31labs/buckley/concepts/cli#usage",
		},
		{
			name:          "path with .md extension and anchor",
			targetURI:     "hypha://m31labs/buckley/concepts/cli.md#usage",
			wantAnchor:    "usage",
			wantCanonical: "hypha://m31labs/buckley/concepts/cli#usage",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotFile, gotAnchor, gotCanonical, err := resolveTarget(installRoot, tc.targetURI)
			if err != nil {
				t.Fatalf("resolveTarget(%q): unexpected error: %v", tc.targetURI, err)
			}
			if gotFile != wantFile {
				t.Errorf("resolveTarget(%q) file = %q, want %q", tc.targetURI, gotFile, wantFile)
			}
			if gotAnchor != tc.wantAnchor {
				t.Errorf("resolveTarget(%q) anchor = %q, want %q", tc.targetURI, gotAnchor, tc.wantAnchor)
			}
			if gotCanonical != tc.wantCanonical {
				t.Errorf("resolveTarget(%q) canonical = %q, want %q", tc.targetURI, gotCanonical, tc.wantCanonical)
			}
		})
	}
}
