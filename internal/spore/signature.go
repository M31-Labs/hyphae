package spore

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"m31labs.dev/hyphae/internal/identity"
	"m31labs.dev/mdpp"
	"gopkg.in/yaml.v3"
)

// Signature is the structured form of the spore's signature block.
type Signature struct {
	Alg         string    `yaml:"alg" json:"alg"`
	Key         string    `yaml:"key" json:"key"`                   // identity URI
	ContentHash string    `yaml:"content_hash" json:"content_hash"` // "sha256:<hex>"
	SignedAt    time.Time `yaml:"signed_at" json:"signed_at"`
	Value       string    `yaml:"value" json:"value"` // "ed25519:<base64>"
}

// ErrUnsigned is returned by Verify when the spore has no signature block.
var ErrUnsigned = errors.New("spore: not signed")

// IdentityResolver maps an identity URI to a loaded Identity record.
// Return (zero, error) for unknown identities.
type IdentityResolver func(uri string) (identity.Identity, error)

// Sign produces a signed copy of the spore bytes. It computes the canonical
// payload, signs it with priv, and inserts a frontmatter `signature:` block
// just before the closing `---`. signedKey is the public identity URI (e.g.
// "identity://m31labs/odvcencio"). If the input already has a signature block
// it is replaced.
func Sign(source []byte, priv identity.PrivateKey, signedKey string) ([]byte, error) {
	doc, err := mdpp.Parse(source)
	if err != nil {
		return nil, fmt.Errorf("spore: sign: parse: %w", err)
	}

	fm := doc.Frontmatter()
	if fm == nil {
		return nil, fmt.Errorf("spore: sign: no frontmatter block found")
	}

	// Extract required fields for the canonical payload.
	agentID := stringField(fm, "agent.id")
	if agentBlock, ok := fm["agent"].(map[string]any); ok {
		agentID = stringField(agentBlock, "id")
	}
	sporeID := stringField(fm, "id")

	var createdAt time.Time
	switch v := fm["created"].(type) {
	case time.Time:
		createdAt = v.UTC()
	case string:
		t, parseErr := time.Parse(time.RFC3339, v)
		if parseErr != nil {
			return nil, fmt.Errorf("spore: sign: parse created field: %w", parseErr)
		}
		createdAt = t.UTC()
	default:
		return nil, fmt.Errorf("spore: sign: created field missing or invalid type %T", fm["created"])
	}

	// Extract body bytes (excluding any tool-appended work-log section).
	body := signableBody(extractBodyBytes(doc))

	// Compute body hash.
	bodyHash := sha256.Sum256(body)
	bodyHashHex := fmt.Sprintf("%x", bodyHash[:])
	contentHash := "sha256:" + bodyHashHex

	// Build canonical payload.
	payload := buildCanonicalPayload(agentID, sporeID, createdAt, bodyHashHex)

	// Sign.
	sigBytes := identity.Sign(priv, payload)
	sigValue := "ed25519:" + base64.StdEncoding.EncodeToString(sigBytes)

	sig := Signature{
		Alg:         "ed25519",
		Key:         signedKey,
		ContentHash: contentHash,
		SignedAt:    time.Now().UTC().Truncate(time.Second),
		Value:       sigValue,
	}

	// Rewrite the source with the signature block inserted.
	return injectSignature(source, sig)
}

