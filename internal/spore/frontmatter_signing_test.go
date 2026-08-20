package spore

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"m31labs.dev/hyphae/internal/identity"
)

func adversarialSporeSource(writes string) []byte {
	return []byte(fmt.Sprintf(`---
mdpp: "0.1"
id: spore.2026-08-20.signing-adversarial
type: spore
space: hypha://m31labs/research
status: unreviewed
created: 2026-08-20T10:00:00Z
agent:
  id: agent://test/signing
  kind: ephemeral
confidence: high
source_refs:
  - hypha://m31labs/research/concepts/signing
proposed_writes:
%s
proposed_edges:
  - kind: supports
    src: spore.2026-08-20.signing-adversarial
    dst: hypha://m31labs/research/concepts/signing
---

# Signing adversarial body
`, writes))
}

func signSubmitVerify(t *testing.T, source []byte, useCRLF bool) []byte {
	t.Helper()
	id, priv, err := identity.Generate("m31labs", "signing-test", "hypha://m31labs/research")
	if err != nil {
		t.Fatalf("identity.Generate: %v", err)
	}
	resolver := func(uri string) (identity.Identity, error) {
		if uri == id.ID {
			return id, nil
		}
		return identity.Identity{}, fmt.Errorf("unknown identity %q", uri)
	}
	if useCRLF {
		source = bytes.ReplaceAll(source, []byte("\n"), []byte("\r\n"))
	}
	signed, err := Sign(source, priv, id.ID)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	spaceRoot := t.TempDir()
	filePath, _, err := SubmitBytes(signed, spaceRoot)
	if err != nil {
		t.Fatalf("SubmitBytes: %v", err)
	}
	onDisk, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read submitted spore: %v", err)
	}
	if err := Verify(onDisk, resolver); err != nil {
		t.Fatalf("Verify after SubmitBytes: %v", err)
	}
	return onDisk
}

func TestSignSubmitVerifyAdversarialFrontmatterShapes(t *testing.T) {
	tests := []struct {
		name   string
		writes string
		crlf   bool
	}{
		{
			name: "leading blank block scalar",
			writes: `  - kind: append_section
    target: hypha://m31labs/research/concepts/target
    heading: "Leading blank"
    body: |

      first content byte follows a blank line
`,
		},
		{
			name: "ordinary scalar",
			writes: `  - kind: append_section
    target: hypha://m31labs/research/concepts/target
    heading: "Ordinary"
    body: "ordinary scalar body"
`,
		},
		{
			name: "multiple writes",
			writes: `  - kind: append_section
    target: hypha://m31labs/research/concepts/target
    heading: "First"
    body: |

      first write
  - kind: append_section
    target: hypha://m31labs/research/concepts/target
    heading: "Second"
    body: "second write"
`,
		},
		{
			name: "crlf",
			writes: `  - kind: append_section
    target: hypha://m31labs/research/concepts/target
    heading: "CRLF"
    body: |

      CRLF body
`,
			crlf: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			onDisk := signSubmitVerify(t, adversarialSporeSource(tt.writes), tt.crlf)
			if tt.crlf && !bytes.Contains(onDisk, []byte("\r\n")) {
				t.Fatal("CRLF source was not preserved in signed bytes")
			}
		})
	}
}

