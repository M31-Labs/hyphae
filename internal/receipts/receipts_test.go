package receipts_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"m31labs.dev/hyphae/internal/db"
	"m31labs.dev/hyphae/internal/receipts"
	"m31labs.dev/hyphae/internal/types"
)

// openTestDB opens a fresh SQLite DB in the test's temp directory.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// makeReceipt builds a fully-populated Receipt for tests.
func makeReceipt(id, spaceID string, createdAt time.Time) types.Receipt {
	exp := createdAt.Add(24 * time.Hour)
	return types.Receipt{
		ID:              id,
		SpaceID:         spaceID,
		SubjectID:       "spore_abc123",
		SubjectKind:     "spore",
		Action:          "spore:create",
		Status:          "accepted",
		ContentHash:     "sha256:deadbeef",
		IdentityID:      "identity_xyz",
		CreatedAt:       createdAt,
		ExpiresAt:       &exp,
		PermissionsUsed: []string{"spore:write", "graph:read"},
		NextState:       "accepted",
		MetadataJSON:    `{"extra":"value"}`,
	}
}

// TestWriteGetRoundtrip verifies that all fields survive a Write→Get cycle.
func TestWriteGetRoundtrip(t *testing.T) {
	conn := openTestDB(t)

	now := time.Now().UTC().Truncate(time.Second) // RFC3339 has second precision
	r := makeReceipt("receipt_001", "space_alpha", now)

	if err := receipts.Write(conn, r); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := receipts.Get(conn, r.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Scalar fields.
	assertEq(t, "ID", r.ID, got.ID)
	assertEq(t, "SpaceID", r.SpaceID, got.SpaceID)
	assertEq(t, "SubjectID", r.SubjectID, got.SubjectID)
	assertEq(t, "SubjectKind", r.SubjectKind, got.SubjectKind)
	assertEq(t, "Action", r.Action, got.Action)
	assertEq(t, "Status", r.Status, got.Status)
	assertEq(t, "ContentHash", r.ContentHash, got.ContentHash)
	assertEq(t, "IdentityID", r.IdentityID, got.IdentityID)

	// Time fields.
	if !got.CreatedAt.Equal(r.CreatedAt) {
		t.Errorf("CreatedAt: want %v, got %v", r.CreatedAt, got.CreatedAt)
	}
	if got.ExpiresAt == nil {
		t.Fatal("ExpiresAt: want non-nil, got nil")
	}
	if !got.ExpiresAt.Equal(*r.ExpiresAt) {
		t.Errorf("ExpiresAt: want %v, got %v", *r.ExpiresAt, *got.ExpiresAt)
	}

	// Metadata round-trip: PermissionsUsed and NextState must be decoded.
	if len(got.PermissionsUsed) != len(r.PermissionsUsed) {
		t.Fatalf("PermissionsUsed len: want %d, got %d", len(r.PermissionsUsed), len(got.PermissionsUsed))
	}
	for i, p := range r.PermissionsUsed {
		if got.PermissionsUsed[i] != p {
			t.Errorf("PermissionsUsed[%d]: want %q, got %q", i, p, got.PermissionsUsed[i])
		}
	}
	assertEq(t, "NextState", r.NextState, got.NextState)

	// The caller-supplied MetadataJSON key "extra" must survive.
	if got.MetadataJSON == "" {
		t.Fatal("MetadataJSON: want non-empty, got empty")
	}
	// Verify the merged JSON contains the caller's key.
	if !contains(got.MetadataJSON, `"extra"`) {
		t.Errorf("MetadataJSON missing caller key: %s", got.MetadataJSON)
	}
}

// TestListFiltering writes 5 receipts across 2 spaces / 2 subjects and checks
// that all filter combinations return the right rows in newest-first order.
func TestListFiltering(t *testing.T) {
	conn := openTestDB(t)

	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	receiptsToWrite := []types.Receipt{
		// space_a, subject_1
		{ID: "r1", SpaceID: "space_a", SubjectID: "subj_1", SubjectKind: "spore",
			Action: "spore:create", Status: "accepted",
			CreatedAt: base.Add(0 * time.Hour)},
		// space_a, subject_2
		{ID: "r2", SpaceID: "space_a", SubjectID: "subj_2", SubjectKind: "spore",
			Action: "graft", Status: "accepted",
			CreatedAt: base.Add(1 * time.Hour)},
		// space_a, subject_1 (later)
		{ID: "r3", SpaceID: "space_a", SubjectID: "subj_1", SubjectKind: "spore",
			Action: "spore:create", Status: "rejected",
			CreatedAt: base.Add(2 * time.Hour)},
		// space_b, subject_1
		{ID: "r4", SpaceID: "space_b", SubjectID: "subj_1", SubjectKind: "spore",
			Action: "spore:create", Status: "accepted",
			CreatedAt: base.Add(3 * time.Hour)},
		// space_b, subject_2
		{ID: "r5", SpaceID: "space_b", SubjectID: "subj_2", SubjectKind: "spore",
			Action: "graft", Status: "accepted",
			CreatedAt: base.Add(4 * time.Hour)},
	}

	for _, r := range receiptsToWrite {
		if err := receipts.Write(conn, r); err != nil {
			t.Fatalf("Write %s: %v", r.ID, err)
		}
	}

	// All receipts — newest first.
	all, err := receipts.List(conn, receipts.ListFilter{Limit: 10})
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("List all: want 5, got %d", len(all))
	}
	assertNewestFirst(t, "all", all)

	// Filter by space_a.
	bySpaceA, err := receipts.List(conn, receipts.ListFilter{SpaceID: "space_a", Limit: 10})
	if err != nil {
		t.Fatalf("List space_a: %v", err)
	}
	if len(bySpaceA) != 3 {
		t.Fatalf("List space_a: want 3, got %d", len(bySpaceA))
	}
	for _, r := range bySpaceA {
		if r.SpaceID != "space_a" {
			t.Errorf("List space_a: unexpected SpaceID %q", r.SpaceID)
		}
	}
	assertNewestFirst(t, "space_a", bySpaceA)

	// Filter by subj_2 across all spaces.
	bySubj2, err := receipts.List(conn, receipts.ListFilter{SubjectID: "subj_2", Limit: 10})
	if err != nil {
		t.Fatalf("List subj_2: %v", err)
	}
	if len(bySubj2) != 2 {
		t.Fatalf("List subj_2: want 2, got %d", len(bySubj2))
	}

	// Filter by action=graft.
	byGraft, err := receipts.List(conn, receipts.ListFilter{Action: "graft", Limit: 10})
	if err != nil {
		t.Fatalf("List graft: %v", err)
	}
	if len(byGraft) != 2 {
		t.Fatalf("List graft: want 2, got %d", len(byGraft))
	}

	// Filter by Since — only receipts at or after base+2h (r3, r4, r5).
	bySince, err := receipts.List(conn, receipts.ListFilter{Since: base.Add(2 * time.Hour), Limit: 10})
	if err != nil {
		t.Fatalf("List since: %v", err)
	}
	if len(bySince) != 3 {
		t.Fatalf("List since: want 3, got %d", len(bySince))
	}
	assertNewestFirst(t, "since", bySince)

	// Limit capping.
	limited, err := receipts.List(conn, receipts.ListFilter{Limit: 2})
	if err != nil {
		t.Fatalf("List limit=2: %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("List limit=2: want 2, got %d", len(limited))
	}
	// Must be the two newest.
	if limited[0].ID != "r5" {
		t.Errorf("limit=2[0]: want r5, got %s", limited[0].ID)
	}
	if limited[1].ID != "r4" {
		t.Errorf("limit=2[1]: want r4, got %s", limited[1].ID)
	}
}

// TestDuplicateWrite verifies that writing the same ID twice returns
// ErrAlreadyExists without touching the stored row.
func TestDuplicateWrite(t *testing.T) {
	conn := openTestDB(t)

	now := time.Now().UTC().Truncate(time.Second)
	r := makeReceipt("receipt_dup", "space_alpha", now)

	if err := receipts.Write(conn, r); err != nil {
		t.Fatalf("first Write: %v", err)
	}

	// Mutate a field to confirm the stored row is not changed.
	r2 := r
	r2.Status = "rejected"
	r2.ContentHash = "sha256:different"

	err := receipts.Write(conn, r2)
	if !errors.Is(err, receipts.ErrAlreadyExists) {
		t.Fatalf("second Write: want ErrAlreadyExists, got %v", err)
	}

	// Stored row must be the original.
	got, err := receipts.Get(conn, r.ID)
	if err != nil {
		t.Fatalf("Get after dup Write: %v", err)
	}
	assertEq(t, "Status after dup", "accepted", got.Status)
	assertEq(t, "ContentHash after dup", "sha256:deadbeef", got.ContentHash)
}

// --- helpers ---

func assertEq(t *testing.T, field, want, got string) {
	t.Helper()
	if want != got {
		t.Errorf("%s: want %q, got %q", field, want, got)
	}
}

func assertNewestFirst(t *testing.T, label string, rs []types.Receipt) {
	t.Helper()
	for i := 1; i < len(rs); i++ {
		if rs[i].CreatedAt.After(rs[i-1].CreatedAt) {
			t.Errorf("%s: not sorted newest-first at index %d: %v > %v",
				label, i, rs[i].CreatedAt, rs[i-1].CreatedAt)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) <= len(s) && (len(sub) == 0 || func() bool {
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}
