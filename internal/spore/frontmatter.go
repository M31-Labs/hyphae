package spore

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrNonCanonicalSignature is returned when an existing semantic root
// signature is not exactly the canonical block emitted by renderSignatureYAML.
// Pre-v1 signing deliberately rejects such input instead of guessing which
// bytes belong to the signature mapping.
var ErrNonCanonicalSignature = errors.New("spore: noncanonical signature block")

// frontmatterRange identifies the opening and closing delimiters in raw source.
// openEnd is the first byte after the opening delimiter; closeStart points at
// the first byte of the closing delimiter.
type frontmatterRange struct {
	openEnd          int
	closeStart       int
	documentEndStart int
	lineEnding       string
}

// frontmatterBounds locates a frontmatter block without interpreting its YAML.
// In particular, a line containing `---` inside an indented block scalar is not
// treated as the closing delimiter. A root-column `...` marker is retained as
// part of the raw frontmatter so callers can insert before it.
func frontmatterBounds(source []byte) (frontmatterRange, bool) {
	var bounds frontmatterRange
	bounds.documentEndStart = -1
	switch {
	case bytes.HasPrefix(source, []byte("---\r\n")):
		bounds.openEnd = len("---\r\n")
		bounds.lineEnding = "\r\n"
	case bytes.HasPrefix(source, []byte("---\n")):
		bounds.openEnd = len("---\n")
		bounds.lineEnding = "\n"
	default:
		return frontmatterRange{}, false
	}

	for lineStart := bounds.openEnd; lineStart <= len(source); {
		relEnd := bytes.IndexByte(source[lineStart:], '\n')
		lineEnd := len(source)
		nextLine := len(source)
		if relEnd >= 0 {
			lineEnd = lineStart + relEnd
			nextLine = lineEnd + 1
		}
		line := bytes.TrimSuffix(source[lineStart:lineEnd], []byte{'\r'})
		if bytes.Equal(line, []byte("---")) {
			bounds.closeStart = lineStart
			raw := source[bounds.openEnd:bounds.closeStart]
			marker, count := explicitDocumentEndOffset(raw)
			if count > 0 {
				bounds.documentEndStart = bounds.openEnd + marker
			}
			return bounds, true
		}
		if relEnd < 0 {
			break
		}
		lineStart = nextLine
	}
	return frontmatterRange{}, false
}

// isExplicitDocumentEndLine recognizes the supported root-column YAML document
// end marker. Indented `...` lines remain ordinary scalar/comment content.
func isExplicitDocumentEndLine(line []byte) bool {
	if !bytes.HasPrefix(line, []byte("...")) {
		return false
	}
	rest := line[3:]
	if len(rest) == 0 || rest[0] == '#' {
		return true
	}
	for len(rest) > 0 && (rest[0] == ' ' || rest[0] == '\t') {
		rest = rest[1:]
	}
	return len(rest) == 0 || rest[0] == '#'
}

// explicitDocumentEndOffset returns the first root-column document-end marker
// and the total marker count in raw frontmatter. The stream preflight rejects
// counts other than zero or one; keeping this scan separate lets replacement
// preserve the marker and all bytes after it exactly.
func explicitDocumentEndOffset(raw []byte) (first, count int) {
	first = -1
	for lineStart := 0; lineStart <= len(raw); {
		relEnd := bytes.IndexByte(raw[lineStart:], '\n')
		lineEnd := len(raw)
		nextLine := len(raw)
		if relEnd >= 0 {
			lineEnd = lineStart + relEnd
			nextLine = lineEnd + 1
		}
		line := bytes.TrimSuffix(raw[lineStart:lineEnd], []byte{'\r'})
		if isExplicitDocumentEndLine(line) {
			if first < 0 {
				first = lineStart
			}
			count++
		}
		if relEnd < 0 {
			break
		}
		lineStart = nextLine
	}
	return first, count
}

// findFrontmatterClose preserves the delimiter scanner's historical offset
// helper for package-local callers. It does not inspect or rewrite YAML.
func findFrontmatterClose(source []byte) int {
	bounds, ok := frontmatterBounds(source)
	if !ok {
		return -1
	}
	return bounds.closeStart
}