func TestSignReplacesExistingSignatureWithoutRewritingYAML(t *testing.T) {
	id, priv, err := identity.Generate("m31labs", "replacement-test", "hypha://m31labs/research")
	if err != nil {
		t.Fatalf("identity.Generate: %v", err)
	}
	source := adversarialSporeSource(`  - kind: append_section
    target: hypha://m31labs/research/concepts/target
    heading: "Replacement"
    body: |

      preserve this block scalar exactly
`)
	first, err := Sign(source, priv, id.ID)
	if err != nil {
		t.Fatalf("first Sign: %v", err)
	}
	second, err := Sign(first, priv, id.ID)
	if err != nil {
		t.Fatalf("replacement Sign: %v", err)
	}
	if got := bytes.Count(second, []byte("\nsignature:\n")); got != 1 {
		t.Fatalf("signature block count = %d, want 1", got)
	}
	if strings.Contains(string(second), "|4") {
		t.Fatal("signature replacement rewrote block scalar to unsafe |4 form")
	}
	if !bytes.Contains(second, []byte("body: |\n\n      preserve this block scalar exactly")) {
		t.Fatal("signature replacement did not preserve the leading-blank block scalar bytes")
	}
	resolver := func(uri string) (identity.Identity, error) {
		if uri == id.ID {
			return id, nil
		}
		return identity.Identity{}, fmt.Errorf("unknown identity %q", uri)
	}
	spaceRoot := t.TempDir()
	path, _, err := SubmitBytes(second, spaceRoot)
	if err != nil {
		t.Fatalf("SubmitBytes replacement: %v", err)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read replacement: %v", err)
	}
	if err := Verify(onDisk, resolver); err != nil {
		t.Fatalf("Verify replacement: %v", err)
	}
}

func TestSignReplacesExistingCRLFSignatureInPlace(t *testing.T) {
	id, priv, err := identity.Generate("m31labs", "replacement-crlf-test", "hypha://m31labs/research")
	if err != nil {
		t.Fatalf("identity.Generate: %v", err)
	}
	source := bytes.ReplaceAll(adversarialSporeSource(`  - kind: append_section
    target: hypha://m31labs/research/concepts/target
    heading: "CRLF replacement"
    body: |

      preserve CRLF bytes
`), []byte("\n"), []byte("\r\n"))
	first, err := Sign(source, priv, id.ID)
	if err != nil {
		t.Fatalf("first CRLF Sign: %v", err)
	}
	second, err := Sign(first, priv, id.ID)
	if err != nil {
		t.Fatalf("second CRLF Sign: %v", err)
	}
	if got := bytes.Count(second, []byte("\r\nsignature:\r\n")); got != 1 {
		t.Fatalf("CRLF signature block count = %d, want 1", got)
	}
	if bytes.Contains(second, []byte("signature:\n")) {
		t.Fatal("CRLF replacement introduced LF-only signature bytes")
	}
	resolver := func(uri string) (identity.Identity, error) {
		if uri == id.ID {
			return id, nil
		}
		return identity.Identity{}, fmt.Errorf("unknown identity %q", uri)
	}
	if err := Verify(second, resolver); err != nil {
		t.Fatalf("Verify CRLF replacement: %v", err)
	}
}

func signingFixture(t *testing.T) (source []byte, id identity.Identity, priv identity.PrivateKey) {
	t.Helper()
	id, priv, err := identity.Generate("m31labs", "variant-test", "hypha://m31labs/research")
	if err != nil {
		t.Fatalf("identity.Generate: %v", err)
	}
	source, err = Sign(adversarialSporeSource(`  - kind: append_section
    target: hypha://m31labs/research/concepts/target
    heading: "Variant"
    body: |

      preserve this leading blank
`), priv, id.ID)
	if err != nil {
		t.Fatalf("initial Sign: %v", err)
	}
	return source, id, priv
}

func signatureSpan(t *testing.T, source []byte) (start, end int, block []byte) {
	t.Helper()
	start = bytes.Index(source, []byte("signature:\n"))
	if start < 0 {
		t.Fatal("signed source has no canonical signature block")
	}
	relClose := bytes.Index(source[start:], []byte("\n---"))
	if relClose < 0 {
		t.Fatal("signed source has no closing delimiter")
	}
	end = start + relClose + 1
	return start, end, source[start:end]
}

func replaceSignatureSpan(source, replacement []byte) []byte {
	start, end, _ := signatureSpanForReplace(source)
	out := make([]byte, 0, len(source)-end+start+len(replacement))
	out = append(out, source[:start]...)
	out = append(out, replacement...)
	out = append(out, source[end:]...)
	return out
}

