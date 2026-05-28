package crdtshadow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"m31labs.dev/gosx/crdt"
	"m31labs.dev/gosx/crdt/encoding"
	"m31labs.dev/hyphae/internal/atomicfs"
	"m31labs.dev/mdpp"
)

// Per-file sub-map keys.
const (
	canonKeyFrontmatter = "__frontmatter"
	canonKeyPreamble    = "__preamble"
	canonKeyOrder       = "__order"
)

// canonicalSection is one block of a canonical .md file as the
// shadow stores it. Block boundaries follow mdpp headings: each
// section spans from one heading's StartByte to the next
// same-or-higher-level heading's StartByte (or EOF).
type canonicalSection struct {
	Slug  string // unique heading slug within the file
	Bytes []byte // verbatim source bytes including the heading line itself
}

// decomposeMarkdown splits a canonical-file body into the pieces the
// shadow stores: frontmatter bytes, the preamble (anything between
// frontmatter and the first heading), and one section per heading.
// Section slugs are made unique within the file by appending -2,
// -3, … on collision.
func decomposeMarkdown(content []byte) (frontmatter, preamble []byte, sections []canonicalSection) {
	doc, err := mdpp.Parse(content)
	if err != nil || doc == nil {
		// Best-effort fallback: stash the whole file as the preamble
		// so it round-trips losslessly (just won't merge structurally).
		return nil, append([]byte{}, content...), nil
	}

	fmStart, fmEnd := 0, 0
	for _, child := range doc.AST().Children {
		if child.Type == mdpp.NodeFrontmatter {
			fmStart = child.Range.StartByte
			fmEnd = child.Range.EndByte
			break
		}
	}
	if fmEnd > 0 {
		frontmatter = append([]byte{}, content[fmStart:fmEnd]...)
	}

	headings := doc.AST().Find(mdpp.NodeHeading)
	if len(headings) == 0 {
		preamble = append([]byte{}, content[fmEnd:]...)
		return
	}

	// Preamble: anything between frontmatter end and the first heading.
	preStart := fmEnd
	if first := headings[0].Range.StartByte; first > preStart {
		preamble = append([]byte{}, content[preStart:first]...)
	}

	// Each section owns its prose only; deeper sub-headings are
	// independent sections that get spliced back in by Materialize in
	// document order. So a section spans from its heading to the NEXT
	// heading of any level (or EOF).
	seenSlugs := make(map[string]int)
	for i, h := range headings {
		sectionStart := h.Range.StartByte
		sectionEnd := len(content)
		if i+1 < len(headings) {
			sectionEnd = headings[i+1].Range.StartByte
		}
		raw := slugify(strings.TrimSpace(h.Text()))
		if raw == "" {
			raw = fmt.Sprintf("section-%d", i+1)
		}
		seenSlugs[raw]++
		slug := raw
		if seenSlugs[raw] > 1 {
			slug = fmt.Sprintf("%s-%d", raw, seenSlugs[raw])
		}
		sections = append(sections, canonicalSection{
			Slug:  slug,
			Bytes: append([]byte{}, content[sectionStart:sectionEnd]...),
		})
	}
	return
}

