package spore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"m31labs.dev/hyphae/internal/identity"
)

const v2TestSource = `---
mdpp: "0.1"
id: spore.2026-10-05.example.v2
type: spore
space: hypha://example/knowledge
status: unreviewed
created: 2026-10-05T12:00:00Z
agent: {id: agent://example/session, kind: ephemeral, model: example}
confidence: high
source_refs: [hypha://example/knowledge/concepts/signing]
proposed_writes:
  - kind: create_file
    path: reports/example.md
    body: |
      ---
      id: report.example
      type: report
      ---
      # Example
proposed_edges: [{kind: supports, src: spore.2026-10-05.example.v2, dst: concept.example, confidence: 0.9}]
---
`

func signingIdentity(t testing.TB) (identity.Identity, identity.PrivateKey, IdentityResolver) {
	t.Helper()
	id, priv, err := identity.Generate("example", "reviewer", "hypha://example/knowledge")
	if err != nil {
		t.Fatal(err)
	}
	return id, priv, func(uri string) (identity.Identity, error) {
		if uri != id.ID {
			return identity.Identity{}, fmt.Errorf("unknown signer")
		}
		return id, nil
	}
}

func TestV2ScopeAndReceipts(t *testing.T) {
	id, priv, resolve := signingIdentity(t)
	signed, err := Sign([]byte(v2TestSource), priv, id.ID)
	if err != nil {
		t.Fatal(err)
	}
	report, err := VerifyDetailed(signed, resolve)
	if err != nil {
		t.Fatal(err)
	}
	if report.Version != 2 || report.Status != "VALID" {
		t.Fatalf("report: %+v", report)
	}
	if report.BodyHash != hashBytes(nil) {
		t.Fatalf("expected empty authored body: %s", report.BodyHash)
	}
	if report.ContentHash == report.BodyHash || report.FrontmatterHash == report.BodyHash {
		t.Fatal("proposal substance must affect the displayed hashes")
	}
	for _, field := range []string{"version: 2", "body_hash:", "frontmatter_hash:", "content_hash:"} {
		if !bytes.Contains(signed, []byte(field)) {
			t.Errorf("missing %s", field)
		}
	}
	root := t.TempDir()
	path, receipt, err := SubmitBytes(signed, root)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ContentHash != report.ContentHash {
		t.Fatal("submit receipt does not match signed canonical content")
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, signed) {
		t.Fatal("SubmitBytes changed signed source")
	}
	_, receipt, err = Amend(signed, root)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ContentHash != report.ContentHash {
		t.Fatal("amend receipt does not match signed canonical content")
	}
	unsigned, errs := Parse([]byte(v2TestSource))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	path, receipt, err = Submit(unsigned, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stored, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	signed, err = Sign(stored, priv, id.ID)
	if err != nil {
		t.Fatal(err)
	}
	report, err = VerifyDetailed(signed, resolve)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ContentHash != report.ContentHash {
		t.Fatal("unsigned receipt uses a different content encoding")
	}
}

func TestLegacyV1Fixture(t *testing.T) {
	source, err := os.ReadFile("testdata/v1.md")
	if err != nil {
		t.Fatal(err)
	}
	public, err := os.ReadFile("testdata/v1-identity.json")
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		ID        string `json:"id"`
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal(public, &record); err != nil {
		t.Fatal(err)
	}
	resolve := func(uri string) (identity.Identity, error) {
		if uri != record.ID {
			return identity.Identity{}, fmt.Errorf("wrong signer")
		}
		return identity.Identity{ID: record.ID, PublicKey: record.PublicKey}, nil
	}
	for _, version := range []string{"", "  version: 1\n"} {
		doc := bytes.Replace(source, []byte("signature:\n"), []byte("signature:\n"+version), 1)
		report, err := VerifyDetailed(doc, resolve)
		if err != nil {
			t.Fatal(err)
		}
		if report.Version != 1 || report.Note != "v1: content_hash covers body only; frontmatter verified via payload" {
			t.Fatalf("legacy report: %+v", report)
		}
		if err := Verify(bytes.Replace(doc, []byte("# Example"), []byte("# Changed"), 1), resolve); err == nil {
			t.Fatal("v1 proposal mutation accepted")
		}
		if err := Verify(append(append([]byte(nil), doc...), []byte("tampered")...), resolve); err == nil {
			t.Fatal("v1 body mutation accepted")
		}
		promoted := bytes.Replace(doc, []byte("status: unreviewed"), []byte("status: accepted"), 1)
		if err := Verify(promoted, resolve); err != nil {
			t.Fatal(err)
		}
	}
}

func TestV2SingleFieldMutations(t *testing.T) {
	id, priv, resolve := signingIdentity(t)
	signed, err := Sign([]byte(v2TestSource+"# Authored body\n"), priv, id.ID)
	if err != nil {
		t.Fatal(err)
	}
	fm, body, err := parseSigningSource(signed)
	if err != nil {
		t.Fatal(err)
	}
	// Enumerate all scalar leaves of the signed content, including proposal bodies
	// and fields nested inside lists/maps. Change exactly one leaf per document.
	var walk func(any, []any)
	walk = func(value any, path []any) {
		switch value := value.(type) {
		case map[string]any:
			for key, child := range value {
				if len(path) == 0 && (key == "status" || key == "signature") {
					continue
				}
				walk(child, append(append([]any(nil), path...), key))
			}
		case []any:
			for i, child := range value {
				walk(child, append(append([]any(nil), path...), i))
			}
		default:
			name := fmt.Sprint(path)
			t.Run(name, func(t *testing.T) {
				copyFM, _, _ := parseSigningSource(signed)
				var parent any = copyFM
				for _, part := range path[:len(path)-1] {
					switch part := part.(type) {
					case string:
						parent = parent.(map[string]any)[part]
					case int:
						parent = parent.([]any)[part]
					}
				}
				changed := fmt.Sprint(value) + " changed"
				switch last := path[len(path)-1].(type) {
				case string:
					parent.(map[string]any)[last] = changed
				case int:
					parent.([]any)[last] = changed
				}
				encoded, e := yaml.Marshal(copyFM)
				if e != nil {
					t.Fatal(e)
				}
				mutated := append([]byte("---\n"+string(encoded)+"---\n"), body...)
				report, e := VerifyDetailed(mutated, resolve)
				if e == nil || report.Status != "INVALID" {
					t.Fatalf("mutation accepted: %+v", report)
				}
			})
		}
	}
	walk(fm, nil)
	report, err := VerifyDetailed(append(append([]byte(nil), signed...), []byte("changed")...), resolve)
	if err == nil || strings.Join(report.Changed, ",") != "body" {
		t.Fatalf("body diagnostic: %+v %v", report, err)
	}
	changed := bytes.Replace(signed, []byte("# Example"), []byte("# Changed"), 1)
	report, err = VerifyDetailed(changed, resolve)
	if err == nil || strings.Join(report.Changed, ",") != "frontmatter" {
		t.Fatalf("frontmatter diagnostic: %+v %v", report, err)
	}
}

func TestV2SignatureFailures(t *testing.T) {
	id, priv, resolve := signingIdentity(t)
	signed, err := Sign([]byte(v2TestSource), priv, id.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ old, new, want string }{
		{"version: 2", "version: 99", "unsupported signature version"},
		{"alg: ed25519", "alg: other", "unsupported signature alg"},
		{"value: ed25519:", "value: invalid:", "missing ed25519: prefix"},
		{"value: ed25519:", "value: ed25519:!", "decode signature value"},
		{"key: " + id.ID, "key: identity://example/unknown", "unknown signer"},
	} {
		t.Run(test.want, func(t *testing.T) {
			changed := bytes.Replace(signed, []byte(test.old), []byte(test.new), 1)
			if err := Verify(changed, resolve); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("got %v", err)
			}
		})
	}
	fm, body, _ := parseSigningSource(signed)
	sig := fm["signature"].(map[string]any)
	sig["content_hash"] = "sha256:incorrect"
	encoded, _ := yaml.Marshal(fm)
	if err := Verify(append([]byte("---\n"+string(encoded)+"---\n"), body...), resolve); err == nil || !strings.Contains(err.Error(), "canonical payload") {
		t.Fatal(err)
	}
	sig["content_hash"], _ = ContentHash(signed)
	sig["value"] = "ed25519:" + strings.Repeat("A", 88)
	encoded, _ = yaml.Marshal(fm)
	if err := Verify(append([]byte("---\n"+string(encoded)+"---\n"), body...), resolve); err == nil || !strings.Contains(err.Error(), "verification failed") {
		t.Fatal(err)
	}
	report, err := VerifyDetailed([]byte(v2TestSource), resolve)
	if !errors.Is(err, ErrUnsigned) || report.Status != "UNSIGNED" {
		t.Fatalf("%+v %v", report, err)
	}
}