func signatureSpanForReplace(source []byte) (start, end int, block []byte) {
	start = bytes.Index(source, []byte("signature:\n"))
	if start < 0 {
		return -1, -1, nil
	}
	relClose := bytes.Index(source[start:], []byte("\n---"))
	if relClose < 0 {
		return -1, -1, nil
	}
	end = start + relClose + 1
	return start, end, source[start:end]
}

func mutateSignatureKey(block []byte, key string) []byte {
	suffix := block[len("signature:"):]
	return append([]byte(key+":"), suffix...)
}

func signatureFlowBlock(block []byte, multiline bool) []byte {
	lines := strings.Split(strings.TrimSuffix(string(block), "\n"), "\n")
	fields := make([]string, 0, len(lines)-1)
	for _, line := range lines[1:] {
		fields = append(fields, strings.TrimSpace(line))
	}
	if multiline {
		return []byte("signature: {\n  " + strings.Join(fields, "\n  ") + "\n}\n")
	}
	return []byte("signature: {" + strings.Join(fields, ", ") + "}\n")
}

func reorderSignatureFields(block []byte) []byte {
	lines := strings.Split(strings.TrimSuffix(string(block), "\n"), "\n")
	lines[1], lines[2] = lines[2], lines[1]
	return []byte(strings.Join(lines, "\n") + "\n")
}

func removeCanonicalSignatureForTest(t *testing.T, source []byte) []byte {
	t.Helper()
	bounds, ok := frontmatterBounds(source)
	if !ok {
		t.Fatal("source has no frontmatter bounds")
	}
	raw := source[bounds.openEnd:bounds.closeStart]
	parsed, err := parseFrontmatterYAML(raw)
	if err != nil {
		t.Fatalf("parse source frontmatter: %v", err)
	}
	_, start, end, err := canonicalSignatureRange(raw, bounds.lineEnding, parsed)
	if err != nil {
		t.Fatalf("locate canonical signature: %v", err)
	}
	out := make([]byte, 0, len(source)-(end-start))
	out = append(out, source[:bounds.openEnd+start]...)
	out = append(out, source[bounds.openEnd+end:]...)
	return out
}

func TestSignRejectsNonCanonicalExistingSignatures(t *testing.T) {
	signed, id, priv := signingFixture(t)
	_, _, canonical := signatureSpan(t, signed)
	variants := []struct {
		name string
		make func([]byte) []byte
	}{
		{name: "double quoted key", make: func(b []byte) []byte { return mutateSignatureKey(b, `"signature"`) }},
		{name: "single quoted key", make: func(b []byte) []byte { return mutateSignatureKey(b, `'signature'`) }},
		{name: "tagged key", make: func(b []byte) []byte { return mutateSignatureKey(b, "!!str signature") }},
		{name: "one-line flow", make: func(b []byte) []byte { return signatureFlowBlock(b, false) }},
		{name: "multiline flow", make: func(b []byte) []byte { return signatureFlowBlock(b, true) }},
		{name: "empty value", make: func([]byte) []byte { return []byte("signature:\n") }},
		{name: "null value", make: func([]byte) []byte { return []byte("signature: null\n") }},
		{name: "scalar value", make: func([]byte) []byte { return []byte("signature: scalar\n") }},
		{name: "reordered fields", make: reorderSignatureFields},
		{name: "missing field", make: func(b []byte) []byte {
			return []byte(strings.Replace(string(b), "  value: ", "  omitted_value: ", 1))
		}},
		{name: "extra field", make: func(b []byte) []byte {
			return []byte(strings.TrimSuffix(string(b), "\n") + "\n  extra: nope\n")
		}},
		{name: "duplicate field", make: func(b []byte) []byte {
			lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
			return []byte(strings.Join(append(lines, lines[1]), "\n") + "\n")
		}},
		{name: "indented comment", make: func(b []byte) []byte {
			return []byte(strings.Replace(string(b), "  key: ", "  # comment inside\n  key: ", 1))
		}},
		{name: "trailing indented comment", make: func(b []byte) []byte {
			return []byte(strings.TrimSuffix(string(b), "\n") + "\n  # comment inside\n")
		}},
		{name: "root comment between fields", make: func(b []byte) []byte {
			return []byte(strings.Replace(string(b), "  key: ", "# comment inside\n  key: ", 1))
		}},
		{name: "explicit key", make: func(b []byte) []byte {
			flow := string(signatureFlowBlock(b, false))
			flow = strings.TrimSuffix(strings.TrimPrefix(flow, "signature: "), "\n")
			return []byte("? signature\n: " + flow + "\n")
		}},
	}
	for _, tt := range variants {
		t.Run(tt.name, func(t *testing.T) {
			mutated := replaceSignatureSpan(signed, tt.make(canonical))
			if _, err := Sign(mutated, priv, id.ID); err == nil {
				t.Fatal("Sign accepted a noncanonical existing signature")
			} else if !errors.Is(err, ErrNonCanonicalSignature) {
				t.Fatalf("Sign error = %v, want ErrNonCanonicalSignature", err)
			}
		})
	}
}

