// Package parser walks mdpp spaces and extracts typed Objects, Anchors, and
// Edges from frontmatter and body content.
package parser

import (
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/odvcencio/hyphae/internal/types"
	"github.com/odvcencio/mdpp"
)

// wikitextRe matches [[name]] and [[path|alias]] wikilink patterns.
var wikitextRe = regexp.MustCompile(`\[\[([^\]|]+)(?:\|[^\]]*)?\]\]`)

// ParseFile parses a single mdpp file at path. spaceID is the URI
// authority/name (e.g. "m31labs/hyphae"). Returns the typed object plus
// anchors and edges extracted from the file.
func ParseFile(path, spaceID string) (types.Object, []types.Anchor, []types.Edge, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return types.Object{}, nil, nil, fmt.Errorf("read %s: %w", path, err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		return types.Object{}, nil, nil, fmt.Errorf("stat %s: %w", path, err)
	}

	doc, err := mdpp.Parse(data)
	if err != nil {
		return types.Object{}, nil, nil, fmt.Errorf("parse %s: %w", path, err)
	}

	fm := doc.Frontmatter()
	if fm == nil {
		fm = map[string]any{}
	}

	// --- Object fields from frontmatter ---

	id, _ := fm["id"].(string)
	if id == "" {
		return types.Object{}, nil, nil, fmt.Errorf("missing frontmatter id in %s", path)
	}

	objType, _ := fm["type"].(string)
	status, _ := fm["status"].(string)

	tags := extractStringList(fm["tags"])

	// --- Title and Summary from body ---

	root := doc.AST()
	title := extractTitle(root)
	summary := extractSummary(root)
	body := extractBody(data, root)

	obj := types.Object{
		ID:        id,
		Type:      types.ObjectType(objType),
		SpaceID:   spaceID,
		FilePath:  path,
		Status:    status,
		Title:     title,
		Summary:   summary,
		Tags:      tags,
		Body:      body,
		Frontmtr:  fm,
		UpdatedAt: fi.ModTime(),
	}

	// --- Anchors from headings ---

	anchors := extractAnchors(id, spaceID, path, root)

	// --- Edges ---

	edges := extractEdges(id, fm, root)

	return obj, anchors, edges, nil
}

// WalkSpace walks every *.md file under spaceRoot (excluding inbox/ unless
// includeInbox is true). spaceID is the URI authority/name.
func WalkSpace(spaceRoot, spaceID string, includeInbox bool) (objects []types.Object, anchors []types.Anchor, edges []types.Edge, err error) {
	err = filepath.WalkDir(spaceRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		// Skip the inbox directory unless explicitly requested.
		if d.IsDir() && d.Name() == "inbox" && !includeInbox {
			return filepath.SkipDir
		}

		if d.IsDir() {
			return nil
		}

		if filepath.Ext(path) != ".md" {
			return nil
		}

		obj, anch, edgs, parseErr := ParseFile(path, spaceID)
		if parseErr != nil {
			log.Printf("warning: skipping %s: %v", path, parseErr)
			return nil
		}

		objects = append(objects, obj)
		anchors = append(anchors, anch...)
		edges = append(edges, edgs...)
		return nil
	})
	return
}

// --- helpers ---

// extractTitle returns the text of the first H1 node in the AST.
func extractTitle(root *mdpp.Node) string {
	if root == nil {
		return ""
	}
	for _, n := range root.Find(mdpp.NodeHeading) {
		if n.Level() == 1 {
			return strings.TrimSpace(n.Text())
		}
	}
	return ""
}

// extractSummary returns the text of the first paragraph that follows the
// first H1, truncated to ~200 chars.
func extractSummary(root *mdpp.Node) string {
	if root == nil {
		return ""
	}
	foundH1 := false
	var summary string
	root.Walk(func(n *mdpp.Node) bool {
		if summary != "" {
			return false
		}
		if n.Type == mdpp.NodeHeading && n.Level() == 1 {
			foundH1 = true
			return true
		}
		if foundH1 && n.Type == mdpp.NodeParagraph {
			text := strings.TrimSpace(n.Text())
			if len(text) > 200 {
				// Truncate at last space before 200 to avoid cutting mid-word.
				cut := text[:200]
				if idx := strings.LastIndex(cut, " "); idx > 0 {
					cut = cut[:idx]
				}
				text = cut + "…"
			}
			summary = text
			return false
		}
		return true
	})
	return summary
}

