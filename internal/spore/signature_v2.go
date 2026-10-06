package spore

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"m31labs.dev/hyphae/internal/identity"
)

// VerificationReport describes the signed scope and the verification result.
// Hashes are recomputed from the document; Recorded contains signature hashes
// and metadata. Changed localizes hash differences, not their cause.
type VerificationReport struct {
	Status           string     `json:"status"`
	Signer           string     `json:"signer,omitempty"`
	Version          int        `json:"version"`
	CoveredFields    []string   `json:"covered_fields,omitempty"`
	ExcludedFields   []string   `json:"excluded_fields,omitempty"`
	BodyHash         string     `json:"body_hash,omitempty"`
	FrontmatterHash  string     `json:"frontmatter_hash,omitempty"`
	ContentHash      string     `json:"content_hash,omitempty"`
	Recorded         *Signature `json:"recorded,omitempty"`
	Changed          []string   `json:"changed,omitempty"`
	ProposalMismatch []string   `json:"proposal_mismatch,omitempty"`
	Note             string     `json:"note,omitempty"`
	Error            string     `json:"error,omitempty"`
}

// parseSigningSource uses YAML's semantic values, independent of mdpp's scalar
// extraction. Delimiters must occupy a whole line. Body bytes start after the
// closing delimiter's line ending and are never reserialized.
func parseSigningSource(source []byte) (map[string]any, []byte, error) {
	opening := bytes.IndexByte(source, '\n')
	if opening < 0 || string(bytes.TrimSuffix(source[:opening], []byte("\r"))) != "---" {
		return nil, nil, fmt.Errorf("spore: expected opening --- frontmatter delimiter")
	}
	start := opening + 1
	for offset := start; offset < len(source); {
		end := bytes.IndexByte(source[offset:], '\n')
		next := len(source)
		if end < 0 {
			end = len(source)
		} else {
			end += offset
			next = end + 1
		}
		if string(bytes.TrimSuffix(source[offset:end], []byte("\r"))) == "---" {
			var fm map[string]any
			if err := yaml.Unmarshal(source[start:offset], &fm); err != nil {
				return nil, nil, fmt.Errorf("spore: parse frontmatter: %w", err)
			}
			if fm == nil {
				return nil, nil, fmt.Errorf("spore: no frontmatter mapping")
			}
			return fm, source[next:], nil
		}
		offset = next
	}
	return nil, nil, fmt.Errorf("spore: missing closing --- frontmatter delimiter")
}

func hashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return fmt.Sprintf("sha256:%x", h)
}

// canonicalV2 encodes all frontmatter except the top-level status/signature as
// compact encoding/json JSON. String map keys are sorted recursively by Go's
// encoder, array order is retained, and timestamps become UTC RFC3339Nano.
// Unsupported values (non-string map keys, NaN, infinity) return errors.
// The payload is another compact sorted-key JSON object, domain-separated by
// version:2. Hashes in that object include the sha256: prefix. No YAML rendering
// or placeholder signature participates in either digest.
func canonicalV2(fm map[string]any, body []byte) ([]byte, string, string, error) {
	substance := make(map[string]any, len(fm))
	for key, value := range fm {
		if key == "status" || key == "signature" {
			continue
		}
		normalized, err := canonicalValue(value)
		if err != nil {
			return nil, "", "", fmt.Errorf("spore: canonical frontmatter %s: %w", key, err)
		}
		substance[key] = normalized
	}
	encoded, err := json.Marshal(substance)
	if err != nil {
		return nil, "", "", fmt.Errorf("spore: canonical frontmatter: %w", err)
	}
	bodyHash, fmHash := hashBytes(signableBody(body)), hashBytes(encoded)
	var created time.Time
	switch value := fm["created"].(type) {
	case time.Time:
		created = value
	case string:
		created, err = time.Parse(time.RFC3339Nano, value)
	default:
		err = fmt.Errorf("created field missing or invalid")
	}
	if err != nil {
		return nil, "", "", fmt.Errorf("spore: canonical created: %w", err)
	}
	agent, _ := fm["agent"].(map[string]any)
	payload, err := json.Marshal(map[string]any{
		"version": 2, "agent_id": stringField(agent, "id"), "spore_id": stringField(fm, "id"),
		"created": created.UTC().Format(time.RFC3339Nano), "body_hash": bodyHash, "frontmatter_hash": fmHash,
	})
	return payload, bodyHash, fmHash, err
}

func canonicalValue(value any) (any, error) {
	switch value := value.(type) {
	case time.Time:
		return value.UTC().Format(time.RFC3339Nano), nil
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, child := range value {
			normalized, err := canonicalValue(child)
			if err != nil {
				return nil, err
			}
			out[key] = normalized
		}
		return out, nil
	case map[any]any:
		return nil, fmt.Errorf("mapping keys must be strings")
	case []any:
		out := make([]any, len(value))
		for i, child := range value {
			normalized, err := canonicalValue(child)
			if err != nil {
				return nil, err
			}
			out[i] = normalized
		}
		return out, nil
	default:
		return value, nil
	}
}