func TestSignRejectsDuplicateSemanticSignaturesAndTabs(t *testing.T) {
	signed, id, priv := signingFixture(t)
	_, _, canonical := signatureSpan(t, signed)
	duplicate := replaceSignatureSpan(signed, append(append([]byte(nil), canonical...), canonical...))
	if _, err := Sign(duplicate, priv, id.ID); err == nil {
		t.Fatal("Sign accepted duplicate semantic root signatures")
	} else if !errors.Is(err, ErrNonCanonicalSignature) {
		t.Fatalf("duplicate signature error = %v, want ErrNonCanonicalSignature", err)
	}
	tabbed := replaceSignatureSpan(signed, bytes.Replace(canonical, []byte("  alg:"), []byte("\talg:"), 1))
	if _, err := Sign(tabbed, priv, id.ID); err == nil {
		t.Fatal("Sign accepted tab-indented signature YAML")
	} else if !errors.Is(err, ErrNonCanonicalSignature) {
		t.Fatalf("tabbed signature error = %v, want ErrNonCanonicalSignature", err)
	}
}

func TestSignPreservesAllNonSignatureBytesAndTrivia(t *testing.T) {
	signed, id, priv := signingFixture(t)
	_, _, canonical := signatureSpan(t, signed)
	replacement := append([]byte("# before signature\n\n"), canonical...)
	replacement = append(replacement, []byte("# after signature\n\npost_signature: preserved\n")...)
	withTrivia := replaceSignatureSpan(signed, replacement)
	oldStart, oldEnd, _ := signatureSpan(t, withTrivia)
	resigned, err := Sign(withTrivia, priv, id.ID)
	if err != nil {
		t.Fatalf("Sign with surrounding trivia: %v", err)
	}
	if !bytes.Contains(resigned, []byte("# before signature\n\n")) || !bytes.Contains(resigned, []byte("# after signature\n\n")) {
		t.Fatal("signature replacement did not preserve surrounding comments and blank lines")
	}
	newStart, newEnd, _ := signatureSpan(t, resigned)
	if oldStart != newStart {
		t.Fatalf("signature start moved from %d to %d", oldStart, newStart)
	}
	if !bytes.Equal(withTrivia[:oldStart], resigned[:newStart]) {
		t.Fatal("bytes before signature changed during replacement")
	}
	if !bytes.Equal(withTrivia[oldEnd:], resigned[newEnd:]) {
		t.Fatal("bytes after signature changed during replacement")
	}
	if got, want := removeCanonicalSignatureForTest(t, resigned), removeCanonicalSignatureForTest(t, withTrivia); !bytes.Equal(got, want) {
		t.Fatalf("non-signature bytes changed during replacement")
	}
}

