package indexer

import (
	"os"
	"path/filepath"
	"testing"

	"m31labs.dev/hyphae/internal/db"
	"m31labs.dev/hyphae/internal/recall"
	"m31labs.dev/hyphae/internal/types"
)

func TestPromoteFileIndexesCanonicalObject(t *testing.T) {
	root := t.TempDir()
	spaceRoot := filepath.Join(root, "spaces", "acme-eng")
	path := filepath.Join(spaceRoot, "specs", "honesty-gate.md")
	writeFile(t, path, `---
id: spec.acme.honesty-gate
type: spec
space: hypha://acme/eng
status: canonical
tags: [honesty-gate]
---

# Honesty Gate

Backend selection must fail closed instead of silently degrading.
`)

	conn, err := db.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer conn.Close()

	p, err := PromoteFile(conn, spaceRoot, "hypha://acme/eng", path)
	if err != nil {
		t.Fatalf("PromoteFile: %v", err)
	}
	if p.Object.ID != "spec.acme.honesty-gate" {
		t.Fatalf("object id = %q", p.Object.ID)
	}

	resp, err := recall.Recall(conn, "honesty backend", 10, types.DefaultBudget())
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(resp.Hits) == 0 {
		t.Fatalf("expected promoted object to be recall-able")
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}
