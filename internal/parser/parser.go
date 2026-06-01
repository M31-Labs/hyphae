// Package parser walks mdpp spaces and extracts typed Objects, Anchors, and
// Edges from frontmatter and body content.
package parser

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
	"m31labs.dev/hyphae/internal/types"
)

// wikitextRe matches [[name]] and [[path|alias]] wikilink patterns.
var wikitextRe = regexp.MustCompile(`\[\[([^\]|]+)(?:\|[^\]]*)?\]\]`)

// markdownHyphaLinkRe matches Markdown links whose destination is hypha://...
var markdownHyphaLinkRe = regexp.MustCompile(`\]\(\s*<?(hypha://[^)\s>]+)>?`)

var ErrLimitExceeded = errors.New("parser limit exceeded")

const (
	DefaultMaxFileBytes      int64 = 2 * 1024 * 1024
	DefaultMaxBodyBytes            = 256 * 1024
	DefaultMaxMarkdownFiles        = 20_000
	DefaultMaxDepth                = 32
	DefaultMaxAnchorsPerFile       = 512
	DefaultMaxEdgesPerFile         = 2048
	DefaultMaxSkipRecords          = 256

	HardMaxFileBytes      int64 = 32 * 1024 * 1024
	HardMaxBodyBytes            = 4 * 1024 * 1024
	HardMaxMarkdownFiles        = 1_000_000
	HardMaxDepth                = 128
	HardMaxAnchorsPerFile       = 8192
	HardMaxEdgesPerFile         = 32768
	HardMaxSkipRecords          = 4096

	maxFrontmatterListItems    = 512
	maxFrontmatterScalarBytes  = 2048
	maxFrontmatterFlattenDepth = 8
)

// Limits bound parser memory and traversal work. Zero means use the default;
// callers cannot disable limits through this struct.
type Limits struct {
	MaxFileBytes     int64
	MaxBodyBytes     int
	MaxMarkdownFiles int
	MaxDepth         int
	MaxAnchors       int
	MaxEdges         int
	MaxSkipRecords   int
}

// WalkOptions controls a bounded space walk.
type WalkOptions struct {
	IncludeInbox bool
	Limits       Limits
	SkipDirs     []string
}

// ParsedFile is one successfully parsed markdown object from a space walk.
type ParsedFile struct {
	Object  types.Object
	Anchors []types.Anchor
	Edges   []types.Edge
}

// Skip records a file or directory excluded during a bounded walk.
type Skip struct {
	Path   string
	Reason string
}

// WalkStats describes a bounded walk. Skips is capped by MaxSkipRecords.
type WalkStats struct {
	MarkdownSeen   int
	FilesParsed    int
	FilesSkipped   int
	DirsSkipped    int
	BytesRead      int64
	Objects        int
	Anchors        int
	Edges          int
	Skips          []Skip
	SkipsTruncated bool
}

// DefaultWalkOptions returns safe indexing defaults. WalkSpaceWithOptions reads
// directories in fixed-size chunks and does not follow directory symlinks;
// ParseFileWithLimits also refuses file symlinks so a space cannot escape into
// arbitrary filesystem trees.
func DefaultWalkOptions() WalkOptions {
	return WalkOptions{
		Limits: defaultLimits(),
		SkipDirs: []string{
			".git",
			".hg",
			".svn",
			"node_modules",
			"vendor",
			"dist",
		},
	}
}

// ParseFile parses a single mdpp file at path. spaceID is the URI
// authority/name (e.g. "m31labs/hyphae"). Returns the typed object plus
// anchors and edges extracted from the file.
func ParseFile(path, spaceID string) (types.Object, []types.Anchor, []types.Edge, error) {
	return ParseFileWithLimits(path, spaceID, defaultLimits())
}

