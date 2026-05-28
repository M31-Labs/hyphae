package syncbundle_test

import (
	"testing"

	"m31labs.dev/gosx/crdt"
	"m31labs.dev/hyphae/internal/syncbundle"
)

func TestExportImportRoundTrip(t *testing.T) {
	sender := crdt.NewDoc()
	_ = sender.Put(crdt.Root, "k1", crdt.StringValue("v1"))
	_, _ = sender.Commit("c1")
	_ = sender.Put(crdt.Root, "k2", crdt.StringValue("v2"))
	_, _ = sender.Commit("c2")

	b, err := syncbundle.Export(sender, "hypha://test/space")
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	raw, err := b.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	b2, err := syncbundle.Unmarshal(raw)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if b2.Space != "hypha://test/space" {
		t.Errorf("space mismatch: got %q", b2.Space)
	}

	receiver := crdt.NewDoc()
	n, err := syncbundle.Import(receiver, b2)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if n != 2 {
		t.Errorf("Import absorbed %d changes, want 2", n)
	}

	// Verify the receiver now has both keys.
	val, _, err := receiver.Get(crdt.Root, "k1")
	if err != nil || val.Str != "v1" {
		t.Errorf("k1 after import = %v %q", err, val.Str)
	}
	val, _, err = receiver.Get(crdt.Root, "k2")
	if err != nil || val.Str != "v2" {
		t.Errorf("k2 after import = %v %q", err, val.Str)
	}

	// Idempotent re-import.
	again, err := syncbundle.Import(receiver, b2)
	if err != nil {
		t.Fatalf("re-Import: %v", err)
	}
	if again != 0 {
		t.Errorf("re-import should absorb 0, got %d", again)
	}
}

func TestUnmarshalRejectsBadVersion(t *testing.T) {
	bad := []byte(`{"version":99,"space":"x","message_b64":"AA=="}`)
	if _, err := syncbundle.Unmarshal(bad); err == nil {
		t.Error("expected version mismatch error")
	}
}
