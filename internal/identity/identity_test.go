package identity_test

import (
	"os"
	"strings"
	"testing"

	"github.com/odvcencio/hyphae/internal/identity"
)

// TestRoundTrip verifies Generate → Save → Load + LoadPrivate → Sign → Verify.
func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()

	// 1. Generate
	id, priv, err := identity.Generate("m31labs", "odvcencio", "hypha://m31labs/hyphae")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if id.PublicKey == "" {
		t.Fatal("Generate: empty public key")
	}
	if !strings.HasPrefix(id.PublicKey, "ed25519:base64:") {
		t.Fatalf("Generate: unexpected public key format: %q", id.PublicKey)
	}
	if id.ID != "identity://m31labs/odvcencio" {
		t.Fatalf("Generate: unexpected ID %q", id.ID)
	}
	if id.Status != "active" {
		t.Fatalf("Generate: expected status 'active', got %q", id.Status)
	}

	// 2. Save
	mdPath, keyPath, err := identity.Save(dir, id, priv)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if mdPath == "" || keyPath == "" {
		t.Fatal("Save: returned empty paths")
	}

	// Verify file modes.
	fi, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("Stat keyPath: %v", err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Fatalf("key file permissions: got %o, want 0600", fi.Mode().Perm())
	}

	// 3. Load public identity
	loaded, err := identity.Load(dir, "odvcencio")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.ID != id.ID {
		t.Fatalf("Load: ID mismatch: got %q, want %q", loaded.ID, id.ID)
	}
	if loaded.PublicKey != id.PublicKey {
		t.Fatalf("Load: public key mismatch")
	}
	if loaded.Space != id.Space {
		t.Fatalf("Load: space mismatch: got %q, want %q", loaded.Space, id.Space)
	}
	if !loaded.CreatedAt.Equal(id.CreatedAt) {
		t.Fatalf("Load: created_at mismatch: got %v, want %v", loaded.CreatedAt, id.CreatedAt)
	}
	if loaded.FilePath == "" {
		t.Fatal("Load: FilePath is empty")
	}

	// 4. LoadPrivate
	loadedPriv, err := identity.LoadPrivate(dir, "odvcencio")
	if err != nil {
		t.Fatalf("LoadPrivate: %v", err)
	}

	// 5. Sign
	message := []byte("hyphae test payload v0.1.1")
	sig := identity.Sign(loadedPriv, message)
	if len(sig) == 0 {
		t.Fatal("Sign: empty signature")
	}

	// 6. Verify with loaded public identity
	if !identity.Verify(loaded, message, sig) {
		t.Fatal("Verify: expected true, got false")
	}
}

// TestBadPermsRefusal verifies that LoadPrivate rejects a key file with
// permissions other than 0600.
func TestBadPermsRefusal(t *testing.T) {
	dir := t.TempDir()

	id, priv, err := identity.Generate("testauth", "alice", "hypha://testauth/test")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	_, keyPath, err := identity.Save(dir, id, priv)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Relax permissions to 0644.
	if err := os.Chmod(keyPath, 0644); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	_, err = identity.LoadPrivate(dir, "alice")
	if err == nil {
		t.Fatal("LoadPrivate: expected error for unsafe permissions, got nil")
	}
	if !strings.Contains(err.Error(), "unsafe permissions") && !strings.Contains(err.Error(), "0600") {
		t.Fatalf("LoadPrivate: error should mention permissions, got: %v", err)
	}
}

// TestTamperedMessageVerifyFails ensures that Verify returns false when the
// signed message is different from the verified message.
func TestTamperedMessageVerifyFails(t *testing.T) {
	dir := t.TempDir()

	id, priv, err := identity.Generate("lab", "bob", "hypha://lab/test")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, _, err := identity.Save(dir, id, priv); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := identity.Load(dir, "bob")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	original := []byte("original message")
	sig := identity.Sign(priv, original)

	tampered := []byte("tampered message")
	if identity.Verify(loaded, tampered, sig) {
		t.Fatal("Verify: expected false for tampered message, got true")
	}
}

// TestSaveRefusesOverwrite ensures that Save refuses to overwrite an existing
// identity file.
func TestSaveRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()

	id, priv, err := identity.Generate("auth", "carol", "hypha://auth/test")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, _, err := identity.Save(dir, id, priv); err != nil {
		t.Fatalf("First Save: %v", err)
	}

	id2, priv2, _ := identity.Generate("auth", "carol", "hypha://auth/test")
	_, _, err = identity.Save(dir, id2, priv2)
	if err == nil {
		t.Fatal("Save: expected error on overwrite, got nil")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("Save: error should mention 'already exists', got: %v", err)
	}
}

// TestList verifies that List returns all saved identities in a directory.
func TestList(t *testing.T) {
	dir := t.TempDir()

	names := []string{"dave", "eve", "frank"}
	for _, n := range names {
		id, priv, err := identity.Generate("listauth", n, "hypha://listauth/test")
		if err != nil {
			t.Fatalf("Generate %s: %v", n, err)
		}
		if _, _, err := identity.Save(dir, id, priv); err != nil {
			t.Fatalf("Save %s: %v", n, err)
		}
	}

	ids, err := identity.List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ids) != len(names) {
		t.Fatalf("List: expected %d identities, got %d", len(names), len(ids))
	}
}