type frontmatterSignature struct {
	key   *yaml.Node
	value *yaml.Node
}

type parsedFrontmatter struct {
	document   *yaml.Node
	mapping    *yaml.Node
	signatures []frontmatterSignature
}

// parseFrontmatterYAML parses raw frontmatter only for semantic structure and
// source locations. It never marshals or otherwise rewrites the YAML. The
// stream must contain exactly one YAML document; a second Decode must return
// io.EOF.
func parseFrontmatterYAML(raw []byte) (*parsedFrontmatter, error) {
	_, markerCount := explicitDocumentEndOffset(raw)
	if markerCount > 1 {
		return nil, fmt.Errorf("spore: frontmatter YAML has multiple document-end markers")
	}

	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("spore: frontmatter YAML parse: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("spore: frontmatter YAML contains multiple documents")
		}
		return nil, fmt.Errorf("spore: frontmatter YAML trailing stream: %w", err)
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return nil, fmt.Errorf("spore: frontmatter YAML root is not one document")
	}
	mapping := document.Content[0]
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("spore: frontmatter YAML root is not a mapping")
	}
	if len(mapping.Content)%2 != 0 {
		return nil, fmt.Errorf("spore: frontmatter YAML mapping has an incomplete key/value pair")
	}
	if err := validateFrontmatterNode(&document, mapping); err != nil {
		return nil, err
	}

	parsed := &parsedFrontmatter{
		document: &document,
		mapping:  mapping,
	}
	for i := 0; i < len(mapping.Content); i += 2 {
		key := mapping.Content[i]
		value := mapping.Content[i+1]
		if key.Value == "signature" {
			parsed.signatures = append(parsed.signatures, frontmatterSignature{key: key, value: value})
		}
	}
	return parsed, nil
}

// validateFrontmatterNode defines the supported root YAML subset. Root keys
// must be ordinary scalar strings; aliases, merges, complex keys, and custom
// key tags are rejected without expanding any merge or alias value.
func validateFrontmatterNode(document, mapping *yaml.Node) error {
	if err := validateYAMLNodeGraph(document); err != nil {
		return err
	}
	for i := 0; i < len(mapping.Content); i += 2 {
		key := mapping.Content[i]
		if key == nil || key.Kind == yaml.AliasNode {
			return fmt.Errorf("spore: frontmatter YAML root key is an alias")
		}
		if key.Kind != yaml.ScalarNode {
			return fmt.Errorf("spore: frontmatter YAML root key is not a scalar string")
		}
		if key.Tag == "!!merge" || (key.Value == "<<" && key.Style == 0) {
			return fmt.Errorf("spore: frontmatter YAML root merge key is not supported")
		}
		if key.Tag != "!!str" {
			return fmt.Errorf("spore: frontmatter YAML root key has unsupported tag %q", key.Tag)
		}
	}
	return nil
}

// validateYAMLNodeGraph rejects alias cycles while allowing ordinary acyclic
// aliases in values. It intentionally traverses aliases only for safety; it
// never expands or rewrites the source bytes.
func validateYAMLNodeGraph(root *yaml.Node) error {
	active := make(map[*yaml.Node]bool)
	var visit func(*yaml.Node) error
	visit = func(node *yaml.Node) error {
		if node == nil {
			return fmt.Errorf("spore: frontmatter YAML contains a nil node")
		}
		if active[node] {
			return fmt.Errorf("spore: frontmatter YAML contains an alias cycle")
		}
		active[node] = true
		defer delete(active, node)
		if node.Kind == yaml.AliasNode {
			if node.Alias == nil {
				return fmt.Errorf("spore: frontmatter YAML contains an unresolved alias")
			}
			return visit(node.Alias)
		}
		for _, child := range node.Content {
			if err := visit(child); err != nil {
				return err
			}
		}
		return nil
	}
	return visit(root)
}

