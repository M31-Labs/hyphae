package capability

import (
	"path/filepath"
	"testing"
	"time"

	"m31labs.dev/hyphae/internal/db"
	"m31labs.dev/hyphae/internal/types"
)

func TestIssueAndVerify(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	defer conn.Close()

	cap, err := Issue(conn, "agent://test/x", "m31labs/hyphae",
		[]string{"memory:recall", "spore:create"}, types.Limits{MaxSpores: 3}, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if cap.ID == "" {
		t.Fatal("expected non-empty cap id")
	}

	got, err := Verify(conn, cap.ID)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.Subject != "agent://test/x" {
		t.Errorf("subject = %q, want agent://test/x", got.Subject)
	}
	if len(got.Permissions) != 2 {
		t.Errorf("perms len = %d, want 2", len(got.Permissions))
	}
	if got.Limits.MaxSpores != 3 {
		t.Errorf("limits.max_spores = %d, want 3", got.Limits.MaxSpores)
	}
}

func TestVerifyMissing(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	defer conn.Close()

	if _, err := Verify(conn, "cap_does_not_exist"); err == nil {
		t.Fatal("expected error for missing token")
	}
}

func TestRevoke(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	defer conn.Close()

	cap, err := Issue(conn, "agent://test/y", "s", []string{"memory:recall"}, types.Limits{}, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if err := Revoke(conn, cap.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := Verify(conn, cap.ID); err == nil {
		t.Fatal("expected error after revoke")
	}
}

func TestList(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	defer conn.Close()

	a, err := Issue(conn, "agent://test/a", "space-1", []string{"memory:recall"}, types.Limits{}, time.Hour)
	if err != nil {
		t.Fatalf("issue a: %v", err)
	}
	if _, err := Issue(conn, "agent://test/b", "space-2", []string{"spore:create"}, types.Limits{}, time.Hour); err != nil {
		t.Fatalf("issue b: %v", err)
	}
	if err := Revoke(conn, a.ID); err != nil {
		t.Fatalf("revoke a: %v", err)
	}

	// includeRevoked=false hides the revoked token; only b remains.
	active, err := List(conn, "", false)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 1 || active[0].Subject != "agent://test/b" {
		t.Fatalf("active = %+v, want only agent://test/b", active)
	}

	// includeRevoked=true returns both, and surfaces the revoked marker.
	all, err := List(conn, "", true)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("all len = %d, want 2", len(all))
	}
	var sawRevoked bool
	for _, c := range all {
		if c.ID == a.ID {
			if c.RevokedAt == nil {
				t.Error("expected RevokedAt set on revoked token")
			}
			sawRevoked = true
		}
	}
	if !sawRevoked {
		t.Error("revoked token a missing from includeRevoked listing")
	}

	// spaceID filter scopes to one space.
	scoped, err := List(conn, "space-2", true)
	if err != nil {
		t.Fatalf("list scoped: %v", err)
	}
	if len(scoped) != 1 || scoped[0].SpaceID != "space-2" {
		t.Fatalf("scoped = %+v, want only space-2", scoped)
	}
}