// ParseFileWithLimits parses one markdown file with hard memory bounds.
func ParseFileWithLimits(path, spaceID string, limits Limits) (types.Object, []types.Anchor, []types.Edge, error) {
	limits = normalizeLimits(limits)

	fi, err := os.Lstat(path)
	if err != nil {
		return types.Object{}, nil, nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return types.Object{}, nil, nil, fmt.Errorf("%w: refusing symlink %s", ErrLimitExceeded, path)
	}
	if !fi.Mode().IsRegular() {
		return types.Object{}, nil, nil, fmt.Errorf("%w: refusing non-regular file %s", ErrLimitExceeded, path)
	}
	if fi.Size() > limits.MaxFileBytes {
		return types.Object{}, nil, nil, fmt.Errorf("%w: %s is %d bytes (max %d)", ErrLimitExceeded, path, fi.Size(), limits.MaxFileBytes)
	}
	data, err := readLimitedFile(path, limits.MaxFileBytes)
	if err != nil {
		return types.Object{}, nil, nil, err
	}

	fm, bodyStart, err := parseFrontmatter(data)
	if err != nil {
		return types.Object{}, nil, nil, fmt.Errorf("parse frontmatter %s: %w", path, err)
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

	bodyRaw := extractBody(data, bodyStart)
	title := extractTitle(bodyRaw)
	summary := extractSummary(bodyRaw)
	body := truncateStringBytes(bodyRaw, limits.MaxBodyBytes)

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

	anchors := extractAnchors(id, spaceID, path, data, bodyStart, limits.MaxAnchors)

	// --- Edges ---

	edges := extractEdges(id, fm, bodyRaw, limits.MaxEdges)

	return obj, anchors, edges, nil
}

// WalkSpace walks every *.md file under spaceRoot (excluding inbox/ unless
// includeInbox is true). spaceID is the URI authority/name.
func WalkSpace(spaceRoot, spaceID string, includeInbox bool) (objects []types.Object, anchors []types.Anchor, edges []types.Edge, err error) {
	opts := DefaultWalkOptions()
	opts.IncludeInbox = includeInbox
	_, err = WalkSpaceWithOptions(spaceRoot, spaceID, opts, func(item ParsedFile) error {
		objects = append(objects, item.Object)
		anchors = append(anchors, item.Anchors...)
		edges = append(edges, item.Edges...)
		return nil
	})
	return
}

// WalkSpaceWithOptions walks a space and invokes visit once per parsed file.
// It keeps only one parsed document and one directory chunk in memory at a time
// unless visit stores more. The recursive directory descent is depth-limited.
func WalkSpaceWithOptions(spaceRoot, spaceID string, opts WalkOptions, visit func(ParsedFile) error) (WalkStats, error) {
	opts.Limits = normalizeLimits(opts.Limits)
	if len(opts.SkipDirs) == 0 {
		opts.SkipDirs = DefaultWalkOptions().SkipDirs
	}
	skipDirs := map[string]bool{}
	for _, dir := range opts.SkipDirs {
		skipDirs[dir] = true
	}

	var stats WalkStats
	const dirReadChunkSize = 128
	var walkDir func(string, int) error
	walkDir = func(dir string, depth int) error {
		if depth > opts.Limits.MaxDepth {
			stats.DirsSkipped++
			addSkip(&stats, opts.Limits, dir, fmt.Sprintf("depth %d exceeds max %d", depth, opts.Limits.MaxDepth))
			return nil
		}

		f, err := os.Open(dir)
		if err != nil {
			return err
		}
		defer f.Close()

		for {
			entries, readErr := f.ReadDir(dirReadChunkSize)
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return readErr
			}
			for _, d := range entries {
				path := filepath.Join(dir, d.Name())
				childDepth := depth + 1
				if d.IsDir() {
					switch {
					case d.Name() == "inbox" && !opts.IncludeInbox:
						stats.DirsSkipped++
						addSkip(&stats, opts.Limits, path, "inbox excluded")
						continue
					case skipDirs[d.Name()]:
						stats.DirsSkipped++
						addSkip(&stats, opts.Limits, path, "bulk/system directory excluded")
						continue
					case childDepth > opts.Limits.MaxDepth:
						stats.DirsSkipped++
						addSkip(&stats, opts.Limits, path, fmt.Sprintf("depth %d exceeds max %d", childDepth, opts.Limits.MaxDepth))
						continue
					default:
						if err := walkDir(path, childDepth); err != nil {
							return err
						}
						continue
					}
				}

				if filepath.Ext(d.Name()) != ".md" {
					continue
				}
				stats.MarkdownSeen++
				if stats.MarkdownSeen > opts.Limits.MaxMarkdownFiles {
					return fmt.Errorf("%w: markdown file count exceeds %d under %s", ErrLimitExceeded, opts.Limits.MaxMarkdownFiles, spaceRoot)
				}
				if childDepth > opts.Limits.MaxDepth {
					stats.FilesSkipped++
					addSkip(&stats, opts.Limits, path, fmt.Sprintf("depth %d exceeds max %d", childDepth, opts.Limits.MaxDepth))
					continue
				}

				obj, anch, edgs, parseErr := ParseFileWithLimits(path, spaceID, opts.Limits)
				if parseErr != nil {
					stats.FilesSkipped++
					addSkip(&stats, opts.Limits, path, parseErr.Error())
					continue
				}

				stats.FilesParsed++
				stats.BytesRead += fileSize(path)
				stats.Objects++
				stats.Anchors += len(anch)
				stats.Edges += len(edgs)
				if visit != nil {
					if err := visit(ParsedFile{Object: obj, Anchors: anch, Edges: edgs}); err != nil {
						return err
					}
				}
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
		}
		return nil
	}
	err := walkDir(spaceRoot, 0)
	return stats, err
}

// --- helpers ---

func defaultLimits() Limits {
	return Limits{
		MaxFileBytes:     DefaultMaxFileBytes,
		MaxBodyBytes:     DefaultMaxBodyBytes,
		MaxMarkdownFiles: DefaultMaxMarkdownFiles,
		MaxDepth:         DefaultMaxDepth,
		MaxAnchors:       DefaultMaxAnchorsPerFile,
		MaxEdges:         DefaultMaxEdgesPerFile,
		MaxSkipRecords:   DefaultMaxSkipRecords,
	}
}

func normalizeLimits(l Limits) Limits {
	def := defaultLimits()
	if l.MaxFileBytes <= 0 {
		l.MaxFileBytes = def.MaxFileBytes
	}
	if l.MaxFileBytes > HardMaxFileBytes {
		l.MaxFileBytes = HardMaxFileBytes
	}
	if l.MaxBodyBytes <= 0 {
		l.MaxBodyBytes = def.MaxBodyBytes
	}
	if l.MaxBodyBytes > HardMaxBodyBytes {
		l.MaxBodyBytes = HardMaxBodyBytes
	}
	if l.MaxMarkdownFiles <= 0 {
		l.MaxMarkdownFiles = def.MaxMarkdownFiles
	}
	if l.MaxMarkdownFiles > HardMaxMarkdownFiles {
		l.MaxMarkdownFiles = HardMaxMarkdownFiles
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = def.MaxDepth
	}
	if l.MaxDepth > HardMaxDepth {
		l.MaxDepth = HardMaxDepth
	}
	if l.MaxAnchors <= 0 {
		l.MaxAnchors = def.MaxAnchors
	}
	if l.MaxAnchors > HardMaxAnchorsPerFile {
		l.MaxAnchors = HardMaxAnchorsPerFile
	}
	if l.MaxEdges <= 0 {
		l.MaxEdges = def.MaxEdges
	}
	if l.MaxEdges > HardMaxEdgesPerFile {
		l.MaxEdges = HardMaxEdgesPerFile
	}
	if l.MaxSkipRecords <= 0 {
		l.MaxSkipRecords = def.MaxSkipRecords
	}
	if l.MaxSkipRecords > HardMaxSkipRecords {
		l.MaxSkipRecords = HardMaxSkipRecords
	}
	return l
}

func readLimitedFile(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: %s grew past %d bytes while reading", ErrLimitExceeded, path, maxBytes)
	}
	return data, nil
}