// slugify mirrors the parser/graft slug rule: lowercase, runs of
// non-alphanumeric → single hyphen, trim edges.
func slugify(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	inDash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			inDash = false
		default:
			if !inDash {
				b.WriteByte('-')
				inDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// recordCanonicalSectioned stores the decomposed file as flat keys
// under canonicalObj: "<rel>:__frontmatter", "<rel>:__preamble",
// "<rel>:__order", and "<rel>:<slug>" per section. Caller holds s.mu.
//
// The flat layout avoids the LWW-on-MakeMap problem: if two peers
// independently MakeMap'd a per-file sub-map, only one sub-map would
// survive merge and the other's section ops would be orphaned. With
// flat keys, each section competes for LWW only with its own peer
// section, leaving untouched sections undisturbed.
//
// Only sections whose bytes actually changed get Put'd, so an
// untouched section keeps its existing OpID and doesn't compete with
// remote edits on merge.
func (s *Shadow) recordCanonicalSectioned(rel string, content []byte) error {
	fm, pre, sections := decomposeMarkdown(content)

	putIfChanged := func(key string, newBytes []byte) error {
		if cur, ok := s.readBytesLocked(crdt.Root, key); ok && bytesEqual(cur, newBytes) {
			return nil
		}
		if err := s.doc.Put(crdt.Root, crdt.Prop(key), crdt.BytesValue(append([]byte{}, newBytes...))); err != nil {
			return fmt.Errorf("crdtshadow: put %s: %w", key, err)
		}
		return nil
	}

	if err := putIfChanged(canonicalKey(rel, canonKeyFrontmatter), fm); err != nil {
		return err
	}
	if err := putIfChanged(canonicalKey(rel, canonKeyPreamble), pre); err != nil {
		return err
	}

	order := make([]string, 0, len(sections))
	for _, sec := range sections {
		order = append(order, sec.Slug)
	}
	orderBlob, err := json.Marshal(order)
	if err != nil {
		return err
	}
	if err := putIfChanged(canonicalKey(rel, canonKeyOrder), orderBlob); err != nil {
		return err
	}

	for _, sec := range sections {
		if err := putIfChanged(canonicalKey(rel, sec.Slug), sec.Bytes); err != nil {
			return err
		}
	}
	if _, err := s.doc.Commit("canonical:" + rel); err != nil {
		return err
	}
	return s.persistLocked(false)
}

// canonicalKey builds the Root-level flat key for one section of one
// file: "canonical\x00<rel>\x00<slug>". Three components are unambiguous
// because slugify strips NULs and rels don't contain them either.
func canonicalKey(rel, slug string) string {
	return flatKey(prefixCanonical, rel+"\x00"+slug)
}

// parseCanonicalKey is the inverse: returns (rel, slug, ok). It only
// matches keys produced by canonicalKey.
func parseCanonicalKey(key string) (rel, slug string, ok bool) {
	prefix, rest, ok := parseFlatKey(key)
	if !ok || prefix != prefixCanonical {
		return "", "", false
	}
	idx := strings.IndexByte(rest, '\x00')
	if idx < 0 {
		return "", "", false
	}
	return rest[:idx], rest[idx+1:], true
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// MaterializeCanonical reconstructs a canonical file from its CRDT
// sections and returns the bytes. Order = stored __order; missing
// entries are skipped. Returns nil + nil if the file has no
// canonical entries at all (i.e. nothing to materialize).
func (s *Shadow) MaterializeCanonical(rel string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.materializeCanonicalLocked(rel)
}

func (s *Shadow) materializeCanonicalLocked(rel string) ([]byte, error) {
	var out []byte
	if fm, ok := s.readBytesLocked(crdt.Root, canonicalKey(rel, canonKeyFrontmatter)); ok {
		out = append(out, fm...)
	}
	if pre, ok := s.readBytesLocked(crdt.Root, canonicalKey(rel, canonKeyPreamble)); ok {
		out = append(out, pre...)
	}

	order, _ := s.readBytesLocked(crdt.Root, canonicalKey(rel, canonKeyOrder))
	var slugs []string
	if len(order) > 0 {
		_ = json.Unmarshal(order, &slugs)
	}
	for _, slug := range slugs {
		if section, ok := s.readBytesLocked(crdt.Root, canonicalKey(rel, slug)); ok {
			out = append(out, section...)
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// MaterializeAll walks every canonical entry and writes any whose
// stored bytes differ from what's on disk. Returns the list of paths
// that were updated. Used after Pull to land remote canonical edits.
func (s *Shadow) MaterializeAll() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Save() returns gosx-envelope-wrapped JSON; peel the envelope
	// before unmarshaling so we can list canonical-sub-map keys
	// without adding a "list keys" API to gosx.
	raw, err := s.doc.Save()
	if err != nil {
		return nil, fmt.Errorf("crdtshadow: doc.Save: %w", err)
	}
	body, err := encoding.DecodeDocument(raw)
	if err != nil {
		return nil, fmt.Errorf("crdtshadow: decode snapshot envelope: %w", err)
	}
	var snap map[string]any
	if err := json.Unmarshal(body, &snap); err != nil {
		return nil, fmt.Errorf("crdtshadow: parse snapshot: %w", err)
	}
	objects, _ := snap["objects"].(map[string]any)
	root, _ := objects[string(crdt.Root)].(map[string]any)
	rootMap, _ := root["map"].(map[string]any)

	// Collect every distinct rel path under the canonical prefix.
	rels := make(map[string]struct{})
	for key := range rootMap {
		if rel, _, ok := parseCanonicalKey(key); ok {
			rels[rel] = struct{}{}
		}
	}

	var changed []string
	for rel := range rels {
		content, err := s.materializeCanonicalLocked(rel)
		if err != nil || content == nil {
			continue
		}
		abs := filepath.Join(s.spaceRoot, rel)
		if existing, rerr := readIfExists(abs); rerr == nil && existing != nil &&
			string(existing) == string(content) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return changed, fmt.Errorf("crdtshadow: mkdir %s: %w", abs, err)
		}
		if err := atomicfs.WriteFile(abs, content, 0o644); err != nil {
			return changed, fmt.Errorf("crdtshadow: materialize %s: %w", abs, err)
		}
		changed = append(changed, rel)
	}
	return changed, nil
}

func (s *Shadow) readBytesLocked(parent crdt.ObjID, key string) ([]byte, bool) {
	val, _, err := s.doc.Get(parent, crdt.Prop(key))
	if err != nil {
		return nil, false
	}
	if val.Kind != crdt.ValueKindBytes {
		return nil, false
	}
	return append([]byte{}, val.Bytes...), true
}
