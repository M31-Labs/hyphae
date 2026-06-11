package spore

import (
	"fmt"
	"os"
	"testing"

	"m31labs.dev/hyphae/internal/identity"
)

// TestReproSubmitThenVerify_WithProposedWrites is the regression test for the
// production bug: a spore with proposed_writes is signed + submitted via the
// same code path as the CLI (Sign → SubmitBytes), then read back from the inbox
// and Verified with --verify.
//
// Root cause: Sign computed the frontmatter substance hash from the ORIGINAL
// (pre-normalisation) parsed frontmatter, while Verify computed it from the
// POST-normalisation parsed frontmatter. The normalisation is the yaml.Node
// round-trip in removeSignatureBlock (called by injectSignature). For spores
// containing proposed_writes with body: | block scalars, the re-indented YAML
// caused mdpp.Parse to produce different string values (with vs without trailing
// newline), leading to a substance hash mismatch on untampered files.
//
// Fix: computeCanonicalFmSubstanceHash builds a synthetic signed document
// (normalised fm + placeholder signature block) and hashes its mdpp-parsed
// frontmatter — matching exactly the context Verify sees.
func TestReproSubmitThenVerify_WithProposedWrites(t *testing.T) {
	id, priv, err := identity.Generate("m31labs", "testbot", "hypha://m31labs/research")
	if err != nil {
		t.Fatalf("identity.Generate: %v", err)
	}
	resolver := IdentityResolver(func(uri string) (identity.Identity, error) {
		if uri == id.ID {
			return id, nil
		}
		return identity.Identity{}, fmt.Errorf("unknown identity %q", uri)
	})

	// Spore with proposed_writes containing a body: | block scalar — the exact
	// shape that triggers the normalisation divergence.
	source := []byte(`---
mdpp: "0.1"
id: spore.2026-06-10.repro.pw01
type: spore
space: hypha://m31labs/research
status: unreviewed
created: 2026-06-10T10:00:00Z

agent:
  id: agent://cloud/test
  kind: ephemeral

confidence: high

source_refs:
  - hypha://m31labs/research/concept.hyphae

proposed_writes:
  - kind: append_section
    target: hypha://m31labs/research/concept.hyphae
    heading: "New Section"
    body: |
      Some content to append here.
---

# Body

This is the spore body.
`)

	// Step 1: sign (exactly what the CLI does with --sign).
	signed, err := Sign(source, priv, id.ID)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	// Step 2: submit via SubmitBytes (exactly what CLI does with --sign).
	spaceRoot := t.TempDir()
	filePath, _, err := SubmitBytes(signed, spaceRoot)
	if err != nil {
		t.Fatalf("SubmitBytes: %v", err)
	}

	// Step 3: read back from inbox (exactly what graft --verify does).
	ondisk, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read inbox file: %v", err)
	}

	// Step 4: verify — must pass for an untampered spore.
	if err := Verify(ondisk, resolver); err != nil {
		t.Fatalf("Verify on inbox file FAILED (regression): %v\n\nThis means Sign and Verify computed different substance hashes for an untampered spore with proposed_writes containing body: | block scalars.", err)
	}
}

// TestReproSubmitThenVerify_TamperedProposedWritesStillFails verifies that the
// fix does NOT make tampered proposed_writes pass verification. Modifying the
// body text in proposed_writes after signing must still be detected.
func TestReproSubmitThenVerify_TamperedProposedWritesStillFails(t *testing.T) {
	id, priv, err := identity.Generate("m31labs", "testbot", "hypha://m31labs/research")
	if err != nil {
		t.Fatalf("identity.Generate: %v", err)
	}
	resolver := IdentityResolver(func(uri string) (identity.Identity, error) {
		if uri == id.ID {
			return id, nil
		}
		return identity.Identity{}, fmt.Errorf("unknown identity %q", uri)
	})

	source := []byte(`---
mdpp: "0.1"
id: spore.2026-06-10.repro.pw02
type: spore
space: hypha://m31labs/research
status: unreviewed
created: 2026-06-10T10:00:00Z

agent:
  id: agent://cloud/test
  kind: ephemeral

confidence: high

source_refs:
  - hypha://m31labs/research/concept.hyphae

proposed_writes:
  - kind: append_section
    target: hypha://m31labs/research/concept.hyphae
    heading: "New Section"
    body: |
      Original body content.
---

# Body

This is the spore body.
`)

	signed, err := Sign(source, priv, id.ID)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	// Tamper: change the proposed_writes body text inline.
	const originalText = "Original body content."
	const evilText = "EVIL injected content."
	signedStr := string(signed)
	idx := -1
	for i := 0; i+len(originalText) <= len(signedStr); i++ {
		if signedStr[i:i+len(originalText)] == originalText {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("could not find original text in signed bytes for tampering")
	}
	tampered := []byte(signedStr[:idx] + evilText + signedStr[idx+len(originalText):])

	if err := Verify(tampered, resolver); err == nil {
		t.Fatal("Verify must FAIL for tampered proposed_writes body, but returned nil (security regression)")
	}
}