func truncateStringBytes(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if cut <= 0 {
		cut = maxBytes
	}
	return s[:cut] + "\n\n[hyphae: indexed body truncated]\n"
}

func pathDepth(root, path string) int {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return 0
	}
	return strings.Count(rel, string(os.PathSeparator)) + 1
}

func fileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

func addSkip(stats *WalkStats, limits Limits, path, reason string) {
	if len(stats.Skips) >= limits.MaxSkipRecords {
		stats.SkipsTruncated = true
		return
	}
	stats.Skips = append(stats.Skips, Skip{Path: path, Reason: reason})
}

func parseFrontmatter(data []byte) (map[string]any, int, error) {
	fm := map[string]any{}
	firstEnd, firstNext := nextLine(data, 0)
	if strings.TrimSpace(string(trimCR(data[:firstEnd]))) != "---" {
		return fm, 0, nil
	}

	contentStart := firstNext
	for pos := contentStart; pos <= len(data); {
		lineEnd, next := nextLine(data, pos)
		line := strings.TrimSpace(string(trimCR(data[pos:lineEnd])))
		if line == "---" || line == "..." {
			raw := data[contentStart:pos]
			if len(bytes.TrimSpace(raw)) == 0 {
				return fm, next, nil
			}
			if err := yaml.Unmarshal(raw, &fm); err != nil {
				return nil, 0, err
			}
			if fm == nil {
				fm = map[string]any{}
			}
			return fm, next, nil
		}
		if next <= pos {
			break
		}
		pos = next
	}
	return fm, 0, nil
}

// extractTitle returns the text of the first ATX H1 outside fenced code.
func extractTitle(body string) string {
	for _, h := range scanHeadings([]byte(body), 0, 1) {
		if h.level == 1 {
			return h.text
		}
	}
	return ""
}

