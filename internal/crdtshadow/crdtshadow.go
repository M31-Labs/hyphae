// Package crdtshadow mirrors every state-changing Hyphae operation
// (receipt write, spore submit / status flip, trace open / tick / done,
// graft canonical write, edge persist) into a per-space gosx CRDT Doc.
//
// The Doc is the substrate for v0.2 federation (see
// spec.real-time-federation-via-crdt). Phase 1 (this file) snapshots the
// full Doc to <space-root>/.crdt.dat after every recorded operation;
// Phase 2 replaces that with an append-only SQLite change log.
//
// Top-level Doc layout:
//
//	Root (map)
//	├── receipts  (map[receipt_id]  → bytes (json receipt blob))
//	├── edges     (map[edge_id]     → bytes (json edge blob))
//	├── spores    (map[spore_id]    → bytes (json spore-summary blob))
//	├── traces    (map[trace_id]    → bytes (json trace blob))
//	└── canonical (map[file_path]   → bytes (file contents; Phase 5 makes this block-level))
//
// JSON encoding for Phase 1 is a pragmatic choice — easy to inspect,
// trivially LWW on full replace. Phase 2/5 can move to richer CRDT
// composition (LWW registers per field, RGA lists for tick streams,
// block-level Text for canonical bodies).
package crdtshadow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"m31labs.dev/gosx/crdt"
	"m31labs.dev/hyphae/internal/crdtdb"
	"m31labs.dev/hyphae/internal/types"
)

// Top-level Doc key prefixes. Every record is a flat Put on Root
// under "<prefix>\x00<key>" because gosx's MakeMap is LWW-compared
// across actors — two peers independently MakeMap'ing the same slot
// would orphan one side's children. Flattening sidesteps the
// problem: each Put competes only with the same key from another
// peer, and same-key edits become explicit per-key conflicts (which
// is what we want — Phase 6 surfaces these).
const (
	prefixReceipts  = "receipts"
	prefixEdges     = "edges"
	prefixSpores    = "spores"
	prefixTraces    = "traces"
	prefixCanonical = "canonical"
)

// LegacySnapshotFilename is the Phase 1 per-space snapshot file. Kept
// for one-time migration into the Phase 2 SQLite change log.
const LegacySnapshotFilename = ".crdt.dat"

// SnapshotFilename is the historical name still exported for tests
// that introspect on-disk shape. New code should prefer DBFilename.
const SnapshotFilename = LegacySnapshotFilename

// DBFilename is the per-space append-only SQLite change log (Phase 2).
const DBFilename = ".crdt.db"

// Shadow owns one gosx Doc for one Hyphae space. Concurrent-safe.
type Shadow struct {
	mu        sync.Mutex
	spaceRoot string
	spaceID   string
	doc       *crdt.Doc
	store     *crdtdb.Store
	snapPath  string // legacy .crdt.dat path; retained for the migration
	dbPath    string
	closed    bool
}

// flatKey builds the canonical Root-level key for a prefix + payload key.
func flatKey(prefix, key string) string { return prefix + "\x00" + key }

// parseFlatKey splits a flat Root key back into (prefix, key). Returns
// ok=false when the key does not match the flat-key shape.
func parseFlatKey(k string) (prefix, key string, ok bool) {
	idx := strings.IndexByte(k, '\x00')
	if idx < 0 {
		return "", "", false
	}
	return k[:idx], k[idx+1:], true
}

