// Package syncbundle is the on-disk wire format for Hyphae's
// file-based federation (Phase 3 of spec.real-time-federation-via-crdt).
//
// A bundle is a self-describing JSON envelope wrapping a base64-encoded
// gosx sync.Message. Carries enough metadata for the receiver to verify
// what space it's importing into and reject mismatches.
//
//	{
//	  "version": 1,
//	  "space": "hypha://myorg/knowledge",
//	  "exported_at": "2026-05-28T08:30:00Z",
//	  "from_actor": "abc123…",
//	  "from_heads": ["hash1", "hash2"],
//	  "message_b64": "<base64 sync.Message bytes>"
//	}
//
// The payload is a gosx sync.Message containing every Change the
// exporter has (v0.2 ships "send everything"; --since trims the
// content in a later iteration). ReceiveSyncMessage is idempotent so
// re-importing a bundle is safe.
package syncbundle

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"m31labs.dev/gosx/crdt"
	crdtsync "m31labs.dev/gosx/crdt/sync"
)

// Version is the on-disk envelope schema.
const Version = 1

// Bundle is the wire format.
type Bundle struct {
	Version    int       `json:"version"`
	Space      string    `json:"space"`
	ExportedAt time.Time `json:"exported_at"`
	FromActor  string    `json:"from_actor,omitempty"`
	FromHeads  []string  `json:"from_heads,omitempty"`
	MessageB64 string    `json:"message_b64"`
}

// Export packages every change in doc into a Bundle for spaceURI.
// The whole local history is shipped; receivers idempotently absorb
// only what they don't already have.
func Export(doc *crdt.Doc, spaceURI string) (*Bundle, error) {
	state := crdtsync.NewState()
	msg, ok := doc.GenerateSyncMessage(state)
	if !ok || msg == nil {
		// Empty Doc — emit an empty bundle so the receiver gets a
		// well-formed file rather than failing.
		empty, err := crdtsync.EncodeMessage(crdtsync.Message{Version: crdtsync.MessageTypeV1})
		if err != nil {
			return nil, fmt.Errorf("syncbundle: encode empty message: %w", err)
		}
		msg = empty
	}
	decoded, _ := crdtsync.DecodeMessage(msg)
	heads := make([]string, 0, len(decoded.Heads))
	for _, h := range decoded.Heads {
		heads = append(heads, fmt.Sprintf("%x", h[:]))
	}
	return &Bundle{
		Version:    Version,
		Space:      spaceURI,
		ExportedAt: time.Now().UTC(),
		FromActor:  doc.ActorID().String(),
		FromHeads:  heads,
		MessageB64: base64.StdEncoding.EncodeToString(msg),
	}, nil
}

// Marshal serializes the bundle as indented JSON.
func (b *Bundle) Marshal() ([]byte, error) {
	return json.MarshalIndent(b, "", "  ")
}

// Unmarshal parses bundle bytes (file contents) into a Bundle.
func Unmarshal(data []byte) (*Bundle, error) {
	var b Bundle
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("syncbundle: parse: %w", err)
	}
	if b.Version != Version {
		return nil, fmt.Errorf("syncbundle: unsupported version %d (want %d)", b.Version, Version)
	}
	if b.MessageB64 == "" {
		return nil, fmt.Errorf("syncbundle: empty message_b64")
	}
	return &b, nil
}

// Import applies every change in the bundle to doc. Returns the count
// of newly-absorbed changes. Idempotent: re-importing the same bundle
// is a no-op.
func Import(doc *crdt.Doc, b *Bundle) (int, error) {
	if b == nil {
		return 0, fmt.Errorf("syncbundle: nil bundle")
	}
	msg, err := base64.StdEncoding.DecodeString(b.MessageB64)
	if err != nil {
		return 0, fmt.Errorf("syncbundle: decode message_b64: %w", err)
	}

	// Count changes the doc didn't have before import.
	preState := crdtsync.NewState()
	preMsg, _ := doc.GenerateSyncMessage(preState)
	preCount := 0
	if preMsg != nil {
		dec, _ := crdtsync.DecodeMessage(preMsg)
		preCount = len(dec.Changes)
	}

	state := crdtsync.NewState()
	if err := doc.ReceiveSyncMessage(state, msg); err != nil {
		return 0, fmt.Errorf("syncbundle: receive: %w", err)
	}

	postState := crdtsync.NewState()
	postMsg, _ := doc.GenerateSyncMessage(postState)
	postCount := 0
	if postMsg != nil {
		dec, _ := crdtsync.DecodeMessage(postMsg)
		postCount = len(dec.Changes)
	}
	delta := postCount - preCount
	if delta < 0 {
		delta = 0
	}
	return delta, nil
}