func (p *parsedFrontmatter) singleSignature() (*frontmatterSignature, error) {
	switch len(p.signatures) {
	case 0:
		return nil, nil
	case 1:
		return &p.signatures[0], nil
	default:
		return nil, nonCanonicalSignatureError("multiple semantic root signature keys")
	}
}

func nonCanonicalSignatureError(reason string) error {
	return fmt.Errorf("%w: %s", ErrNonCanonicalSignature, reason)
}

func decodeSignatureNode(node *yaml.Node) (Signature, error) {
	if node == nil {
		return Signature{}, fmt.Errorf("signature value is missing")
	}
	var raw any
	if err := node.Decode(&raw); err != nil {
		return Signature{}, fmt.Errorf("decode signature value: %w", err)
	}
	return parseSignatureBlock(raw)
}

// yamlNodeByteOffset converts a yaml.v3 node's 1-based line/column location to
// a byte offset in the exact raw frontmatter passed to yaml.Unmarshal.
func yamlNodeByteOffset(source []byte, node *yaml.Node) (int, error) {
	if node == nil || node.Line < 1 || node.Column < 1 {
		return 0, fmt.Errorf("signature key has no valid YAML source location")
	}
	lineStart := 0
	for line := 1; line < node.Line; line++ {
		relEnd := bytes.IndexByte(source[lineStart:], '\n')
		if relEnd < 0 {
			return 0, fmt.Errorf("signature key line %d is outside frontmatter", node.Line)
		}
		lineStart += relEnd + 1
	}
	lineEnd := len(source)

	if relEnd := bytes.IndexByte(source[lineStart:], '\n'); relEnd >= 0 {
		lineEnd = lineStart + relEnd
	}
	column := node.Column - 1
	if column > lineEnd-lineStart {
		return 0, fmt.Errorf("signature key column %d is outside its YAML line", node.Column)
	}
	return lineStart + column, nil
}

func yamlNodeHasComment(node *yaml.Node) bool {
	return node != nil && (node.HeadComment != "" || node.LineComment != "" || node.FootComment != "")
}

func validateSignatureMapping(node *yaml.Node) error {
	if node == nil || node.Kind != yaml.MappingNode {
		return nonCanonicalSignatureError("signature value is not a mapping")
	}
	if yamlNodeHasComment(node) {
		return nonCanonicalSignatureError("signature mapping contains a comment")
	}
	required := []string{"alg", "key", "content_hash", "signed_at", "value"}
	expected := make(map[string]bool, len(required))
	for _, field := range required {
		expected[field] = true
	}
	seen := make(map[string]bool, len(expected))
	if len(node.Content)%2 != 0 {
		return nonCanonicalSignatureError("signature mapping has an incomplete key/value pair")
	}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i]
		value := node.Content[i+1]
		if yamlNodeHasComment(key) || yamlNodeHasComment(value) {
			return nonCanonicalSignatureError("signature mapping contains a comment")
		}
		if key == nil || !expected[key.Value] {
			return nonCanonicalSignatureError("signature mapping has an extra field")
		}
		if seen[key.Value] {
			return nonCanonicalSignatureError("signature mapping has a duplicate field")
		}
		seen[key.Value] = true
	}
	for _, field := range required {
		if !seen[field] {
			return nonCanonicalSignatureError("signature mapping is missing a required field: " + field)
		}
	}
	return nil
}

func canonicalSignatureYAML(sig Signature, lineEnding string) ([]byte, error) {
	rendered, err := renderSignatureYAML(sig)
	if err != nil {
		return nil, err
	}
	if lineEnding == "\r\n" {
		rendered = strings.ReplaceAll(rendered, "\n", "\r\n")
	}
	return []byte(rendered), nil
}