// Open loads (or creates) the Shadow for a space. spaceRoot is the
// absolute directory of the space; spaceID is the canonical
// hypha://authority/name URI.
//
// Phase 2 persistence: the per-space change log lives at
// <spaceRoot>/.crdt.db. A legacy <spaceRoot>/.crdt.dat snapshot from
// Phase 1 is auto-migrated on first open and the .dat file removed.
func Open(spaceRoot, spaceID string) (*Shadow, error) {
	if spaceRoot == "" {
		return nil, fmt.Errorf("crdtshadow: spaceRoot is required")
	}

	dbPath := filepath.Join(spaceRoot, DBFilename)
	legacy := filepath.Join(spaceRoot, LegacySnapshotFilename)

	store, err := crdtdb.Open(dbPath)
	if err != nil {
		return nil, err
	}

	doc := crdt.NewDoc()
	if _, err := store.LoadAllInto(doc); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("crdtshadow: replay log: %w", err)
	}

	// One-time migration: if the legacy .dat exists, load it as a Doc,
	// merge into the current Doc, append the merged changes into the
	// store, then remove the .dat so the migration is idempotent.
	if data, rerr := readIfExists(legacy); rerr == nil && data != nil {
		legacyDoc, lerr := crdt.Load(data)
		if lerr != nil {
			_ = store.Close()
			return nil, fmt.Errorf("crdtshadow: load legacy .dat: %w", lerr)
		}
		if err := doc.Merge(legacyDoc); err != nil {
			_ = store.Close()
			return nil, fmt.Errorf("crdtshadow: merge legacy: %w", err)
		}
		if _, err := store.AppendChangesFromDoc(doc); err != nil {
			_ = store.Close()
			return nil, fmt.Errorf("crdtshadow: persist legacy: %w", err)
		}
		_ = os.Remove(legacy) // best-effort; idempotent on next open
	}

	s := &Shadow{
		spaceRoot: spaceRoot,
		spaceID:   spaceID,
		doc:       doc,
		store:     store,
		snapPath:  legacy,
		dbPath:    dbPath,
	}
	if err := s.bootstrap(); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("crdtshadow: bootstrap: %w", err)
	}
	return s, nil
}

// Doc returns the underlying CRDT Doc. Use for hub.SyncDoc registration
// in Phase 4 and inspection in tests.
func (s *Shadow) Doc() *crdt.Doc {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.doc
}

// SpaceID returns the space URI this shadow tracks.
func (s *Shadow) SpaceID() string { return s.spaceID }

// SpaceRoot returns the on-disk root this shadow tracks.
func (s *Shadow) SpaceRoot() string { return s.spaceRoot }

// SnapshotPath returns the legacy .crdt.dat path. Kept for tests that
// inspect on-disk shape; new code should prefer DBPath.
func (s *Shadow) SnapshotPath() string { return s.snapPath }

// DBPath returns the SQLite change-log path (<spaceRoot>/.crdt.db).
func (s *Shadow) DBPath() string { return s.dbPath }

// Store returns the underlying append-only change log for inspection
// (history, heads, compaction). Read-only operations are safe; mutating
// the store outside Shadow.Record* is not.
func (s *Shadow) Store() *crdtdb.Store { return s.store }

// Close persists pending state and closes the underlying DB handle.
// Safe to call multiple times.
func (s *Shadow) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if err := s.persistLocked(true); err != nil {
		_ = s.store.Close()
		return err
	}
	return s.store.Close()
}

// RecordReceipt mirrors a Receipt into the CRDT.
func (s *Shadow) RecordReceipt(r types.Receipt) error {
	if r.ID == "" {
		return fmt.Errorf("crdtshadow: receipt missing id")
	}
	return s.putBlobRoot(prefixReceipts, r.ID, r, "receipt:"+r.ID)
}

// RecordEdge mirrors an Edge into the CRDT.
func (s *Shadow) RecordEdge(e types.Edge) error {
	if e.ID == "" {
		return fmt.Errorf("crdtshadow: edge missing id")
	}
	return s.putBlobRoot(prefixEdges, e.ID, e, "edge:"+e.ID)
}

// SporeSummary is the compact, federation-friendly view of a spore
// stored in the CRDT. The full spore body stays on disk under
// inbox/agents/; the shadow holds only the metadata needed for
// status flips and inbox enumeration. Phase 5 may expand this.
type SporeSummary struct {
	ID          string `json:"id"`
	SpaceID     string `json:"space"`
	Status      string `json:"status"`
	Path        string `json:"path"`
	ContentHash string `json:"content_hash,omitempty"`
	AgentID     string `json:"agent_id,omitempty"`
	SubmittedAt string `json:"submitted_at,omitempty"`
}

// RecordSpore mirrors a spore submission (or a status update) into the
// CRDT. Idempotent: re-recording the same spore with the same status
// is a no-op LWW replace.
func (s *Shadow) RecordSpore(sum SporeSummary) error {
	if sum.ID == "" {
		return fmt.Errorf("crdtshadow: spore missing id")
	}
	return s.putBlobRoot(prefixSpores, sum.ID, sum, "spore:"+sum.ID)
}

