package peers_test

import (
	"testing"

	"m31labs.dev/hyphae/internal/peers"
)

func TestAddListRemove(t *testing.T) {
	root := t.TempDir()

	if list, err := peers.List(root); err != nil {
		t.Fatalf("initial List: %v", err)
	} else if len(list) != 0 {
		t.Errorf("expected empty list, got %d", len(list))
	}

	p, err := peers.Add(root, "kube", "tailscale://kube.tailnet")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if p.Name != "kube" || p.URI != "tailscale://kube.tailnet" {
		t.Errorf("Add returned wrong peer: %+v", p)
	}
	if p.AddedAt.IsZero() {
		t.Error("AddedAt should be set")
	}

	// Re-add same URI → error.
	if _, err := peers.Add(root, "another", "tailscale://kube.tailnet"); err == nil {
		t.Error("re-add same URI should fail")
	}
	if _, err := peers.Add(root, "kube", "tailscale://other.tailnet"); err == nil {
		t.Error("re-add same name should fail")
	}

	if _, err := peers.Add(root, "", "https://laptop2:7777"); err != nil {
		t.Fatalf("Add with derived name: %v", err)
	}

	list, err := peers.List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("expected 2 peers, got %d", len(list))
	}

	// Remove by URI.
	removed, err := peers.Remove(root, "tailscale://kube.tailnet")
	if err != nil {
		t.Fatalf("Remove by URI: %v", err)
	}
	if removed.Name != "kube" {
		t.Errorf("Remove returned %+v, want kube", removed)
	}

	// Remove by name.
	if _, err := peers.Remove(root, "laptop2"); err != nil {
		t.Fatalf("Remove by name: %v", err)
	}

	// Now empty.
	list, _ = peers.List(root)
	if len(list) != 0 {
		t.Errorf("expected empty after removes, got %d", len(list))
	}

	// Remove missing → error.
	if _, err := peers.Remove(root, "missing"); err == nil {
		t.Error("Remove missing should fail")
	}
}