// Verify checks the signature block in source against the canonical payload.
// Returns nil if the signature is valid. Returns ErrUnsigned if there is no
// signature block. Other failures return descriptive errors.
func Verify(source []byte, resolve IdentityResolver) error {
	doc, err := mdpp.Parse(source)
	if err != nil {
		return fmt.Errorf("spore: verify: parse: %w", err)
	}

	fm := doc.Frontmatter()
	if fm == nil {
		return fmt.Errorf("spore: verify: no frontmatter block found")
	}

	// Check for signature block.
	sigRaw, hasSig := fm["signature"]
	if !hasSig || sigRaw == nil {
		return ErrUnsigned
	}

	sig, err := parseSignatureBlock(sigRaw)
	if err != nil {
		return fmt.Errorf("spore: verify: parse signature block: %w", err)
	}

	// Validate alg.
	if sig.Alg != "ed25519" {
		return fmt.Errorf("spore: unsupported signature alg %q", sig.Alg)
	}

	// Resolve the signer identity.
	id, err := resolve(sig.Key)
	if err != nil {
		return fmt.Errorf("spore: unknown signer %q: %w", sig.Key, err)
	}

	// Extract body bytes (excluding any tool-appended work-log section).
	body := signableBody(extractBodyBytes(doc))

	// Verify content hash.
	bodyHash := sha256.Sum256(body)
	bodyHashHex := fmt.Sprintf("%x", bodyHash[:])
	expectedContentHash := "sha256:" + bodyHashHex
	if sig.ContentHash != expectedContentHash {
		return fmt.Errorf("spore: content hash does not match body")
	}

	// Extract spore fields for canonical payload.
	agentID := ""
	if agentBlock, ok := fm["agent"].(map[string]any); ok {
		agentID = stringField(agentBlock, "id")
	}
	sporeID := stringField(fm, "id")

	var createdAt time.Time
	switch v := fm["created"].(type) {
	case time.Time:
		createdAt = v.UTC()
	case string:
		t, parseErr := time.Parse(time.RFC3339, v)
		if parseErr != nil {
			return fmt.Errorf("spore: verify: parse created field: %w", parseErr)
		}
		createdAt = t.UTC()
	default:
		return fmt.Errorf("spore: verify: created field missing or invalid type %T", fm["created"])
	}

	// Build canonical payload.
	payload := buildCanonicalPayload(agentID, sporeID, createdAt, bodyHashHex)

	// Decode signature value.
	const ed25519Prefix = "ed25519:"
	if !strings.HasPrefix(sig.Value, ed25519Prefix) {
		return fmt.Errorf("spore: signature value missing ed25519: prefix")
	}
	sigBytes, err := base64.StdEncoding.DecodeString(sig.Value[len(ed25519Prefix):])
	if err != nil {
		return fmt.Errorf("spore: decode signature value: %w", err)
	}

	// Verify the signature.
	if !identity.Verify(id, payload, sigBytes) {
		return fmt.Errorf("spore: signature verification failed")
	}

	return nil
}

// buildCanonicalPayload assembles the deterministic byte payload over which the
// signature is computed:
//
//	agent.id\n
//	spore.id\n
//	created (RFC3339)\n
//	sha256hex-of-body\n
func buildCanonicalPayload(agentID, sporeID string, createdAt time.Time, bodyHashHex string) []byte {
	var sb strings.Builder
	sb.WriteString(agentID)
	sb.WriteByte('\n')
	sb.WriteString(sporeID)
	sb.WriteByte('\n')
	sb.WriteString(createdAt.UTC().Format(time.RFC3339))
	sb.WriteByte('\n')
	sb.WriteString(bodyHashHex)
	sb.WriteByte('\n')
	return []byte(sb.String())
}

// extractBodyBytes returns the raw body bytes (everything after the closing
// `---` of the frontmatter block).
// workLogMarker is the heading `hypha trace done --link-spore` appends to a
// spore body (see internal/trace.appendWorkLogToSpore). The leading newline is
// part of the appended block, so matching it lets signableBody recover the
// original authored body byte-for-byte.
var workLogMarker = []byte("\n## Work log (trace.")

// signableBody returns the portion of the body the signature covers: the
// authored content, excluding any work-log section appended by trace-done
// after signing. A body with no work log is returned unchanged, so signing
// and verification agree and pre-existing signatures stay valid. Tampering
// anywhere in the authored region (or via any other appended text) still
// changes the hash and fails verification — only the specific tool-generated
// work-log section is exempt.
func signableBody(body []byte) []byte {
	if i := bytes.Index(body, workLogMarker); i >= 0 {
		return body[:i]
	}
	return body
}

func extractBodyBytes(doc *mdpp.Document) []byte {
	if doc == nil || doc.Root == nil {
		return nil
	}
	fmEnd := 0
	for _, child := range doc.Root.Children {
		if child != nil && child.Type.String() == "Frontmatter" {
			fmEnd = child.Range.EndByte
			break
		}
	}
	if fmEnd == 0 || fmEnd >= len(doc.Source) {
		return nil
	}
	return doc.Source[fmEnd:]
}