// RecordSporeStatus is a convenience for the spore-review path that
// only knows the spore id and the new status; preserves any earlier
// summary fields by reading-then-writing.
func (s *Shadow) RecordSporeStatus(sporeID, newStatus string) error {
	if sporeID == "" {
		return fmt.Errorf("crdtshadow: spore id required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var sum SporeSummary
	if existing, ok := s.readBytesLocked(crdt.Root, flatKey(prefixSpores, sporeID)); ok {
		_ = json.Unmarshal(existing, &sum)
	}
	if sum.ID == "" {
		sum.ID = sporeID
	}
	sum.Status = newStatus
	return s.putBlobRootLocked(prefixSpores, sporeID, sum, "spore-status:"+sporeID)
}

// TraceSummary is the compact federation-friendly view of a trace.
// Ticks are stored as a slice in JSON for Phase 1; Phase 5 may
// migrate to an RGA list per trace.
type TraceSummary struct {
	ID          string        `json:"id"`
	SpaceID     string        `json:"space"`
	AgentID     string        `json:"agent_id"`
	Status      string        `json:"status"`
	TaskRef     string        `json:"task_ref,omitempty"`
	Phase       string        `json:"phase,omitempty"`
	Started     string        `json:"started"`
	LastTick    string        `json:"last_tick"`
	Ticks       []TickSummary `json:"ticks,omitempty"`
	LinkedSpore string        `json:"linked_spore,omitempty"`
	FilePath    string        `json:"file_path,omitempty"`
}

// TickSummary is one checkpoint inside a TraceSummary.
type TickSummary struct {
	At      string `json:"at"`
	Message string `json:"message"`
}

// RecordTrace mirrors a trace (open / tick / done) into the CRDT.
// Idempotent: every call replaces the entry under its trace id.
func (s *Shadow) RecordTrace(t TraceSummary) error {
	if t.ID == "" {
		return fmt.Errorf("crdtshadow: trace missing id")
	}
	return s.putBlobRoot(prefixTraces, t.ID, t, "trace:"+t.ID)
}

// ResolveConflict writes value to the flat key, producing a fresh
// Put op that wins LWW over every existing entry. Used by
// `hypha conflict resolve`. If the key is a canonical section,
// MaterializeAll afterwards re-renders the file on disk.
func (s *Shadow) ResolveConflict(key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.doc.Put(crdt.Root, crdt.Prop(key), crdt.BytesValue(append([]byte{}, value...))); err != nil {
		return fmt.Errorf("crdtshadow: resolve put: %w", err)
	}
	if _, err := s.doc.Commit("conflict-resolve:" + key); err != nil {
		return fmt.Errorf("crdtshadow: resolve commit: %w", err)
	}
	return s.persistLocked(false)
}

// RecordCanonicalWrite mirrors the post-write contents of a canonical
// file. Phase 5: decomposes the file into per-section CRDT entries
// (frontmatter + preamble + one Bytes entry per heading slug) so that
// independent-region grafts auto-merge structurally on sync.
//
// path should be the absolute file path; the shadow stores it relative
// to spaceRoot so multi-machine replicas can rebase paths.
func (s *Shadow) RecordCanonicalWrite(path string, after []byte) error {
	rel, err := filepath.Rel(s.spaceRoot, path)
	if err != nil {
		// Fall back to the absolute path; better than dropping the write.
		rel = path
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recordCanonicalSectioned(rel, after)
}

// ─── internals ────────────────────────────────────────────────────────────

// bootstrap is now a no-op: the Shadow stores everything under Root
// with flat-key namespacing, so there's no per-category sub-map to
// pre-create. Kept as a hook for future migrations.
func (s *Shadow) bootstrap() error { return nil }

func (s *Shadow) putBlobRoot(prefix, key string, payload any, msg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.putBlobRootLocked(prefix, key, payload, msg)
}

func (s *Shadow) putBlobRootLocked(prefix, key string, payload any, msg string) error {
	blob, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("crdtshadow: marshal %s: %w", msg, err)
	}
	if err := s.doc.Put(crdt.Root, crdt.Prop(flatKey(prefix, key)), crdt.BytesValue(blob)); err != nil {
		return fmt.Errorf("crdtshadow: put %s: %w", msg, err)
	}
	if _, err := s.doc.Commit(msg); err != nil {
		return fmt.Errorf("crdtshadow: commit %s: %w", msg, err)
	}
	return s.persistLocked(false)
}

// persistLocked flushes any uncommitted ops to disk via the change log.
// Caller must hold s.mu. Idempotent — appending an already-stored
// change is a no-op (sync.State suppresses it).
func (s *Shadow) persistLocked(_ bool) error {
	if s.store == nil {
		return nil
	}
	if _, err := s.store.AppendChangesFromDoc(s.doc); err != nil {
		return fmt.Errorf("crdtshadow: append changes: %w", err)
	}
	return nil
}