func TestSignLiteralSignatureLikeBodyAndClosingDelimiterEOF(t *testing.T) {
	source := adversarialSporeSource(`  - kind: append_section
    target: hypha://m31labs/research/concepts/target
    heading: "Literal"
    body: |
      signature:
        alg: ed25519
        key: not-a-root-signature
`)
	id, priv, err := identity.Generate("m31labs", "literal-test", "hypha://m31labs/research")
	if err != nil {
		t.Fatalf("identity.Generate: %v", err)
	}
	signed, err := Sign(source, priv, id.ID)
	if err != nil {
		t.Fatalf("Sign literal body: %v", err)
	}
	if _, _, err := SubmitBytes(signed, t.TempDir()); err != nil {
		t.Fatalf("SubmitBytes literal body: %v", err)
	}
	closeStart := bytes.Index(source, []byte("\n---\n"))
	if closeStart < 0 {
		t.Fatal("source closing delimiter not found")
	}
	eofSource := append([]byte(nil), source[:closeStart+4]...)
	eofSigned, err := Sign(eofSource, priv, id.ID)
	if err != nil {
		t.Fatalf("Sign closing delimiter at EOF: %v", err)
	}
	if !bytes.HasSuffix(eofSigned, []byte("---")) {
		t.Fatal("signed EOF source lost closing delimiter")
	}
	crlfSource := bytes.ReplaceAll(source, []byte("\n"), []byte("\r\n"))
	crlfCloseStart := bytes.Index(crlfSource, []byte("\r\n---\r\n"))
	if crlfCloseStart < 0 {
		t.Fatal("CRLF source closing delimiter not found")
	}
	crlfEOFSource := append([]byte(nil), crlfSource[:crlfCloseStart+len("\r\n---")]...)
	crlfEOFSigned, err := Sign(crlfEOFSource, priv, id.ID)
	if err != nil {
		t.Fatalf("Sign CRLF closing delimiter at EOF: %v", err)
	}
	if !bytes.HasSuffix(crlfEOFSigned, []byte("---")) {
		t.Fatal("signed CRLF EOF source lost closing delimiter")
	}
}

func TestCanonicalizationErrorsAreReturned(t *testing.T) {
	if _, err := computeCanonicalFmSubstanceHash([]byte("not frontmatter")); err == nil {
		t.Fatal("computeCanonicalFmSubstanceHash accepted missing delimiters")
	}
	if _, err := computeFmSubstanceHash(map[string]any{"bad": func() {}}); err == nil {
		t.Fatal("computeFmSubstanceHash swallowed JSON marshal failure")
	}
}

func generatedSigningIdentity(t *testing.T, name string) (identity.Identity, identity.PrivateKey) {
	t.Helper()
	id, priv, err := identity.Generate("m31labs", name, "hypha://m31labs/research")
	if err != nil {
		t.Fatalf("identity.Generate: %v", err)
	}
	return id, priv
}

func identityResolverFor(id identity.Identity) IdentityResolver {
	return func(uri string) (identity.Identity, error) {
		if uri == id.ID {
			return id, nil
		}
		return identity.Identity{}, fmt.Errorf("unknown identity %q", uri)
	}
}

func explicitMarkerSource(crlf bool) []byte {
	source := adversarialSporeSource(`  - kind: append_section
    target: hypha://m31labs/research/concepts/target
    heading: "Explicit marker ✓"
    body: |

      preserve the leading blank and UTF-8 ✓
`)
	source = bytes.Replace(source, []byte("\n---\n"), []byte("\n... # end frontmatter\n# bytes after marker stay put\n---\n"), 1)
	if crlf {
		source = bytes.ReplaceAll(source, []byte("\n"), []byte("\r\n"))
	}
	return source
}

