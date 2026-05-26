package assess_test

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/hyphae/internal/assess"
	"m31labs.dev/hyphae/internal/db"
	"m31labs.dev/hyphae/internal/recall"
	"m31labs.dev/hyphae/internal/types"
)

// --- fixture helpers --------------------------------------------------------

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "assess.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func mustExec(t *testing.T, conn *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := conn.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// seedInitiative inserts an initiative into both objects and objects_fts so
// the FTS5 typed match in assess finds it.
func seedInitiative(t *testing.T, conn *sql.DB, id, spaceID, title, body, status string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, conn,
		`INSERT INTO objects(id, type, space_id, file_id, status, title, updated_at)
		 VALUES (?, 'initiative', ?, 'file.001', ?, ?, ?)`,
		id, spaceID, status, title, now,
	)
	if err := recall.Index(conn, types.Object{
		ID:      id,
		Type:    types.TypeInitiative,
		SpaceID: spaceID,
		Status:  status,
		Title:   title,
		Body:    body,
		Tags:    []string{"alignment", "reliability"},
	}); err != nil {
		t.Fatalf("recall.Index: %v", err)
	}
}

// --- tests ------------------------------------------------------------------

func TestChange_DirectlyAligned(t *testing.T) {
	conn := openDB(t)

	seedInitiative(t, conn,
		"initiative.billing-reliability",
		"hypha://acme/eng",
		"Billing reliability",
		"Eliminate retry storms and replay duplication in billing. Bounded backoff, webhook retry bounds.",
		"active",
	)

	req := assess.ChangeRequest{
		Task:         "Add bounded backoff to billing-worker webhook retry.",
		ChangedFiles: []string{"services/billing-worker/retry.go"},
		DiffSummary:  "Adds bounded exponential backoff for failed webhook delivery.",
		Space:        "hypha://acme/eng",
	}

	got, err := assess.Change(conn, req)
	if err != nil {
		t.Fatalf("Change: %v", err)
	}

	if got.Alignment != assess.AlignDirectlyAligned {
		t.Errorf("Alignment = %q, want %q", got.Alignment, assess.AlignDirectlyAligned)
	}
	if len(got.MatchedInitiatives) == 0 {
		t.Fatal("MatchedInitiatives empty; expected at least 1")
	}
	if got.MatchedInitiatives[0].ID != "initiative.billing-reliability" {
		t.Errorf("top match = %q, want billing-reliability", got.MatchedInitiatives[0].ID)
	}
	if got.MatchedInitiatives[0].Score < 0.7 {
		t.Errorf("top score = %v, want >= 0.7", got.MatchedInitiatives[0].Score)
	}
	if got.Recommendation != assess.RecProceed {
		t.Errorf("Recommendation = %q, want %q", got.Recommendation, assess.RecProceed)
	}
	if got.TokensUsed == 0 {
		t.Error("TokensUsed = 0, want > 0")
	}
}

func TestChange_NoMatchesNeutral(t *testing.T) {
	conn := openDB(t)

	// Seed an initiative about something totally unrelated.
	seedInitiative(t, conn,
		"initiative.observability",
		"hypha://acme/eng",
		"Observability rollout",
		"Tracing, metrics, structured logging across the platform.",
		"active",
	)

	req := assess.ChangeRequest{
		Task:        "Refactor the marketing landing page CSS gradient.",
		DiffSummary: "Adjusts color stops and easing curve.",
	}

	got, err := assess.Change(conn, req)
	if err != nil {
		t.Fatalf("Change: %v", err)
	}

	if got.Alignment != assess.AlignNeutral {
		t.Errorf("Alignment = %q, want %q", got.Alignment, assess.AlignNeutral)
	}
	if len(got.MatchedInitiatives) != 0 {
		t.Errorf("MatchedInitiatives = %v, want []", got.MatchedInitiatives)
	}
	if got.Recommendation != assess.RecReviewRequired {
		t.Errorf("Recommendation = %q, want %q", got.Recommendation, assess.RecReviewRequired)
	}
}

func TestChange_AdjacentLowScore(t *testing.T) {
	conn := openDB(t)

	// Seed an initiative with weak overlap.
	seedInitiative(t, conn,
		"initiative.observability",
		"hypha://acme/eng",
		"Observability rollout",
		"Tracing, metrics, structured logging.",
		"active",
	)

	req := assess.ChangeRequest{
		Task: "Add a log line to the billing webhook handler.",
	}

	got, err := assess.Change(conn, req)
	if err != nil {
		t.Fatalf("Change: %v", err)
	}

	// Weak overlap → not directly_aligned, but should still match.
	if got.Alignment == assess.AlignDirectlyAligned {
		t.Errorf("Alignment = directly_aligned, want adjacent or enabling")
	}
	if len(got.MatchedInitiatives) == 0 {
		t.Fatal("expected weak match, got none")
	}
}

