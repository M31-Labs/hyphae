package graft

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestV0ProposalsRequireExplicitOptIn(t *testing.T) {
	root := t.TempDir()
	space := filepath.Join(root, "spaces", "example-knowledge")
	inbox := filepath.Join(space, "inbox", "agents")
	identities := filepath.Join(root, ".catalog", "identities")
	for _, dir := range []string{inbox, identities} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	source, err := os.ReadFile("../spore/testdata/v0.md")
	if err != nil {
		t.Fatal(err)
	}
	public, err := os.ReadFile("../spore/testdata/v0-identity.json")
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal(public, &record); err != nil {
		t.Fatal(err)
	}
	identitySource := "---\nid: identity.reviewer\ntype: identity\nauthority: example\nkey_alg: ed25519\npublic_key: " + record.PublicKey + "\n---\n"
	if err := os.WriteFile(filepath.Join(identities, "reviewer.md"), []byte(identitySource), 0644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(inbox, "legacy.md")
	if err := os.WriteFile(path, source, 0644); err != nil {
		t.Fatal(err)
	}
	conn := openTestDB(t)
	id := "spore.2026-10-05.example.legacy"
	for _, verify := range []bool{false, true} {
		if _, err := ApplyWithOpts(conn, root, space, id, "identity://example/reviewer", ApplyOpts{RequireVerified: verify}); err == nil || !strings.Contains(err.Error(), "--allow-legacy-proposals") {
			t.Fatalf("opt-in requirement: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(space, "reports", "example.md")); !os.IsNotExist(err) {
		t.Fatal("unverified proposals applied")
	}
	preview, err := ApplyWithOpts(conn, root, space, id, "identity://example/reviewer", ApplyOpts{DryRun: true, RequireVerified: true})
	if err != nil || len(preview.Warnings) != 1 || len(preview.AppliedWrites) != 1 {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	again, _ := os.ReadFile(path)
	if !bytes.Equal(source, again) {
		t.Fatal("blocked graft or preview changed spore")
	}
	for _, tampered := range [][]byte{
		append(append([]byte{}, source...), []byte("Tampered authored body\n")...),
		bytes.Replace(source, []byte("key: identity://example/reviewer"), []byte("key: identity://trusted/reviewer"), 1),
	} {
		if err := os.WriteFile(path, tampered, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := ApplyWithOpts(conn, root, space, id, "identity://example/reviewer", ApplyOpts{AllowLegacyProposals: true}); err == nil {
			t.Fatal("legacy opt-in bypassed an invalid signature")
		}
	}
	if err := os.WriteFile(path, source, 0644); err != nil {
		t.Fatal(err)
	}
	result, err := ApplyWithOpts(conn, root, space, id, "identity://example/reviewer", ApplyOpts{AllowLegacyProposals: true, RequireVerified: true})
	if err != nil || len(result.AppliedWrites) != 1 || len(result.Warnings) != 1 {
		t.Fatalf("explicit apply: %+v %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(space, "reports", "example.md")); err != nil {
		t.Fatal(err)
	}
}
