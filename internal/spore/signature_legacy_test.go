package spore

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/hyphae/internal/identity"
	"m31labs.dev/mdpp"
)

func legacyTestSource(t *testing.T, version int, omit []string) ([]byte, IdentityResolver) {
	t.Helper()
	id, priv, resolve := signingIdentity(t)
	source := []byte(v2TestSource)
	doc, err := mdpp.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	fm := doc.Frontmatter()
	for _, field := range omit {
		delete(fm, field)
	}
	bodyHash := sha256.Sum256(nil)
	// Encode the historical format directly, independent of the verifier.
	payload := fmt.Sprintf("agent://example/session\nspore.2026-10-05.example.v2\n2026-10-05T12:00:00Z\n%x\n", bodyHash)
	if version == 1 {
		payload += computeFmSubstanceHash(fm) + "\n"
	}
	signature := base64.StdEncoding.EncodeToString(identity.Sign(priv, []byte(payload)))
	block := fmt.Sprintf("signature:\n  alg: ed25519\n  key: %s\n  content_hash: sha256:%x\n  value: ed25519:%s\n", id.ID, bodyHash, signature)
	closing := bytes.LastIndex(source, []byte("\n---\n")) + 1
	result := append([]byte{}, source[:closing]...)
	result = append(result, block...)
	result = append(result, source[closing:]...)
	return result, resolve
}

func TestSignerMatchesFullResolvedURI(t *testing.T) {
	id, priv, _ := signingIdentity(t)
	permissive := func(string) (identity.Identity, error) { return id, nil }
	v2, err := Sign([]byte(v2TestSource), priv, id.ID)
	if err != nil {
		t.Fatal(err)
	}
	v1, v1Resolve := legacyTestSource(t, 1, nil)
	v0, v0Resolve := legacyTestSource(t, 0, nil)
	for _, test := range []struct {
		source  []byte
		resolve IdentityResolver
		status  string
	}{
		{v2, permissive, "VALID"}, {v1, v1Resolve, "VALID"}, {v0, v0Resolve, "V0_LEGACY"},
	} {
		resolved, _ := test.resolve("identity://example/reviewer")
		resolve := func(string) (identity.Identity, error) { return resolved, nil }
		report, err := VerifyDetailed(test.source, resolve)
		if report.Status != test.status || (err != nil && !errors.Is(err, ErrLegacyUnverified)) || report.Signer != resolved.ID {
			t.Fatalf("correct identity: %+v %v", report, err)
		}
		forged := bytes.ReplaceAll(test.source, []byte("key: identity://example/reviewer"), []byte("key: identity://trusted/reviewer"))
		report, err = VerifyDetailed(forged, resolve)
		if err == nil || report.Status != "INVALID" || report.Signer != resolved.ID || strings.Contains(report.Signer, "trusted") {
			t.Fatalf("forged identity: %+v %v", report, err)
		}
	}
}

func TestLegacyV0ScopeAndMutations(t *testing.T) {
	source, resolve := legacyTestSource(t, 0, nil)
	report, err := VerifyDetailed(source, resolve)
	if !errors.Is(err, ErrLegacyUnverified) || report.Status != "V0_LEGACY" || report.Version != 0 || report.FrontmatterHash != "" || report.Note != "v0 legacy: covers body only; frontmatter and proposals NOT covered" {
		t.Fatalf("%+v %v", report, err)
	}
	if !errors.Is(Verify(source, resolve), ErrLegacyUnverified) {
		t.Fatal("v0 must not pass full verification")
	}
	for _, edit := range [][2]string{{"# Example", "# Edited proposal"}, {"confidence: high", "confidence: low"}} {
		changed := bytes.Replace(source, []byte(edit[0]), []byte(edit[1]), 1)
		r, err := VerifyDetailed(changed, resolve)
		if r.Status != "V0_LEGACY" || !errors.Is(err, ErrLegacyUnverified) {
			t.Fatalf("unsigned field edit: %+v %v", r, err)
		}
	}
	for _, changed := range [][]byte{
		append(append([]byte{}, source...), []byte("Edited body\n")...),
		bytes.Replace(source, []byte("id: spore.2026-10-05.example.v2"), []byte("id: spore.2026-10-05.example.other"), 1),
		bytes.Replace(source, []byte("created: 2026-10-05T12:00:00Z"), []byte("created: 2026-10-05T13:00:00Z"), 1),
		bytes.Replace(source, []byte("agent://example/session"), []byte("agent://example/other"), 1),
	} {
		r, err := VerifyDetailed(changed, resolve)
		if r.Status != "INVALID" || err == nil {
			t.Fatalf("signed field edit: %+v %v", r, err)
		}
	}
}