// extractBody returns the full body text: everything after the YAML
// frontmatter block. We detect the end of frontmatter by finding the second
// `---` line (or `...` terminator) and returning the remainder.
func extractBody(data []byte, root *mdpp.Node) string {
	// Locate the frontmatter node's end byte so we can slice.
	if root != nil {
		for _, n := range root.Children {
			if n.Type == mdpp.NodeFrontmatter {
				if n.Range.EndByte > 0 && n.Range.EndByte <= len(data) {
					// Skip the trailing newline after the closing `---`.
					rest := data[n.Range.EndByte:]
					return strings.TrimLeft(string(rest), "\r\n")
				}
			}
		}
	}

	// Fallback: scan for the closing `---` line manually.
	s := string(data)
	lines := strings.Split(s, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return s
	}
	for i := 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "---" || trimmed == "..." {
			return strings.Join(lines[i+1:], "\n")
		}
	}
	return s
}

// extractAnchors builds one Anchor per heading in the document.
func extractAnchors(objectID, spaceID, filePath string, root *mdpp.Node) []types.Anchor {
	if root == nil {
		return nil
	}

	absPath, _ := filepath.Abs(filePath)
	spaceRoot := inferSpaceRoot(absPath)

	// Relative path for the anchor URI.
	rel := absPath
	if spaceRoot != "" {
		if r, err := filepath.Rel(spaceRoot, absPath); err == nil {
			rel = r
		}
	}
	// Strip extension.
	relNoExt := strings.TrimSuffix(rel, filepath.Ext(rel))

	// Track heading path stack (level → text).
	type headingEntry struct {
		level int
		text  string
	}
	var stack []headingEntry

	var anchors []types.Anchor

	root.Walk(func(n *mdpp.Node) bool {
		if n.Type != mdpp.NodeHeading {
			return true
		}
		level := n.Level()
		text := strings.TrimSpace(n.Text())
		slug := slugify(text)

		// Pop stack to the current level.
		for len(stack) > 0 && stack[len(stack)-1].level >= level {
			stack = stack[:len(stack)-1]
		}
		stack = append(stack, headingEntry{level, text})

		// Build heading path.
		var parts []string
		for _, e := range stack {
			parts = append(parts, e.text)
		}
		headingPath := "/" + strings.Join(parts, "/")

		anchorID := fmt.Sprintf("hypha://%s/%s#%s", spaceID, relNoExt, slug)

		anchors = append(anchors, types.Anchor{
			ID:          anchorID,
			ObjectID:    objectID,
			HeadingPath: headingPath,
			StartByte:   n.Range.StartByte,
			EndByte:     n.Range.EndByte,
			StartLine:   n.Range.StartLine,
			EndLine:     n.Range.EndLine,
			NodeKind:    "heading",
		})
		return true
	})

	return anchors
}