// canonicalSignatureRange validates an existing semantic signature against the
// exact canonical bytes and returns its byte range in raw frontmatter.
func canonicalSignatureRange(raw []byte, lineEnding string, parsed *parsedFrontmatter) (Signature, int, int, error) {
	if parsed == nil {
		return Signature{}, 0, 0, nonCanonicalSignatureError("frontmatter parse result is nil")
	}
	entry, err := parsed.singleSignature()
	if err != nil {
		return Signature{}, 0, 0, err
	}
	if entry == nil {
		return Signature{}, 0, 0, nonCanonicalSignatureError("signature mapping is missing")
	}
	if err := validateSignatureMapping(entry.value); err != nil {
		return Signature{}, 0, 0, err
	}
	sig, err := decodeSignatureNode(entry.value)
	if err != nil {
		return Signature{}, 0, 0, nonCanonicalSignatureError(err.Error())
	}
	canonical, err := canonicalSignatureYAML(sig, lineEnding)
	if err != nil {
		return Signature{}, 0, 0, nonCanonicalSignatureError(fmt.Sprintf("render canonical block: %v", err))
	}
	start, err := yamlNodeByteOffset(raw, entry.key)
	if err != nil {
		return Signature{}, 0, 0, nonCanonicalSignatureError(err.Error())
	}
	end := start + len(canonical)
	if start < 0 || end < start || end > len(raw) || !bytes.Equal(raw[start:end], canonical) {
		return Signature{}, 0, 0, nonCanonicalSignatureError("existing block is not the canonical rendered signature")
	}
	return sig, start, end, nil
}

// cleanFrontmatter removes exactly one canonical signature byte range. It
// returns the cleaned bytes and the offset where a replacement signature must
// be inserted. Unsigned frontmatter is copied byte-for-byte and gets an
// insertion offset immediately before a supported `...` marker, or at the
// closing delimiter when no marker exists. Signed frontmatter gets the old
// block's exact start offset. It never consumes surrounding bytes heuristically.
func cleanFrontmatter(raw []byte, lineEnding string) ([]byte, int, error) {
	parsed, err := parseFrontmatterYAML(raw)
	if err != nil {
		return nil, 0, nonCanonicalSignatureError(err.Error())
	}
	entry, err := parsed.singleSignature()
	if err != nil {
		return nil, 0, err
	}
	if entry == nil {
		marker, _ := explicitDocumentEndOffset(raw)
		insertionOffset := len(raw)
		if marker >= 0 {
			insertionOffset = marker
		}
		return append([]byte(nil), raw...), insertionOffset, nil
	}
	_, start, end, err := canonicalSignatureRange(raw, lineEnding, parsed)
	if err != nil {
		return nil, 0, err
	}
	cleaned := make([]byte, 0, len(raw)-(end-start))
	cleaned = append(cleaned, raw[:start]...)
	cleaned = append(cleaned, raw[end:]...)
	return cleaned, start, nil
}

// errFrontmatterNotFound lets Verify preserve its historical no-frontmatter
// diagnostic while both Sign and Verify share the same YAML stream/root-key
// preflight whenever delimiters are present.
var errFrontmatterNotFound = errors.New("frontmatter delimiters not found")

func preflightFrontmatter(source []byte) (frontmatterRange, []byte, *parsedFrontmatter, error) {
	bounds, ok := frontmatterBounds(source)
	if !ok {
		return frontmatterRange{}, nil, nil, errFrontmatterNotFound
	}
	raw := source[bounds.openEnd:bounds.closeStart]
	parsed, err := parseFrontmatterYAML(raw)
	if err != nil {
		return frontmatterRange{}, nil, nil, nonCanonicalSignatureError(err.Error())
	}
	return bounds, raw, parsed, nil
}

// validateSignInput performs the raw frontmatter semantic preflight required by
// Sign. It runs before mdpp.Parse so malformed or noncanonical signature YAML
// has one stable, explicit failure mode instead of being hidden by a generic
// document-parser error.
func validateSignInput(source []byte) error {
	bounds, raw, parsed, err := preflightFrontmatter(source)
	if err != nil {
		return err
	}
	entry, err := parsed.singleSignature()
	if err != nil {
		return err
	}
	if entry == nil {
		return nil
	}
	_, _, _, err = canonicalSignatureRange(raw, bounds.lineEnding, parsed)
	return err
}