func TestLegacyProposalMismatch(t *testing.T) {
	for _, omitted := range [][]string{{"proposed_writes"}, {"proposed_edges"}, {"proposed_writes", "proposed_edges"}} {
		source, resolve := legacyTestSource(t, 1, omitted)
		report, err := VerifyDetailed(source, resolve)
		if err == nil || report.Status != "INVALID" || strings.Join(report.ProposalMismatch, ",") != strings.Join(omitted, ",") {
			t.Fatalf("%+v %v", report, err)
		}
	}
}

func TestAuditCountsAndReadOnly(t *testing.T) {
	root := t.TempDir()
	v0, resolve0 := legacyTestSource(t, 0, nil)
	v1, resolve1 := legacyTestSource(t, 1, nil)
	mismatch, resolveMismatch := legacyTestSource(t, 1, []string{"proposed_writes"})
	id, priv, resolve2 := signingIdentity(t)
	v2, err := Sign([]byte(v2TestSource), priv, id.ID)
	if err != nil {
		t.Fatal(err)
	}
	keys := []IdentityResolver{resolve0, resolve1, resolveMismatch, resolve2}
	// Each fixture has the same URI but its own key.
	resolve := resolve2
	for i, content := range [][]byte{v0, v1, mismatch, v2} {
		dir := filepath.Join(root, fmt.Sprint(i))
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "fixture.md")
		if err := os.WriteFile(path, content, 0644); err != nil {
			t.Fatal(err)
		}
		unsigned := []byte(v2TestSource)
		if err := os.WriteFile(filepath.Join(dir, "unsigned.mdpp"), unsigned, 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "placeholder.md"), bytes.Replace(unsigned, []byte("\n---\n"), []byte("\nsignature: none\n---\n"), 1), 0644); err != nil {
			t.Fatal(err)
		}
		report, err := Audit(dir, keys[i])
		if err != nil || report.Counts.Total != 3 || report.Counts.Unsigned != 2 {
			t.Fatalf("%+v %v", report, err)
		}
		if i == 0 && report.Counts.V0Legacy != 1 || i == 1 && report.Counts.V1 != 1 || i == 2 && (report.Counts.Invalid != 1 || report.Counts.ProposalMismatch != 1 || len(report.Cases) != 1) || i == 3 && report.Counts.V2 != 1 {
			t.Fatalf("%+v", report)
		}
		again, _ := os.ReadFile(path)
		if !bytes.Equal(content, again) {
			t.Fatal("audit changed fixture")
		}
	}
	// A mismatched or absent public identity remains INVALID, never legacy.
	report, err := Audit(filepath.Join(root, "0"), resolve)
	if err != nil || report.Counts.Invalid != 1 {
		t.Fatalf("%+v %v", report, err)
	}
}

func TestAuditIncludesMalformedSignatureYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "malformed.md")
	source := []byte("---\nid: spore.example\ntype: spore\nsignature: [broken\n---\n")
	if err := os.WriteFile(path, source, 0644); err != nil {
		t.Fatal(err)
	}
	report, err := Audit(dir, nil)
	if err != nil || report.Counts.Invalid != 1 || len(report.Cases) != 1 {
		t.Fatalf("%+v %v", report, err)
	}
}