// inferSpaceRoot attempts to find the space root by looking for a SPACE.md or
// .hyphae marker. This is a best-effort heuristic for anchor URI construction.
func inferSpaceRoot(absPath string) string {
	dir := filepath.Dir(absPath)
	for dir != "/" && dir != "." {
		if _, err := os.Stat(filepath.Join(dir, "SPACE.md")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// extractEdges builds all graph edges from both frontmatter declarations and
// inline body links/wikilinks.
func extractEdges(objectID string, fm map[string]any, root *mdpp.Node) []types.Edge {
	var edges []types.Edge
	now := time.Now()

	edgeID := func(kind types.EdgeKind, src, dst string) string {
		return fmt.Sprintf("edge:%s:%s->%s", kind, src, dst)
	}

	makeFrontmatterEdge := func(kind types.EdgeKind, src, dst string) types.Edge {
		dst = resolveWikilink(dst)
		return types.Edge{
			ID:          edgeID(kind, src, dst),
			Kind:        kind,
			SrcID:       src,
			DstID:       dst,
			Confidence:  1.0,
			Derivation:  "frontmatter",
			CreatedAt:   now,
		}
	}

	// related: list → EdgeRelated
	for _, ref := range extractStringList(fm["related"]) {
		dst := stripWikilink(ref)
		edges = append(edges, makeFrontmatterEdge(types.EdgeRelated, objectID, dst))
	}

	// source_refs: list → EdgeSourceRef
	for _, ref := range extractStringList(fm["source_refs"]) {
		dst := stripWikilink(ref)
		edges = append(edges, makeFrontmatterEdge(types.EdgeSourceRef, objectID, dst))
	}

	// applies_to: list → EdgeAppliesTo
	for _, ref := range extractStringList(fm["applies_to"]) {
		dst := stripWikilink(ref)
		edges = append(edges, makeFrontmatterEdge(types.EdgeAppliesTo, objectID, dst))
	}

	// superseded_by: scalar → EdgeSupersededBy
	if v, ok := fm["superseded_by"]; ok {
		var dst string
		switch sv := v.(type) {
		case string:
			dst = sv
		}
		if dst != "" {
			dst = stripWikilink(dst)
			edges = append(edges, makeFrontmatterEdge(types.EdgeSupersededBy, objectID, dst))
		}
	}

	// Body edges: hypha:// links (EdgeLinkRef) and [[name]] wikilinks
	// (EdgeWikilink).
	if root != nil {
		seenLink := map[string]bool{}
		seenWiki := map[string]bool{}

		root.Walk(func(n *mdpp.Node) bool {
			switch n.Type {
			case mdpp.NodeLink:
				href := n.Attr("href")
				if strings.HasPrefix(href, "hypha://") && !seenLink[href] {
					seenLink[href] = true
					edges = append(edges, types.Edge{
						ID:          edgeID(types.EdgeLinkRef, objectID, href),
						Kind:        types.EdgeLinkRef,
						SrcID:       objectID,
						DstID:       href,
						Confidence:  1.0,
						Derivation:  "linkref",
						CreatedAt:   now,
					})
				}

			case mdpp.NodeText:
				// Scan text nodes for [[name]] patterns not already captured by
				// the AST (mdpp may or may not parse wikilinks as distinct nodes).
				matches := wikitextRe.FindAllStringSubmatch(n.Literal, -1)
				for _, m := range matches {
					name := strings.TrimSpace(m[1])
					dst := resolveWikilink(name)
					if !seenWiki[dst] {
						seenWiki[dst] = true
						edges = append(edges, types.Edge{
							ID:          edgeID(types.EdgeWikilink, objectID, dst),
							Kind:        types.EdgeWikilink,
							SrcID:       objectID,
							DstID:       dst,
							Confidence:  1.0,
							Derivation:  "wikilink",
							CreatedAt:   now,
						})
					}
				}
			}
			return true
		})
	}

	return edges
}

// stripWikilink strips [[...]] syntax and returns the inner name.
// If the value is not a wikilink it is returned as-is.
func stripWikilink(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "[[") && strings.HasSuffix(s, "]]") {
		inner := s[2 : len(s)-2]
		// Handle [[path|alias]] — use the path part.
		if idx := strings.Index(inner, "|"); idx >= 0 {
			inner = inner[:idx]
		}
		return strings.TrimSpace(inner)
	}
	return s
}

// resolveWikilink converts a wikilink name/path to a canonical destination ID.
// For v0.1 the only transformation applied is:
//   - plain names (no slashes) → "concept.<name>"
//   - qualified IDs with a dot-separator (e.g. "concept.hyphae") → returned as-is
//   - relative paths like "../concepts/foo" or "concepts/foo" → type-prefix + basename
//   - absolute URIs (contain "://") → returned as-is
func resolveWikilink(name string) string {
	// Already an absolute URI.
	if strings.Contains(name, "://") {
		return name
	}

	// Strip relative path prefix and extension to get the base name.
	base := filepath.Base(name)
	base = strings.TrimSuffix(base, filepath.Ext(base))

	// Infer type from path segment when the name contains a slash.
	if strings.ContainsRune(name, '/') {
		segments := strings.Split(name, "/")
		for _, seg := range segments {
			switch seg {
			case "concepts":
				return "concept." + base
			case "decisions":
				return "decision." + base
			case "initiatives":
				return "initiative." + base
			case "skills":
				return "skill." + base
			case "protocols":
				return "protocol." + base
			}
		}
		// Unknown path type; use the base name with concept prefix.
		return "concept." + base
	}

	// Already a qualified ID (contains a dot but no slash).
	if strings.ContainsRune(name, '.') {
		return name
	}

	// Default: assume concept.
	return "concept." + name
}

// slugify converts a heading text to a URL-safe slug.
// Lowercase, collapse non-alphanumerics to "-", trim edges.
func slugify(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	inDash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			inDash = false
		} else {
			if !inDash {
				b.WriteByte('-')
				inDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// extractStringList coerces an any value to []string. Handles:
//   - nil → nil
//   - []any containing strings or nested []any (YAML parses [[name]] as nested lists)
//   - []string
//   - a bare string → single-element slice
func extractStringList(v any) []string {
	if v == nil {
		return nil
	}
	switch tv := v.(type) {
	case []string:
		return tv
	case []any:
		var out []string
		for _, item := range tv {
			if s := flattenYAMLScalar(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if tv == "" {
			return nil
		}
		return []string{tv}
	}
	return nil
}

// flattenYAMLScalar recursively collapses nested []any structures into a
// single string. YAML parses [[name]] (wikilink syntax) as a doubly-nested
// flow sequence; this function unwraps it back to the inner string.
func flattenYAMLScalar(v any) string {
	switch tv := v.(type) {
	case string:
		return tv
	case []any:
		// Collect all leaf strings and concatenate.
		var sb strings.Builder
		for _, item := range tv {
			sb.WriteString(flattenYAMLScalar(item))
		}
		return sb.String()
	}
	return fmt.Sprintf("%v", v)
}