// extractSummary returns the first paragraph after the first H1, truncated to
// about 200 chars.
func extractSummary(body string) string {
	foundH1 := false
	var para []string
	inFence := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if isFenceLine(trimmed) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if level, _, ok := parseATXHeading(strings.TrimLeft(line, " \t")); ok {
			if !foundH1 && level == 1 {
				foundH1 = true
				continue
			}
			if len(para) > 0 {
				break
			}
			continue
		}
		if !foundH1 {
			continue
		}
		if trimmed == "" {
			if len(para) > 0 {
				break
			}
			continue
		}
		para = append(para, trimmed)
	}
	if len(para) == 0 {
		return ""
	}
	text := strings.Join(para, " ")
	if len(text) > 200 {
		cut := text[:200]
		if idx := strings.LastIndex(cut, " "); idx > 0 {
			cut = cut[:idx]
		}
		text = cut + "…"
	}
	return text
}

// extractBody returns the full body text: everything after the YAML
// frontmatter block.
func extractBody(data []byte, bodyStart int) string {
	if bodyStart <= 0 || bodyStart > len(data) {
		return string(data)
	}
	return strings.TrimLeft(string(data[bodyStart:]), "\r\n")
}

type headingInfo struct {
	level     int
	text      string
	startByte int
	endByte   int
	startLine int
	endLine   int
}

// extractAnchors builds one Anchor per ATX heading in the document.
func extractAnchors(objectID, spaceID, filePath string, data []byte, bodyStart, maxAnchors int) []types.Anchor {
	headings := scanHeadings(data, bodyStart, maxAnchors)
	if len(headings) == 0 {
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

	for _, h := range headings {
		level := h.level
		text := h.text
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
			StartByte:   h.startByte,
			EndByte:     h.endByte,
			StartLine:   h.startLine,
			EndLine:     h.endLine,
			NodeKind:    "heading",
		})
	}

	return anchors
}

func scanHeadings(data []byte, bodyStart, maxHeadings int) []headingInfo {
	if maxHeadings <= 0 || bodyStart > len(data) {
		return nil
	}
	lineNo := 1
	if bodyStart > 0 {
		lineNo += bytes.Count(data[:bodyStart], []byte("\n"))
	}
	var out []headingInfo
	inFence := false
	for pos := bodyStart; pos <= len(data); {
		lineEnd, next := nextLine(data, pos)
		lineBytes := trimCR(data[pos:lineEnd])
		line := string(lineBytes)
		trimmed := strings.TrimSpace(line)
		if isFenceLine(trimmed) {
			inFence = !inFence
		} else if !inFence {
			if level, text, ok := parseATXHeading(strings.TrimLeft(line, " \t")); ok {
				out = append(out, headingInfo{
					level:     level,
					text:      text,
					startByte: pos,
					endByte:   lineEnd,
					startLine: lineNo,
					endLine:   lineNo,
				})
				if len(out) >= maxHeadings {
					return out
				}
			}
		}
		if next <= pos {
			break
		}
		pos = next
		lineNo++
	}
	return out
}

func parseATXHeading(line string) (int, string, bool) {
	level := 0
	for level < len(line) && level < 6 && line[level] == '#' {
		level++
	}
	if level == 0 || len(line) == level || (line[level] != ' ' && line[level] != '\t') {
		return 0, "", false
	}
	text := strings.TrimSpace(line[level:])
	text = strings.TrimSpace(strings.TrimRight(text, "#"))
	if text == "" {
		return 0, "", false
	}
	return level, text, true
}

func isFenceLine(trimmed string) bool {
	return strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
}

func nextLine(data []byte, pos int) (lineEnd, next int) {
	if pos >= len(data) {
		return len(data), len(data) + 1
	}
	idx := bytes.IndexByte(data[pos:], '\n')
	if idx < 0 {
		return len(data), len(data) + 1
	}
	return pos + idx, pos + idx + 1
}

