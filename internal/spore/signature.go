package spore

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"m31labs.dev/hyphae/internal/identity"
	"m31labs.dev/mdpp"
)

// Signature is the structured form of the spore's signature block.
type Signature struct {
	Version         int       `yaml:"version,omitempty" json:"version"`
	Alg             string    `yaml:"alg" json:"alg"`
	Key             string    `yaml:"key" json:"key"` // identity URI
	BodyHash        string    `yaml:"body_hash,omitempty" json:"body_hash,omitempty"`
	FrontmatterHash string    `yaml:"frontmatter_hash,omitempty" json:"frontmatter_hash,omitempty"`
	ContentHash     string    `yaml:"content_hash" json:"content_hash"` // "sha256:<hex>"
	SignedAt        time.Time `yaml:"signed_at" json:"signed_at"`
	Value           string    `yaml:"value" json:"value"` // "ed25519:<base64>"
}

// ErrUnsigned is returned by Verify when the spore has no signature block.
var ErrUnsigned = errors.New("spore: not signed")

// IdentityResolver maps an identity URI to a loaded Identity record.
// Return (zero, error) for unknown identities.
type IdentityResolver func(uri string) (identity.Identity, error)

// Sign writes a v2 signature over the canonical payload. See signature_v2.go
// and docs/spore-signatures.md for the encoding and covered fields.
func Sign(source []byte, priv identity.PrivateKey, signedKey string) ([]byte, error) {
	return signV2(source, priv, signedKey)
}

// Verify accepts v2 and legacy v1 signatures. Use VerifyDetailed for scope and
// hash diagnostics. No source bytes or identity records are changed.
func Verify(source []byte, resolve IdentityResolver) error {
	_, err := VerifyDetailed(source, resolve)
	return err
}

// verifyV1 preserves the legacy mdpp parse, byte ranges, and pair-array hash.
// It checks the signature block in source against the canonical payload.
// Returns nil if the signature is valid. Returns ErrUnsigned if there is no
// signature block. Other failures return descriptive errors.
func verifyV1(source []byte, resolve IdentityResolver) error {
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
	if resolve == nil {
		return fmt.Errorf("spore: identity resolver required")
	}
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

	// Verify frontmatter substance hasn't changed since signing.
	// This catches mutations like adding proposed_writes after signing.
	currentFmSubstanceHashHex := computeFmSubstanceHash(fm)

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
	payload := buildCanonicalPayload(agentID, sporeID, createdAt, bodyHashHex, currentFmSubstanceHashHex)

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
//	sha256hex-of-fm-substance\n
//
// The fm-substance hash covers all frontmatter fields except "status" and
// "signature". Excluding "status" lets review flows promote unreviewed→accepted
// without invalidating the signature. Excluding "signature" avoids a
// bootstrapping cycle. Any other frontmatter mutation (e.g. adding
// proposed_writes after signing) changes the substance hash and fails
// verification.
func buildCanonicalPayload(agentID, sporeID string, createdAt time.Time, bodyHashHex, fmSubstanceHashHex string) []byte {
	var sb strings.Builder
	sb.WriteString(agentID)
	sb.WriteByte('\n')
	sb.WriteString(sporeID)
	sb.WriteByte('\n')
	sb.WriteString(createdAt.UTC().Format(time.RFC3339))
	sb.WriteByte('\n')
	sb.WriteString(bodyHashHex)
	sb.WriteByte('\n')
	sb.WriteString(fmSubstanceHashHex)
	sb.WriteByte('\n')
	return []byte(sb.String())
}

// computeFmSubstanceHash extracts all frontmatter fields from fm except
// "status" and "signature", serialises them as canonical (sorted-key) JSON,
// and returns the SHA-256 hex digest.
//
// Canonicalization rules:
//   - Keys "status" and "signature" are always excluded.
//   - Remaining keys are sorted lexicographically.
//   - The value is serialised with encoding/json (YAML-parsed values such as
//     []any, map[string]any, time.Time are all JSON-serialisable). time.Time
//     values are rendered as RFC3339 UTC strings for legacy compatibility
//     across runs.
func computeFmSubstanceHash(fm map[string]any) string {
	skipped := map[string]bool{"status": true, "signature": true}

	keys := make([]string, 0, len(fm))
	for k := range fm {
		if !skipped[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	substance := make(map[string]any, len(keys))
	for _, k := range keys {
		substance[k] = normaliseForJSON(fm[k])
	}

	b, err := json.Marshal(orderedMap(keys, substance))
	if err != nil {
		// Extremely unlikely; fall back to empty hash marker.
		return "error"
	}
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h[:])
}

// orderedMap builds a json.Marshaler-compatible []any that preserves key order.
// v1 encoded the top-level mapping as an array of [key, value] pairs.
// Keep that historical encoding even though encoding/json sorts map keys.
func orderedMap(keys []string, m map[string]any) any {
	pairs := make([]any, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, []any{k, m[k]})
	}
	return pairs
}

// normaliseForJSON converts values that encoding/json cannot serialise
// portably (primarily time.Time) into their canonical string form.
func normaliseForJSON(v any) any {
	switch t := v.(type) {
	case time.Time:
		return t.UTC().Format(time.RFC3339)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = normaliseForJSON(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = normaliseForJSON(val)
		}
		return out
	default:
		return v
	}
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

// parseSignatureBlock converts the raw frontmatter value for "signature" into
// a Signature struct.
func parseSignatureBlock(raw any) (Signature, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return Signature{}, fmt.Errorf("signature block is not a mapping")
	}
	version := 1 // An omitted version identifies a legacy signature.
	if rawVersion, present := m["version"]; present {
		switch fmt.Sprint(rawVersion) {
		case "1":
			version = 1
		case "2":
			version = 2
		default:
			return Signature{}, fmt.Errorf("unsupported signature version %v", rawVersion)
		}
	}
	bodyHash, _ := m["body_hash"].(string)
	fmHash, _ := m["frontmatter_hash"].(string)
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
		Version:         version,
		BodyHash:        bodyHash,
		FrontmatterHash: fmHash,
		Alg:             alg,
		Key:             key,
		ContentHash:     contentHash,
		SignedAt:        signedAt,
		Value:           value,
	}, nil
}