func TestSignPreservesUnsignedSourceWithExplicitDocumentEnd(t *testing.T) {
	for _, crlf := range []bool{false, true} {
		t.Run(map[bool]string{false: "lf", true: "crlf"}[crlf], func(t *testing.T) {
			source := explicitMarkerSource(crlf)
			id, priv := generatedSigningIdentity(t, "explicit-marker")
			signed, err := Sign(source, priv, id.ID)
			if err != nil {
				t.Fatalf("Sign: %v", err)
			}
			if got := removeCanonicalSignatureForTest(t, signed); !bytes.Equal(got, source) {
				t.Fatalf("removing emitted signature did not restore exact source\n got: %q\nwant: %q", got, source)
			}
			eol := []byte("\n")
			if crlf {
				eol = []byte("\r\n")
			}
			marker := bytes.Index(signed, append([]byte("... # end frontmatter"), eol...))
			signature := bytes.Index(signed, append([]byte("signature:"), eol...))
			if marker < 0 || signature < 0 || signature >= marker {
				t.Fatalf("signature was not inserted immediately before explicit marker: marker=%d signature=%d", marker, signature)
			}
			if err := Verify(signed, identityResolverFor(id)); err != nil {
				t.Fatalf("Verify: %v", err)
			}
		})
	}
}

func insertBeforeApplicationClose(source, insertion []byte) []byte {
	bounds, ok := frontmatterBounds(source)
	if !ok {
		return nil
	}
	out := make([]byte, 0, len(source)+len(insertion))
	out = append(out, source[:bounds.closeStart]...)
	out = append(out, insertion...)
	out = append(out, source[bounds.closeStart:]...)
	return out
}

func insertBeforeStatus(source []byte, insertion string) []byte {
	at := bytes.Index(source, []byte("status: unreviewed\n"))
	if at < 0 {
		return nil
	}
	out := make([]byte, 0, len(source)+len(insertion))
	out = append(out, source[:at]...)
	out = append(out, insertion...)
	out = append(out, source[at:]...)
	return out
}

func TestFrontmatterStreamPreflightRejectsTrailingDocumentsOnSignAndVerify(t *testing.T) {
	id, priv := generatedSigningIdentity(t, "stream-preflight")
	base := adversarialSporeSource(`  - kind: append_section
    target: hypha://m31labs/research/concepts/target
    heading: "stream preflight"
    body: "body"
`)
	validSigned, err := Sign(base, priv, id.ID)
	if err != nil {
		t.Fatalf("Sign base: %v", err)
	}
	resolver := identityResolverFor(id)
	cases := []struct {
		name string
		tail []byte
	}{
		{name: "commented second separator", tail: []byte("--- # second document\ntrailing: true\n")},
		{name: "spaced second separator", tail: []byte("---   \ntrailing: true\n")},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			unsigned := insertBeforeApplicationClose(base, tt.tail)
			if _, err := Sign(unsigned, priv, id.ID); err == nil {
				t.Fatal("Sign accepted a trailing YAML document")
			} else if !errors.Is(err, ErrNonCanonicalSignature) {
				t.Fatalf("Sign error = %v, want ErrNonCanonicalSignature", err)
			}
			mutated := insertBeforeApplicationClose(validSigned, tt.tail)
			if err := Verify(mutated, resolver); err == nil {
				t.Fatal("Verify accepted a trailing YAML document")
			} else if !errors.Is(err, ErrNonCanonicalSignature) {
				t.Fatalf("Verify error = %v, want ErrNonCanonicalSignature", err)
			}
		})
	}

	explicit := explicitMarkerSource(false)
	afterMarker := insertBeforeApplicationClose(explicit, []byte("trailing_after_marker: true\n"))
	if _, err := Sign(afterMarker, priv, id.ID); err == nil {
		t.Fatal("Sign accepted semantic YAML after explicit document end")
	} else if !errors.Is(err, ErrNonCanonicalSignature) {
		t.Fatalf("after-marker Sign error = %v, want ErrNonCanonicalSignature", err)
	}
	explicitSigned, err := Sign(explicit, priv, id.ID)
	if err != nil {
		t.Fatalf("Sign explicit base: %v", err)
	}
	if err := Verify(insertBeforeApplicationClose(explicitSigned, []byte("trailing_after_marker: true\n")), resolver); err == nil {
		t.Fatal("Verify accepted semantic YAML after explicit document end")
	} else if !errors.Is(err, ErrNonCanonicalSignature) {
		t.Fatalf("after-marker Verify error = %v, want ErrNonCanonicalSignature", err)
	}

	multipleMarkers := bytes.Replace(explicit, []byte("... # end frontmatter\n"), []byte("... # first\n... # second\n"), 1)
	if _, err := Sign(multipleMarkers, priv, id.ID); err == nil {
		t.Fatal("Sign accepted multiple explicit document-end markers")
	} else if !errors.Is(err, ErrNonCanonicalSignature) {
		t.Fatalf("multiple-marker Sign error = %v, want ErrNonCanonicalSignature", err)
	}
}