func TestChange_RejectsEmpty(t *testing.T) {
	conn := openDB(t)
	_, err := assess.Change(conn, assess.ChangeRequest{})
	if err == nil {
		t.Fatal("expected ErrInvalidRequest, got nil")
	}
	if err != assess.ErrInvalidRequest {
		t.Errorf("err = %v, want ErrInvalidRequest", err)
	}
}

func TestChange_HotZoneFromCommonPrefix(t *testing.T) {
	conn := openDB(t)

	seedInitiative(t, conn,
		"initiative.api-stability",
		"hypha://acme/eng",
		"API stability",
		"Reduce breaking changes in the public API.",
		"active",
	)

	// Seed a recent graft receipt so the hot-zone count is > 0.
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, conn,
		`INSERT INTO receipts(id, space_id, subject_id, subject_kind, action, status, created_at)
		 VALUES ('r1', 'hypha://acme/eng', 's1', 'spore', 'graft', 'ok', ?)`,
		now,
	)

	req := assess.ChangeRequest{
		Task: "Stabilize the public API endpoints.",
		ChangedFiles: []string{
			"services/api/handlers/users.go",
			"services/api/handlers/billing.go",
		},
	}

	got, err := assess.Change(conn, req)
	if err != nil {
		t.Fatalf("Change: %v", err)
	}

	if got.HotZone == nil {
		t.Fatal("HotZone = nil, want non-nil for shared-prefix files")
	}
	if got.HotZone.Path != "services/api/handlers" {
		t.Errorf("HotZone.Path = %q, want services/api/handlers", got.HotZone.Path)
	}
	if got.HotZone.Commits14d == 0 {
		t.Error("HotZone.Commits14d = 0, want > 0 (seeded a recent graft)")
	}
}

func TestChange_NoCommonPrefixOmitsHotZone(t *testing.T) {
	conn := openDB(t)

	req := assess.ChangeRequest{
		Task: "Touch unrelated trees.",
		ChangedFiles: []string{
			"services/billing/retry.go",
			"docs/runbook.md",
		},
	}

	got, err := assess.Change(conn, req)
	if err != nil {
		t.Fatalf("Change: %v", err)
	}

	if got.HotZone != nil {
		t.Errorf("HotZone = %+v, want nil (paths share no prefix)", got.HotZone)
	}
}

func TestChange_TopMatchScoreCappedAtOne(t *testing.T) {
	conn := openDB(t)

	seedInitiative(t, conn,
		"initiative.alpha",
		"hypha://acme/eng",
		"Alpha rollout",
		"Initial alpha milestone.",
		"active",
	)

	got, err := assess.Change(conn, assess.ChangeRequest{Task: "alpha rollout milestone"})
	if err != nil {
		t.Fatalf("Change: %v", err)
	}
	if len(got.MatchedInitiatives) == 0 {
		t.Fatal("expected match")
	}
	if got.MatchedInitiatives[0].Score > 1.0 {
		t.Errorf("top Score = %v, want ≤ 1.0", got.MatchedInitiatives[0].Score)
	}
}

func TestChange_InactiveInitiativesExcluded(t *testing.T) {
	conn := openDB(t)

	seedInitiative(t, conn,
		"initiative.retired",
		"hypha://acme/eng",
		"Retired billing reliability push",
		"Bounded backoff, webhook retry bounds.",
		"archived",
	)

	req := assess.ChangeRequest{
		Task: "Add bounded backoff to billing webhook retry.",
	}

	got, err := assess.Change(conn, req)
	if err != nil {
		t.Fatalf("Change: %v", err)
	}
	if len(got.MatchedInitiatives) != 0 {
		t.Errorf("matched archived initiative: %+v", got.MatchedInitiatives)
	}
	if got.Alignment != assess.AlignNeutral {
		t.Errorf("Alignment = %q, want neutral", got.Alignment)
	}
}

// --- smoke -------------------------------------------------------------------

func TestChange_BuildQueryDoesNotPanicOnWeirdPaths(t *testing.T) {
	conn := openDB(t)
	req := assess.ChangeRequest{
		ChangedFiles: []string{"", "  ", "/abs/path.go", "../weird"},
		Task:         "x",
	}
	_, err := assess.Change(conn, req)
	if err != nil {
		// Acceptable: scoring returns empty matches; not acceptable: panic.
		if !strings.Contains(err.Error(), "assess:") {
			t.Errorf("unexpected error shape: %v", err)
		}
	}
}
