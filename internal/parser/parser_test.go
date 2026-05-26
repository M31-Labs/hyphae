package parser

import (
	"os"
	"testing"

	"m31labs.dev/hyphae/internal/types"
)

const (
	testSpaceRoot = "/home/draco/.hyphae/spaces/m31labs-hyphae"
	testSpaceID   = "m31labs/hyphae"
)

// TestWalkSpace verifies the happy-path walk of the m31labs-hyphae spec space.
func TestWalkSpace(t *testing.T) {
	if _, err := os.Stat(testSpaceRoot); os.IsNotExist(err) {
		t.Skipf("test space not found at %s", testSpaceRoot)
	}

	objects, anchors, edges, err := WalkSpace(testSpaceRoot, testSpaceID, false)
	if err != nil {
		t.Fatalf("WalkSpace returned error: %v", err)
	}

	// Must return at least 20 objects.
	if len(objects) < 20 {
		t.Errorf("expected >= 20 objects, got %d", len(objects))
	}

	// At least one concept and one decision.
	var hasConcept, hasDecision bool
	for _, o := range objects {
		switch o.Type {
		case types.TypeConcept:
			hasConcept = true
		case types.TypeDecision:
			hasDecision = true
		}
	}
	if !hasConcept {
		t.Error("expected at least one object with Type == concept")
	}
	if !hasDecision {
		t.Error("expected at least one object with Type == decision")
	}

	// At least one edge extracted (spec files have rich related: blocks).
	if len(edges) == 0 {
		t.Error("expected at least one edge to be extracted")
	}

	// Anchors must be non-empty.
	if len(anchors) == 0 {
		t.Error("expected at least one anchor")
	}

	t.Logf("walk summary: %d objects, %d anchors, %d edges", len(objects), len(anchors), len(edges))
}

// TestParseFile verifies single-file parsing against the canonical hyphae concept.
func TestParseFile(t *testing.T) {
	path := testSpaceRoot + "/concepts/hyphae.md"
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skipf("test file not found: %s", path)
	}

	obj, anchors, edges, err := ParseFile(path, testSpaceID)
	if err != nil {
		t.Fatalf("ParseFile returned error: %v", err)
	}

	if obj.ID != "concept.hyphae" {
		t.Errorf("expected ID=concept.hyphae, got %q", obj.ID)
	}
	if obj.Type != types.TypeConcept {
		t.Errorf("expected Type=concept, got %q", obj.Type)
	}
	if obj.SpaceID != testSpaceID {
		t.Errorf("expected SpaceID=%q, got %q", testSpaceID, obj.SpaceID)
	}
	if obj.Title != "Hyphae" {
		t.Errorf("expected Title=Hyphae, got %q", obj.Title)
	}
	if obj.Summary == "" {
		t.Error("expected non-empty Summary")
	}
	if len(obj.Tags) == 0 {
		t.Error("expected at least one tag")
	}
	if obj.Body == "" {
		t.Error("expected non-empty Body")
	}
	if obj.UpdatedAt.IsZero() {
		t.Error("expected non-zero UpdatedAt")
	}

	if len(anchors) == 0 {
		t.Error("expected at least one anchor")
	}

	// hyphae.md has a rich related: block; must produce at least one edge.
	if len(edges) == 0 {
		t.Error("expected at least one edge")
	}

	// Verify anchor IDs look like hypha:// URIs with slugs.
	for _, a := range anchors {
		if a.NodeKind != "heading" {
			t.Errorf("unexpected NodeKind %q for anchor %s", a.NodeKind, a.ID)
		}
		if len(a.ID) == 0 || a.ID[:8] != "hypha://" {
			t.Errorf("anchor ID does not start with hypha://: %s", a.ID)
		}
	}

	t.Logf("hyphae.md: title=%q summary=%q len(anchors)=%d len(edges)=%d",
		obj.Title, obj.Summary, len(anchors), len(edges))
}

// TestSlugify tests the slug helper.
func TestSlugify(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Hello World", "hello-world"},
		{"Architecture & Design", "architecture-design"},
		{"  --- leading dashes ---  ", "leading-dashes"},
		{"v0.1 Release", "v0-1-release"},
		{"Already-slug", "already-slug"},
	}
	for _, c := range cases {
		if got := slugify(c.in); got != c.want {
			t.Errorf("slugify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestExtractStringList tests the coercion helper.
func TestExtractStringList(t *testing.T) {
	got := extractStringList([]any{"a", "b", "c"})
	if len(got) != 3 || got[0] != "a" {
		t.Errorf("unexpected result: %v", got)
	}
	if extractStringList(nil) != nil {
		t.Error("nil input should return nil")
	}
	got2 := extractStringList("single")
	if len(got2) != 1 || got2[0] != "single" {
		t.Errorf("string input: unexpected result: %v", got2)
	}
}

// TestStripWikilink covers the wikilink stripping helper.
func TestStripWikilink(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"[[spore]]", "spore"},
		{"[[../concepts/foo|Foo Concept]]", "../concepts/foo"},
		{"concept.hyphae", "concept.hyphae"},
		{"  [[graft]]  ", "graft"},
	}
	for _, c := range cases {
		if got := stripWikilink(c.in); got != c.want {
			t.Errorf("stripWikilink(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestResolveWikilink covers the resolution helper.
func TestResolveWikilink(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"spore", "concept.spore"},
		{"../concepts/spore", "concept.spore"},
		{"../decisions/0001-foo", "decision.0001-foo"},
		{"concept.hyphae", "concept.hyphae"},                         // already qualified
		{"hypha://m31labs/hyphae/concepts/foo", "hypha://m31labs/hyphae/concepts/foo"}, // URI
	}
	for _, c := range cases {
		if got := resolveWikilink(c.in); got != c.want {
			t.Errorf("resolveWikilink(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