func TestFrontmatterRootKeySubsetRejectsSignAndVerify(t *testing.T) {
	id, priv := generatedSigningIdentity(t, "root-key-subset")
	base := adversarialSporeSource(`  - kind: append_section
    target: hypha://m31labs/research/concepts/target
    heading: "root key subset"
    body: "body"
`)
	validSigned, err := Sign(base, priv, id.ID)
	if err != nil {
		t.Fatalf("Sign base: %v", err)
	}
	resolver := identityResolverFor(id)
	variants := []struct {
		name      string
		insertion string
	}{
		{name: "alias root key", insertion: "root_anchor: &root_alias value\n? *root_alias\n: value\n"},
		{name: "alias signature key", insertion: "signature_anchor: &signature_name signature\n? *signature_name\n: {}\n"},
		{name: "merge mapping", insertion: "merge_anchor: &merge_base\n  merged: value\n<<: *merge_base\n"},
		{name: "merge sequence", insertion: "merge_one: &merge_one\n  merged: value\nmerge_two: &merge_two\n  another: value\n<<: [*merge_one, *merge_two]\n"},
		{name: "custom root key", insertion: "!custom custom_key: value\n"},
		{name: "complex root key", insertion: "? [complex, key]\n: value\n"},
		{name: "alias cycle", insertion: "cycle: &cycle [*cycle]\n"},
		{name: "direct signature plus merge", insertion: "merge_signature: &merge_signature\n  extra: value\n<<: *merge_signature\n"},
	}
	for _, tt := range variants {
		t.Run(tt.name, func(t *testing.T) {
			unsigned := insertBeforeStatus(base, tt.insertion)
			if _, err := Sign(unsigned, priv, id.ID); err == nil {
				t.Fatal("Sign accepted unsupported root-key YAML")
			} else if !errors.Is(err, ErrNonCanonicalSignature) {
				t.Fatalf("Sign error = %v, want ErrNonCanonicalSignature", err)
			}
			mutated := insertBeforeStatus(validSigned, tt.insertion)
			if err := Verify(mutated, resolver); err == nil {
				t.Fatal("Verify accepted unsupported root-key YAML")
			} else if !errors.Is(err, ErrNonCanonicalSignature) {
				t.Fatalf("Verify error = %v, want ErrNonCanonicalSignature", err)
			}
		})
	}
}

func TestOrdinaryValueAliasesRoundTripBytePreserved(t *testing.T) {
	for _, crlf := range []bool{false, true} {
		t.Run(map[bool]string{false: "lf", true: "crlf"}[crlf], func(t *testing.T) {
			source := insertBeforeStatus(adversarialSporeSource(`  - kind: append_section
    target: hypha://m31labs/research/concepts/target
    heading: "ordinary aliases ✓"
    body: "body"
`), "ordinary_anchor: &ordinary_value \"UTF-8 ✓\"\nordinary_alias: *ordinary_value\n")
			if crlf {
				source = bytes.ReplaceAll(source, []byte("\n"), []byte("\r\n"))
			}
			id, priv := generatedSigningIdentity(t, "ordinary-alias")
			signed, err := Sign(source, priv, id.ID)
			if err != nil {
				t.Fatalf("Sign: %v", err)
			}
			if got := removeCanonicalSignatureForTest(t, signed); !bytes.Equal(got, source) {
				t.Fatal("ordinary alias source was not byte-preserved")
			}
			if err := Verify(signed, identityResolverFor(id)); err != nil {
				t.Fatalf("Verify: %v", err)
			}
		})
	}
}