func TestV2RejectsAmbiguousYAML(t *testing.T) {
	id, priv, _ := signingIdentity(t)
	for _, extra := range []string{"confidence: low\n", "extra: {1: value}\n", "extra: .nan\n", "extra: .inf\n"} {
		source := strings.Replace(v2TestSource, "---\n", "---\n"+extra, 1)
		if _, err := Sign([]byte(source), priv, id.ID); err == nil {
			t.Fatalf("accepted ambiguous/unsupported YAML: %s", extra)
		}
	}
}

func FuzzSignVerifyYAMLStyles(f *testing.F) {
	f.Add("first line\nsecond line\n", uint8(0), uint8(0))
	f.Add("unicode: λ # ---\n\n", uint8(1), uint8(1))
	f.Add("no final newline", uint8(2), uint8(2))
	f.Add("", uint8(3), uint8(3))
	f.Add("line\n\n", uint8(4), uint8(4))
	f.Fuzz(func(t *testing.T, text string, style, indent uint8) {
		if len(text) > 8192 {
			t.Skip()
		}
		id, priv, resolve := signingIdentity(t)
		fm, _, err := parseSigningSource([]byte(v2TestSource))
		if err != nil {
			t.Fatal(err)
		}
		fm["proposed_writes"].([]any)[0].(map[string]any)["body"] = text
		var node yaml.Node
		if err := node.Encode(fm); err != nil {
			t.Skip()
		}
		styles := []yaml.Style{yaml.LiteralStyle, yaml.FoldedStyle, yaml.DoubleQuotedStyle, 0, yaml.SingleQuotedStyle}
		var restyle func(*yaml.Node)
		restyle = func(n *yaml.Node) {
			if n.Kind == yaml.ScalarNode && n.Tag == "!!str" {
				n.Style = styles[int(style)%len(styles)]
			}
			if n.Kind == yaml.MappingNode && style%2 == 1 {
				n.Style = yaml.FlowStyle
			}
			for _, child := range n.Content {
				restyle(child)
			}
		}
		restyle(&node)
		var buf bytes.Buffer
		encoder := yaml.NewEncoder(&buf)
		encoder.SetIndent(2 + 2*int(indent%3))
		if err := encoder.Encode(&node); err != nil {
			t.Skip()
		}
		encoder.Close()
		source := []byte("---\n" + buf.String() + "---\n# Authored\n")
		if style%3 == 0 {
			source = bytes.ReplaceAll(source, []byte("\n"), []byte("\r\n"))
		}
		if _, _, err := parseSigningSource(source); err != nil {
			t.Skip("YAML renderer produced invalid source")
		}
		signed, err := Sign(source, priv, id.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := Verify(signed, resolve); err != nil {
			t.Fatal(err)
		}
		originalFM, originalBody, _ := parseSigningSource(source)
		signedFM, signedBody, _ := parseSigningSource(signed)
		originalHash, _ := ContentHash(source)
		signedHash, _ := ContentHash(signed)
		if originalHash != signedHash || !bytes.Equal(originalBody, signedBody) {
			t.Fatal("signing changed authored content")
		}
		originalProposal := originalFM["proposed_writes"].([]any)[0].(map[string]any)["body"]
		signedProposal := signedFM["proposed_writes"].([]any)[0].(map[string]any)["body"]
		if originalProposal != signedProposal {
			t.Fatal("signing changed block scalar substance")
		}
		// Reserialize the signed mapping in flow style and another indentation.
		var alternate yaml.Node
		if err := yaml.Unmarshal(signed[4:len(signed)-len(signedBody)-4], &alternate); err != nil {
			t.Fatal(err)
		}
		var flow func(*yaml.Node)
		flow = func(n *yaml.Node) {
			if n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode {
				n.Style = yaml.FlowStyle
			}
			if n.Kind == yaml.ScalarNode && n.Tag == "!!str" {
				n.Style = yaml.DoubleQuotedStyle
			}
			for _, child := range n.Content {
				flow(child)
			}
		}
		flow(&alternate)
		encoded, err := yaml.Marshal(&alternate)
		if err != nil {
			t.Fatal(err)
		}
		reformatted := append([]byte("---\n"+string(encoded)+"---\n"), signedBody...)
		if err := Verify(reformatted, resolve); err != nil {
			t.Fatalf("equivalent YAML style invalidated signature: %v", err)
		}
		modified := bytes.Replace(signed, []byte("# Authored"), []byte("# Modified"), 1)
		if err := Verify(modified, resolve); err == nil {
			t.Fatal("body mutation accepted")
		}
	})
}
