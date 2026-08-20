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
// before the closing `---` (or replaces an existing canonical block in place).
// signedKey is the public identity URI (e.g. "identity://m31labs/odvcencio").
//
// Canonicalization invariant: the substance hash is computed over the parsed
// frontmatter in the same structural context as the signed bytes. Signing
// preserves the source YAML while inserting only the signature mapping, so a
// leading newline in a block scalar (and all other scalar formatting) remains
// part of the signed document rather than being rewritten by a serializer.
func Sign(source []byte, priv identity.PrivateKey, signedKey string) ([]byte, error) {
	if err := validateSignInput(source); err != nil {
		return nil, fmt.Errorf("spore: sign: frontmatter: %w", err)
	}
	doc, err := mdpp.Parse(source)
	if err != nil {
		return nil, fmt.Errorf("spore: sign: parse: %w", err)
	}

	fm := doc.Frontmatter()
	if fm == nil {
		return nil, fmt.Errorf("spore: sign: no frontmatter block found")
	}

	// Extract required fields for the canonical payload using the original fm.
	// These scalar fields (id, agent.id, created) come from the source representation,
	// which signing preserves byte-for-byte.
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

	// Compute the frontmatter substance hash in the same structural context that
	// Verify will parse after the signature mapping is inserted.
	fmSubstanceHashHex, err := computeCanonicalFmSubstanceHash(source)
	if err != nil {
		return nil, fmt.Errorf("spore: sign: canonical frontmatter: %w", err)
	}

	// Build canonical payload.
	payload := buildCanonicalPayload(agentID, sporeID, createdAt, bodyHashHex, fmSubstanceHashHex)

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

	// Rewrite the source with the signature block inserted, then reparse and
	// validate the exact bytes before returning them to the caller.
	signed, err := injectSignature(source, sig)
	if err != nil {
		return nil, err
	}
	if err := validateSignedOutput(signed, sig, fmSubstanceHashHex); err != nil {
		return nil, fmt.Errorf("spore: sign: signed output validation: %w", err)
	}
	return signed, nil
}