// injectSignature rewrites source to include sig as the `signature:` block
// just before the closing `---` of the frontmatter. Any pre-existing
// `signature:` block is removed first.
func injectSignature(source []byte, sig Signature) ([]byte, error) {
	// Find the frontmatter boundaries in the raw bytes.
	// The frontmatter is delimited by the first `---\n` at position 0 and the
	// next `---\n` (or `---` at EOF).
	if !strings.HasPrefix(string(source), "---") {
		return nil, fmt.Errorf("spore: sign: source does not start with frontmatter delimiter")
	}

	// Find the closing `---`.
	closingIdx := findFrontmatterClose(source)
	if closingIdx < 0 {
		return nil, fmt.Errorf("spore: sign: could not locate closing --- of frontmatter")
	}

	// Split: everything from start up to (but not including) the closing `---`,
	// the closing delimiter, and everything after.
	fmContent := source[4:closingIdx] // skip the opening "---\n"
	rest := source[closingIdx:]       // "---\n..." (body)

	// Strip any existing `signature:` block from the frontmatter YAML.
	fmContent = removeSignatureBlock(fmContent)

	// Render the new signature block as YAML.
	sigYAML, err := renderSignatureYAML(sig)
	if err != nil {
		return nil, fmt.Errorf("spore: sign: render signature yaml: %w", err)
	}

	var out strings.Builder
	out.WriteString("---\n")
	out.Write(fmContent)
	out.WriteString(sigYAML)
	out.Write(rest)

	return []byte(out.String()), nil
}

// findFrontmatterClose locates the byte offset of the closing `---` line in
// source, searching after the opening `---\n`. Returns -1 if not found.
func findFrontmatterClose(source []byte) int {
	s := string(source)
	// Skip the opening delimiter (first line must be "---\n").
	start := 4
	if len(s) < start {
		return -1
	}
	// Look for `\n---\n` or `\n---` at end of file.
	idx := strings.Index(s[start:], "\n---")
	if idx < 0 {
		return -1
	}
	// Return offset to just after the \n (i.e. the position of `---`).
	return start + idx + 1
}

// removeSignatureBlock strips a `signature:` key (and its sub-keys) from raw
// YAML frontmatter bytes. It works by unmarshaling, deleting the key, and
// re-marshaling.
func removeSignatureBlock(fmContent []byte) []byte {
	var node yaml.Node
	if err := yaml.Unmarshal(fmContent, &node); err != nil {
		// Cannot parse; return unchanged.
		return fmContent
	}
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		node.Content[0] = deleteYAMLKey(node.Content[0], "signature")
	}
	out, err := yaml.Marshal(&node)
	if err != nil {
		return fmContent
	}
	// yaml.Marshal adds a "---\n" document header; strip it.
	out = stripYAMLDocHeader(out)
	return out
}

// deleteYAMLKey removes a key from a YAML mapping node in-place and returns
// the modified node.
func deleteYAMLKey(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return n
	}
	newContent := make([]*yaml.Node, 0, len(n.Content))
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i]
		if k.Value == key {
			continue // skip this key and its value
		}
		newContent = append(newContent, n.Content[i], n.Content[i+1])
	}
	n.Content = newContent
	return n
}

// stripYAMLDocHeader removes a leading "---\n" document header that
// yaml.Marshal emits.
func stripYAMLDocHeader(b []byte) []byte {
	s := string(b)
	if strings.HasPrefix(s, "---\n") {
		return []byte(s[4:])
	}
	return b
}

// renderSignatureYAML renders a Signature as a YAML `signature:` block
// suitable for embedding in frontmatter. The output ends with a newline.
func renderSignatureYAML(sig Signature) (string, error) {
	// Build the block manually for deterministic output (no yaml.Marshal
	// key-ordering surprises).
	var sb strings.Builder
	sb.WriteString("signature:\n")
	sb.WriteString(fmt.Sprintf("  alg: %s\n", sig.Alg))
	sb.WriteString(fmt.Sprintf("  key: %s\n", sig.Key))
	sb.WriteString(fmt.Sprintf("  content_hash: %s\n", sig.ContentHash))
	sb.WriteString(fmt.Sprintf("  signed_at: %s\n", sig.SignedAt.UTC().Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("  value: %s\n", sig.Value))
	return sb.String(), nil
}

// parseSignatureBlock converts the raw frontmatter value for "signature" into
// a Signature struct.
func parseSignatureBlock(raw any) (Signature, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return Signature{}, fmt.Errorf("signature block is not a mapping")
	}
	alg, _ := m["alg"].(string)
	key, _ := m["key"].(string)
	contentHash, _ := m["content_hash"].(string)
	value, _ := m["value"].(string)

	var signedAt time.Time
	switch v := m["signed_at"].(type) {
	case time.Time:
		signedAt = v.UTC()
	case string:
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return Signature{}, fmt.Errorf("parse signed_at: %w", err)
		}
		signedAt = t.UTC()
	}

	return Signature{
		Alg:         alg,
		Key:         key,
		ContentHash: contentHash,
		SignedAt:    signedAt,
		Value:       value,
	}, nil
}