func trimCR(line []byte) []byte {
	return bytes.TrimSuffix(line, []byte("\r"))
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
func extractEdges(objectID string, fm map[string]any, body string, maxEdges int) []types.Edge {
	var edges []types.Edge
	now := time.Now()

	edgeID := func(kind types.EdgeKind, src, dst string) string {
		return fmt.Sprintf("edge:%s:%s->%s", kind, src, dst)
	}

	makeFrontmatterEdge := func(kind types.EdgeKind, src, dst string) types.Edge {
		dst = resolveWikilink(dst)
		return types.Edge{
			ID:         edgeID(kind, src, dst),
			Kind:       kind,
			SrcID:      src,
			DstID:      dst,
			Confidence: 1.0,
			Derivation: "frontmatter",
			CreatedAt:  now,
		}
	}
	addEdge := func(e types.Edge) bool {
		if len(edges) >= maxEdges {
			return false
		}
		edges = append(edges, e)
		return true
	}

	// related: list → EdgeRelated
	for _, ref := range extractStringList(fm["related"]) {
		dst := stripWikilink(ref)
		if !addEdge(makeFrontmatterEdge(types.EdgeRelated, objectID, dst)) {
			return edges
		}
	}

	// source_refs: list → EdgeSourceRef
	for _, ref := range extractStringList(fm["source_refs"]) {
		dst := stripWikilink(ref)
		if !addEdge(makeFrontmatterEdge(types.EdgeSourceRef, objectID, dst)) {
			return edges
		}
	}

	// applies_to: list → EdgeAppliesTo
	for _, ref := range extractStringList(fm["applies_to"]) {
		dst := stripWikilink(ref)
		if !addEdge(makeFrontmatterEdge(types.EdgeAppliesTo, objectID, dst)) {
			return edges
		}
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
			if !addEdge(makeFrontmatterEdge(types.EdgeSupersededBy, objectID, dst)) {
				return edges
			}
		}
	}

	// Body edges: Markdown hypha:// links (EdgeLinkRef) and [[name]] wikilinks
	// (EdgeWikilink). This scanner intentionally avoids full Markdown parsing.
	seenLink := map[string]bool{}
	seenWiki := map[string]bool{}
	for _, m := range markdownHyphaLinkRe.FindAllStringSubmatch(body, maxEdges-len(edges)) {
		href := m[1]
		if href == "" || seenLink[href] {
			continue
		}
		seenLink[href] = true
		if !addEdge(types.Edge{
			ID:         edgeID(types.EdgeLinkRef, objectID, href),
			Kind:       types.EdgeLinkRef,
			SrcID:      objectID,
			DstID:      href,
			Confidence: 1.0,
			Derivation: "linkref",
			CreatedAt:  now,
		}) {
			return edges
		}
	}
	remaining := maxEdges - len(edges)
	if remaining <= 0 {
		return edges
	}
	for _, m := range wikitextRe.FindAllStringSubmatch(body, remaining) {
		name := strings.TrimSpace(m[1])
		dst := resolveWikilink(name)
		if !seenWiki[dst] {
			seenWiki[dst] = true
			if !addEdge(types.Edge{
				ID:         edgeID(types.EdgeWikilink, objectID, dst),
				Kind:       types.EdgeWikilink,
				SrcID:      objectID,
				DstID:      dst,
				Confidence: 1.0,
				Derivation: "wikilink",
				CreatedAt:  now,
			}) {
				return edges
			}
		}
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
		if len(tv) > maxFrontmatterListItems {
			return tv[:maxFrontmatterListItems]
		}
		return tv
	case []any:
		var out []string
		for _, item := range tv {
			if len(out) >= maxFrontmatterListItems {
				break
			}
			if s := flattenYAMLScalar(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if tv == "" {
			return nil
		}
		if len(tv) > maxFrontmatterScalarBytes {
			tv = tv[:maxFrontmatterScalarBytes]
		}
		return []string{tv}
	}
	return nil
}

// flattenYAMLScalar recursively collapses nested []any structures into a
// single string. YAML parses [[name]] (wikilink syntax) as a doubly-nested
// flow sequence; this function unwraps it back to the inner string.
func flattenYAMLScalar(v any) string {
	return flattenYAMLScalarLimited(v, 0, maxFrontmatterScalarBytes)
}

func flattenYAMLScalarLimited(v any, depth, budget int) string {
	if depth > maxFrontmatterFlattenDepth || budget <= 0 {
		return ""
	}
	switch tv := v.(type) {
	case string:
		if len(tv) > budget {
			return tv[:budget]
		}
		return tv
	case []any:
		var sb strings.Builder
		for _, item := range tv {
			if sb.Len() >= budget {
				break
			}
			part := flattenYAMLScalarLimited(item, depth+1, budget-sb.Len())
			sb.WriteString(part)
		}
		return sb.String()
	case fmt.Stringer:
		s := tv.String()
		if len(s) > budget {
			return s[:budget]
		}
		return s
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, bool:
		s := fmt.Sprintf("%v", v)
		if len(s) > budget {
			return s[:budget]
		}
		return s
	}
	return ""
}
