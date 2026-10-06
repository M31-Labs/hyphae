package spore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// FrontmatterString reads a top-level string using YAML semantics.
func FrontmatterString(source []byte, key string) (string, bool) {
	fm, _, err := parseSigningSource(source)
	if err != nil {
		return "", false
	}
	value, ok := fm[key].(string)
	return value, ok
}

// SetFrontmatterString edits only a scalar's source bytes. It preserves the
// authored body and every other parsed value, including legacy signed fields.
func SetFrontmatterString(source []byte, key, value string) ([]byte, error) {
	fm, body, err := parseSigningSource(source)
	if err != nil {
		return nil, err
	}
	start := bytes.IndexByte(source, '\n') + 1
	end := len(source) - len(body)
	// The last line before body is the closing delimiter.
	closing := bytes.LastIndexByte(bytes.TrimSuffix(source[:end], []byte("\n")), '\n') + 1
	block := source[start:closing]
	var document yaml.Node
	if err := yaml.Unmarshal(block, &document); err != nil {
		return nil, err
	}
	root := document.Content[0]
	var scalar, keyNode *yaml.Node
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			keyNode, scalar = root.Content[i], root.Content[i+1]
			break
		}
	}
	_, isString := fm[key].(string)
	if scalar == nil || !isString || (scalar.Kind != yaml.ScalarNode && scalar.Kind != yaml.AliasNode) {
		return nil, fmt.Errorf("spore: %s must be a string scalar", key)
	}
	offset := 0
	for line := 1; line < scalar.Line; line++ {
		offset += bytes.IndexByte(block[offset:], '\n') + 1
	}
	// YAML columns count characters rather than UTF-8 bytes.
	line := string(block[offset:])
	offset += len(string([]rune(line)[:scalar.Column-1]))
	finish := offset
	newline := "\n"
	if bytes.Contains(block, []byte("\r\n")) {
		newline = "\r\n"
	}
	switch block[offset] {
	case '\'', '"':
		quote := block[offset]
		finish++
		closed := false
		for finish < len(block) {
			ch := block[finish]
			finish++
			if quote == '"' && ch == '\\' {
				finish++
				continue
			}
			if ch == quote {
				if quote == '\'' && finish < len(block) && block[finish] == '\'' {
					finish++
					continue
				}
				closed = true
				break
			}
		}
		if !closed {
			return nil, fmt.Errorf("spore: unterminated %s scalar", key)
		}
	case '|', '>':
		finish = offset + bytes.IndexByte(block[offset:], '\n') + 1
		for finish < len(block) {
			next := bytes.IndexByte(block[finish:], '\n')
			if next < 0 {
				next = len(block) - finish - 1
			}
			current := block[finish : finish+next]
			indent := len(current) - len(bytes.TrimLeft(current, " "))
			if len(bytes.TrimSpace(current)) > 0 && indent < keyNode.Column {
				break
			}
			finish += next + 1
		}
	default:
		for finish < len(block) {
			ch := block[finish]
			if ch == '\n' || ch == '\r' || (root.Style&yaml.FlowStyle != 0 && (ch == ',' || ch == '}')) || (ch == '#' && (finish == offset || block[finish-1] == ' ' || block[finish-1] == '\t')) {
				break
			}
			finish++
		}
		for finish > offset && (block[finish-1] == ' ' || block[finish-1] == '\t') {
			finish--
		}
	}
	encoded, _ := yaml.Marshal(value)
	encoded = bytes.TrimSuffix(encoded, []byte("\n"))
	if scalar.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		encoded = append(encoded, newline...)
	}
	updated := append([]byte{}, source[:start+offset]...)
	updated = append(updated, encoded...)
	updated = append(updated, source[start+finish:]...)
	after, afterBody, err := parseSigningSource(updated)
	if err != nil || after[key] != value || !bytes.Equal(body, afterBody) {
		return nil, fmt.Errorf("spore: cannot preserve YAML while editing %s", key)
	}
	delete(fm, key)
	delete(after, key)
	beforeJSON, beforeErr := canonicalValue(fm)
	afterJSON, afterErr := canonicalValue(after)
	left, _ := json.Marshal(beforeJSON)
	right, _ := json.Marshal(afterJSON)
	if beforeErr != nil || afterErr != nil || !bytes.Equal(left, right) {
		return nil, fmt.Errorf("spore: editing %s would change other frontmatter", key)
	}
	return updated, nil
}

// HasSignature distinguishes a real block or malformed claim from an unsigned
// document. The historical `signature: none` placeholder means unsigned.
func HasSignature(source []byte) bool {
	fm, _, err := parseSigningSource(source)
	return err == nil && fm["signature"] != nil && !isUnsignedMarker(fm["signature"])
}

func IsSpore(source []byte) bool {
	t, ok := FrontmatterString(source, "type")
	return ok && strings.TrimSpace(t) == "spore"
}