// Verify checks the signature block in source against the canonical payload.
// Returns nil if the signature is valid. Returns ErrUnsigned if there is no
// signature block. Other failures return descriptive errors.
func Verify(source []byte, resolve IdentityResolver) error {
	bounds, rawFM, parsedFM, preflightErr := preflightFrontmatter(source)
	if preflightErr != nil && !errors.Is(preflightErr, errFrontmatterNotFound) {
		return fmt.Errorf("spore: verify: frontmatter: %w", preflightErr)
	}

	var sig Signature
	var hasSig bool
	if preflightErr == nil {
		entry, err := parsedFM.singleSignature()
		if err != nil {
			return fmt.Errorf("spore: verify: frontmatter: %w", err)
		}
		if entry != nil {
			var rangeErr error
			sig, _, _, rangeErr = canonicalSignatureRange(rawFM, bounds.lineEnding, parsedFM)
			if rangeErr != nil {
				return fmt.Errorf("spore: verify: frontmatter: %w", rangeErr)
			}
			hasSig = true
		}
	}
	doc, err := mdpp.Parse(source)
	if err != nil {
		return fmt.Errorf("spore: verify: parse: %w", err)
	}

	fm := doc.Frontmatter()
	if fm == nil {
		return fmt.Errorf("spore: verify: no frontmatter block found")
	}
	if !hasSig {
		return ErrUnsigned
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

	// Verify frontmatter substance hasn't changed since signing.
	// This catches mutations like adding proposed_writes after signing.
	currentFmSubstanceHashHex, err := computeFmSubstanceHash(fm)
	if err != nil {
		return fmt.Errorf("spore: verify: canonical frontmatter: %w", err)
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

// computeCanonicalFmSubstanceHash computes the frontmatter substance hash in the
// same structural context that Verify sees after signing. It returns an error
// for every bounds, YAML, canonical-signature, parse, or hashing failure; no
// literal hash sentinel is ever used.
func computeCanonicalFmSubstanceHash(source []byte) (string, error) {
	bounds, ok := frontmatterBounds(source)
	if !ok {
		return "", fmt.Errorf("frontmatter delimiters not found")
	}
	rawFM := source[bounds.openEnd:bounds.closeStart]
	cleanFM, insertionOffset, err := cleanFrontmatter(rawFM, bounds.lineEnding)
	if err != nil {
		return "", fmt.Errorf("clean frontmatter: %w", err)
	}

	// mdpp.Parse's block-scalar handling depends on content after the scalar.
	// Parse a synthetic document with a canonical placeholder signature so Sign
	// hashes the same structural representation that Verify will parse.
	placeholder := Signature{
		Alg:         "ed25519",
		Key:         "placeholder",
		ContentHash: "sha256:" + strings.Repeat("0", 64),
		SignedAt:    time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC),
		Value:       "ed25519:" + strings.Repeat("A", 88),
	}
	placeholderYAML, err := canonicalSignatureYAML(placeholder, bounds.lineEnding)
	if err != nil {
		return "", fmt.Errorf("render placeholder signature: %w", err)
	}
	if insertionOffset < 0 || insertionOffset > len(cleanFM) {
		return "", fmt.Errorf("clean frontmatter insertion offset %d is outside %d bytes", insertionOffset, len(cleanFM))
	}
	delimiter := []byte("---" + bounds.lineEnding)
	synth := append([]byte(nil), delimiter...)
	synth = append(synth, cleanFM[:insertionOffset]...)
	synth = append(synth, placeholderYAML...)
	synth = append(synth, cleanFM[insertionOffset:]...)
	synth = append(synth, delimiter...)

	synthDoc, err := mdpp.Parse(synth)
	if err != nil || synthDoc == nil {
		if err == nil {
			err = fmt.Errorf("parser returned nil document")
		}
		return "", fmt.Errorf("parse synthetic signed frontmatter: %w", err)
	}
	canonFM := synthDoc.Frontmatter()
	if canonFM == nil {
		return "", fmt.Errorf("synthetic signed document has no frontmatter")
	}
	return computeFmSubstanceHash(canonFM)
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
//     values are rendered as RFC3339 UTC strings to keep the digest stable
//     across runs.
func computeFmSubstanceHash(fm map[string]any) (string, error) {
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
		return "", fmt.Errorf("marshal canonical frontmatter substance: %w", err)
	}
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h[:]), nil
}

// orderedMap builds a json.Marshaler-compatible []any that preserves key order.
// encoding/json marshals map[string]any in random order; by encoding as an
// array of [key, value] pairs we get a deterministic digest.
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

// injectSignature rewrites source to include sig as the `signature:` block
// immediately before an optional root-column `...` marker or the closing `---`
// for unsigned input. A signed source replaces exactly the canonical signature
// byte range in place.
func injectSignature(source []byte, sig Signature) ([]byte, error) {
	bounds, ok := frontmatterBounds(source)
	if !ok {
		return nil, fmt.Errorf("spore: sign: source does not start with frontmatter delimiter")
	}
	rawFM := source[bounds.openEnd:bounds.closeStart]
	parsed, err := parseFrontmatterYAML(rawFM)
	if err != nil {
		return nil, fmt.Errorf("spore: sign: existing signature: %w", nonCanonicalSignatureError(err.Error()))
	}
	entry, err := parsed.singleSignature()
	if err != nil {
		return nil, fmt.Errorf("spore: sign: existing signature: %w", err)
	}
	sigYAML, err := canonicalSignatureYAML(sig, bounds.lineEnding)
	if err != nil {
		return nil, fmt.Errorf("spore: sign: render signature yaml: %w", err)
	}

	if entry == nil {
		insertAt := bounds.closeStart
		if bounds.documentEndStart >= 0 {
			insertAt = bounds.documentEndStart
		}
		out := make([]byte, 0, len(source)+len(sigYAML))
		out = append(out, source[:insertAt]...)
		out = append(out, sigYAML...)
		out = append(out, source[insertAt:]...)
		return out, nil
	}
	_, start, end, err := canonicalSignatureRange(rawFM, bounds.lineEnding, parsed)
	if err != nil {
		return nil, fmt.Errorf("spore: sign: existing signature: %w", err)
	}
	absoluteStart := bounds.openEnd + start
	absoluteEnd := bounds.openEnd + end
	out := make([]byte, 0, len(source)-absoluteEnd+absoluteStart+len(sigYAML))
	out = append(out, source[:absoluteStart]...)
	out = append(out, sigYAML...)
	out = append(out, source[absoluteEnd:]...)
	return out, nil
}

// validateSignedOutput reparses the exact bytes returned by Sign. It verifies
// the canonical semantic signature and the non-signature substance hash before
// Sign can return nil error.
func validateSignedOutput(source []byte, expected Signature, expectedFmHash string) error {
	doc, err := mdpp.Parse(source)
	if err != nil {
		return fmt.Errorf("parse signed output: %w", err)
	}
	fm := doc.Frontmatter()
	if fm == nil {
		return fmt.Errorf("signed output has no frontmatter")
	}
	bounds, ok := frontmatterBounds(source)
	if !ok {
		return fmt.Errorf("signed output has invalid frontmatter delimiters")
	}
	rawFM := source[bounds.openEnd:bounds.closeStart]
	parsed, err := parseFrontmatterYAML(rawFM)
	if err != nil {
		return fmt.Errorf("parse signed output frontmatter: %w", err)
	}
	sig, _, _, err := canonicalSignatureRange(rawFM, bounds.lineEnding, parsed)
	if err != nil {
		return fmt.Errorf("signed output signature: %w", err)
	}
	if !sameSignatureFields(sig, expected) {
		return fmt.Errorf("signed output signature fields differ from payload")
	}
	actualFmHash, err := computeFmSubstanceHash(fm)
	if err != nil {
		return fmt.Errorf("hash signed output frontmatter: %w", err)
	}
	if actualFmHash != expectedFmHash {
		return fmt.Errorf("signed output frontmatter hash differs from payload")
	}
	return nil
}

func sameSignatureFields(a, b Signature) bool {
	return a.Alg == b.Alg &&
		a.Key == b.Key &&
		a.ContentHash == b.ContentHash &&
		a.Value == b.Value &&
		a.SignedAt.UTC().Equal(b.SignedAt.UTC())
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
