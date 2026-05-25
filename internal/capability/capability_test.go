package capability

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/odvcencio/hyphae/internal/db"
	"github.com/odvcencio/hyphae/internal/types"
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