func TestExplicitDocumentEndPreservesApplicationCloseAtEOF(t *testing.T) {
	for _, crlf := range []bool{false, true} {
		t.Run(map[bool]string{false: "lf", true: "crlf"}[crlf], func(t *testing.T) {
			source := explicitMarkerSource(crlf)
			bounds, ok := frontmatterBounds(source)
			if !ok {
				t.Fatal("source has no frontmatter bounds")
			}
			source = append([]byte(nil), source[:bounds.closeStart+len("---")]...)
			id, priv := generatedSigningIdentity(t, "explicit-marker-eof")
			signed, err := Sign(source, priv, id.ID)
			if err != nil {
				t.Fatalf("Sign: %v", err)
			}
			if !bytes.HasSuffix(signed, []byte("---")) {
				t.Fatal("Sign did not preserve application close at EOF")
			}
			if got := removeCanonicalSignatureForTest(t, signed); !bytes.Equal(got, source) {
				t.Fatal("removing signature did not restore explicit-marker EOF source")
			}
			if err := Verify(signed, identityResolverFor(id)); err != nil {
				t.Fatalf("Verify: %v", err)
			}
		})
	}
}

func TestCanonicalExplicitSignatureReplacementStaysInPlace(t *testing.T) {
	source := explicitMarkerSource(false)
	id, priv := generatedSigningIdentity(t, "explicit-marker-replace")
	first, err := Sign(source, priv, id.ID)
	if err != nil {
		t.Fatalf("first Sign: %v", err)
	}
	second, err := Sign(first, priv, id.ID)
	if err != nil {
		t.Fatalf("second Sign: %v", err)
	}
	firstBounds, _ := frontmatterBounds(first)
	secondBounds, _ := frontmatterBounds(second)
	firstRaw := first[firstBounds.openEnd:firstBounds.closeStart]
	secondRaw := second[secondBounds.openEnd:secondBounds.closeStart]
	firstParsed, err := parseFrontmatterYAML(firstRaw)
	if err != nil {
		t.Fatalf("parse first: %v", err)
	}
	secondParsed, err := parseFrontmatterYAML(secondRaw)
	if err != nil {
		t.Fatalf("parse second: %v", err)
	}
	_, firstStart, _, err := canonicalSignatureRange(firstRaw, firstBounds.lineEnding, firstParsed)
	if err != nil {
		t.Fatalf("first signature range: %v", err)
	}
	_, secondStart, _, err := canonicalSignatureRange(secondRaw, secondBounds.lineEnding, secondParsed)
	if err != nil {
		t.Fatalf("second signature range: %v", err)
	}
	if firstStart != secondStart {
		t.Fatalf("signature start moved from %d to %d", firstStart, secondStart)
	}
	if got := removeCanonicalSignatureForTest(t, second); !bytes.Equal(got, source) {
		t.Fatal("canonical replacement changed non-signature bytes")
	}
	if err := Verify(second, identityResolverFor(id)); err != nil {
		t.Fatalf("Verify replacement: %v", err)
	}
}

func TestExplicitDocumentEndPreservesPrecedingTrivia(t *testing.T) {
	source := explicitMarkerSource(false)
	source = bytes.Replace(source, []byte("... # end frontmatter\n"), []byte("# before marker\n\n... # end frontmatter\n"), 1)
	id, priv := generatedSigningIdentity(t, "explicit-marker-trivia")
	signed, err := Sign(source, priv, id.ID)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if got := removeCanonicalSignatureForTest(t, signed); !bytes.Equal(got, source) {
		t.Fatal("removing signature did not restore marker trivia source")
	}
	if err := Verify(signed, identityResolverFor(id)); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}