// ContentHash returns the v2 canonical payload hash, regardless of whether the
// document is signed. Submit and amend receipts use this same content digest.
func ContentHash(source []byte) (string, error) {
	fm, body, err := parseSigningSource(source)
	if err != nil {
		return "", err
	}
	payload, _, _, err := canonicalV2(fm, body)
	if err != nil {
		return "", err
	}
	return hashBytes(payload), nil
}

func signV2(source []byte, priv identity.PrivateKey, signedKey string) ([]byte, error) {
	fm, body, err := parseSigningSource(source)
	if err != nil {
		return nil, err
	}
	payload, bodyHash, fmHash, err := canonicalV2(fm, body)
	if err != nil {
		return nil, err
	}
	fm["signature"] = Signature{
		Version: 2, Alg: "ed25519", Key: signedKey, BodyHash: bodyHash, FrontmatterHash: fmHash,
		ContentHash: hashBytes(payload), SignedAt: time.Now().UTC().Truncate(time.Second),
		Value: "ed25519:" + base64.StdEncoding.EncodeToString(identity.Sign(priv, payload)),
	}
	// YAML v3's default literal rendering drops newline-only strings in nested
	// mappings. Quote them before encoding, while retaining readable blocks for
	// proposal prose. Hashing still uses the original semantic values.
	encoded, err := yaml.Marshal(quoteBlankValues(fm))
	if err != nil {
		return nil, fmt.Errorf("spore: encode signed frontmatter: %w", err)
	}
	out := append([]byte("---\n"), encoded...)
	out = append(out, []byte("---\n")...)
	return append(out, body...), nil
}

func quoteBlankValues(value any) any {
	switch value := value.(type) {
	case string:
		if strings.TrimSpace(value) == "" {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Style: yaml.DoubleQuotedStyle, Value: value}
		}
		return value
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, child := range value {
			out[key] = quoteBlankValues(child)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, child := range value {
			out[i] = quoteBlankValues(child)
		}
		return out
	default:
		return value
	}
}

// VerifyDetailed provides the report used by the CLI, including legacy scope.
func VerifyDetailed(source []byte, resolve IdentityResolver) (report VerificationReport, err error) {
	report.Status = "INVALID"
	defer func() {
		if err != nil && err != ErrUnsigned && err != ErrLegacyUnverified {
			report.Error = err.Error()
		}
	}()
	fm, body, err := parseSigningSource(source)
	if err != nil {
		return report, err
	}
	if fm["signature"] == nil || isUnsignedMarker(fm["signature"]) {
		report.Status = "UNSIGNED"
		return report, ErrUnsigned
	}
	sig, err := parseSignatureBlock(fm["signature"])
	if err != nil {
		return report, err
	}
	report.Version, report.Recorded = sig.Version, &sig
	id, err := resolveSigner(resolve, sig.Key)
	report.Signer = id.ID
	if err != nil {
		return report, err
	}
	if sig.Alg != "ed25519" {
		return report, fmt.Errorf("spore: unsupported signature alg %q", sig.Alg)
	}
	if !strings.HasPrefix(sig.Value, "ed25519:") {
		return report, fmt.Errorf("spore: signature value missing ed25519: prefix")
	}
	sigBytes, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sig.Value, "ed25519:"))
	if err != nil {
		return report, fmt.Errorf("spore: decode signature value: %w", err)
	}
	report.CoveredFields = []string{"agent.id", "id", "created", "authored body", "frontmatter substance (including proposed_writes and proposed_edges)"}
	report.ExcludedFields = []string{"status", "signature block", "appended trace work log"}
	if sig.Version == 0 || sig.Version == 1 {
		return verifyLegacy(source, sig, id, sigBytes, report)
	}
	if sig.Version != 2 {
		return report, fmt.Errorf("spore: unsupported signature version %d", sig.Version)
	}
	payload, bodyHash, fmHash, err := canonicalV2(fm, body)
	if err != nil {
		return report, err
	}
	report.BodyHash, report.FrontmatterHash, report.ContentHash = bodyHash, fmHash, hashBytes(payload)
	if sig.BodyHash != bodyHash {
		report.Changed = append(report.Changed, "body")
	}
	if sig.FrontmatterHash != fmHash {
		report.Changed = append(report.Changed, "frontmatter")
	}
	if len(report.Changed) > 0 {
		return report, fmt.Errorf("spore: %s hash mismatch", strings.Join(report.Changed, " and "))
	}
	if sig.ContentHash != report.ContentHash {
		return report, fmt.Errorf("spore: content_hash does not match canonical payload")
	}
	if !identity.Verify(id, payload, sigBytes) {
		return report, fmt.Errorf("spore: signature verification failed (signed content or signature changed)")
	}
	report.Status = "VALID"
	return report, nil
}
