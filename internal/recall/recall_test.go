package recall_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/hyphae/internal/db"
	"m31labs.dev/hyphae/internal/recall"
	"m31labs.dev/hyphae/internal/types"
)

func makeObjects() []types.Object {
	now := time.Now()
	return []types.Object{
		{
			ID:      "obj-billing-001",
			Type:    types.TypeConcept,
			SpaceID: "acme/platform",
			Title:   "Billing Webhooks",
			Summary: "How the billing service dispatches webhooks to notify subscribers of payment events.",
			Tags:    []string{"billing", "webhooks", "payments"},
			Body:    "The billing service emits webhook events on invoice creation, payment success, and payment failure. Retries use exponential backoff.",
			UpdatedAt: now,
		},
		{
			ID:      "obj-frontend-001",
			Type:    types.TypeConcept,
			SpaceID: "acme/platform",
			Title:   "Frontend Rendering Pipeline",
			Summary: "Server-side rendering strategy for the main frontend application.",
			Tags:    []string{"frontend", "rendering", "ssr"},
			Body:    "The frontend uses React with SSR via Next.js. Hydration happens after initial paint. Streaming is enabled for large pages.",
			UpdatedAt: now,
		},
		{
			ID:      "obj-auth-001",
			Type:    types.TypeDecision,
			SpaceID: "acme/platform",
			Title:   "Authentication Architecture",
			Summary: "Decision to use JWT tokens with short expiry and refresh token rotation.",
			Tags:    []string{"auth", "jwt", "security"},
			Body:    "We chose JWT with 15-minute expiry. Refresh tokens rotate on each use. Sessions are stored server-side for revocation.",
			UpdatedAt: now,
		},
		{
			ID:      "obj-db-001",
			Type:    types.TypeSpec,
			SpaceID: "acme/platform",
			Title:   "Database Sharding Strategy",
			Summary: "Horizontal sharding approach for the main PostgreSQL cluster.",
			Tags:    []string{"database", "sharding", "postgres"},
			Body:    "Sharding key is the tenant ID. Each shard holds at most 10k tenants. Cross-shard queries are prohibited by contract.",
			UpdatedAt: now,
		},
		{
			ID:      "obj-deploy-001",
			Type:    types.TypePlan,
			SpaceID: "acme/platform",
			Title:   "Kubernetes Deployment Runbook",
			Summary: "Step-by-step runbook for deploying services to the production Kubernetes cluster.",
			Tags:    []string{"kubernetes", "deploy", "ops"},
			Body:    "Rolling updates with max-surge=1, max-unavailable=0. Health checks must pass before traffic shifts. Rollback is automatic on probe failure.",
			UpdatedAt: now,
		},
	}
}

func TestRecall_HappyPath(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer conn.Close()

	objs := makeObjects()
	if err := recall.IndexBatch(conn, objs); err != nil {
		t.Fatalf("IndexBatch: %v", err)
	}

	t.Run("single_match", func(t *testing.T) {
		// "billing webhook" should surface obj-billing-001 as the top hit.
		resp, err := recall.Recall(conn, "billing webhook", 10, types.DefaultBudget())
		if err != nil {
			t.Fatalf("Recall: %v", err)
		}
		if len(resp.Hits) == 0 {
			t.Fatal("expected at least one hit, got none")
		}
		top := resp.Hits[0]
		if !strings.Contains(top.URI, "obj-billing-001") {
			t.Errorf("expected top hit to contain obj-billing-001, got URI=%q title=%q", top.URI, top.Title)
		}
		t.Logf("query=%q top=%q score=%.4f tokensUsed=%d", resp.Query, top.Title, top.Score, resp.TokensUsed)
	})

	t.Run("no_matches", func(t *testing.T) {
		resp, err := recall.Recall(conn, "zzzyyyxxx nonexistent", 10, types.DefaultBudget())
		if err != nil {
			t.Fatalf("Recall: %v", err)
		}
		if len(resp.Hits) != 0 {
			t.Errorf("expected no hits, got %d", len(resp.Hits))
		}
		if !strings.Contains(resp.Summary, "No matches") {
			t.Errorf("expected summary to contain 'No matches', got: %q", resp.Summary)
		}
		t.Logf("no-match summary: %q", resp.Summary)
	})

	t.Run("token_budget_50", func(t *testing.T) {
		budget := types.Budget{MaxResponseTokens: 50, Shape: types.ShapeSummaryAnchors}
		resp, err := recall.Recall(conn, "database postgres sharding", 10, budget)
		if err != nil {
			t.Fatalf("Recall: %v", err)
		}
		if resp.TokensUsed > 50 {
			t.Errorf("TokensUsed=%d exceeds budget of 50", resp.TokensUsed)
		}
		// If there were matches, at least 1 hit must survive.
		if len(resp.Hits) == 0 && strings.Contains(resp.Summary, "Found") {
			t.Error("budget trim removed all hits; must keep at least 1")
		}
		t.Logf("budget=50 tokensUsed=%d hits=%d", resp.TokensUsed, len(resp.Hits))
	})
}

func TestRecall_Index_Idempotent(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer conn.Close()

	obj := makeObjects()[0] // billing webhooks

	// Index twice — should not error or duplicate.
	if err := recall.Index(conn, obj); err != nil {
		t.Fatalf("first Index: %v", err)
	}
	if err := recall.Index(conn, obj); err != nil {
		t.Fatalf("second Index (idempotent): %v", err)
	}

	resp, err := recall.Recall(conn, "billing", 10, types.DefaultBudget())
	if err != nil {
		t.Fatalf("Recall after double-index: %v", err)
	}
	if len(resp.Hits) != 1 {
		t.Errorf("expected exactly 1 hit after idempotent re-index, got %d", len(resp.Hits))
	}
}

func TestRecall_ShapeHeadline(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer conn.Close()

	if err := recall.IndexBatch(conn, makeObjects()); err != nil {
		t.Fatalf("IndexBatch: %v", err)
	}

	budget := types.Budget{MaxResponseTokens: 800, Shape: types.ShapeHeadline}
	resp, err := recall.Recall(conn, "frontend rendering", 10, budget)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(resp.Hits) > 1 {
		t.Errorf("headline shape: expected at most 1 hit, got %d", len(resp.Hits))
	}
	t.Logf("headline resp: summary=%q hits=%d", resp.Summary, len(resp.Hits))
}

func TestRecall_ShapeFullDocuments_Unsupported(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer conn.Close()

	budget := types.Budget{MaxResponseTokens: 800, Shape: types.ShapeFullDocuments}
	_, err = recall.Recall(conn, "anything", 10, budget)
	if err == nil {
		t.Error("expected error for ShapeFullDocuments, got nil")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Errorf("expected 'not supported' in error, got: %v", err)
	}
}
