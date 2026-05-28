// Package crdtdb is the per-space append-only SQLite change log for the
// CRDT shadow (Phase 2 of spec.real-time-federation-via-crdt).
//
// Each space gets one DB at <space-root>/.crdt.db. The schema is the
// minimal "changes since hash X" primitive the sync protocol wants:
//
//	crdt_changes      (hash, actor_id, seq, start_op, time, message, ops_blob)
//	crdt_change_deps  (change_hash, dep_hash)
//	crdt_heads        (hash)
//	crdt_snapshots    (at_change_hash, taken_at, blob) — optional compaction
//
// The DB is the canonical history; the materialized .md files under
// the space and the .index/hyphae.db FTS index are both derived from it.
package crdtdb

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"m31labs.dev/gosx/crdt"
	crdtsync "m31labs.dev/gosx/crdt/sync"
	"m31labs.dev/hyphae/internal/db"
)

// Store wraps the SQLite change log for one space.
type Store struct {
	conn *sql.DB
	path string
}

// Open opens (or creates) the change log at path. Idempotent — safe
// to call against an existing DB.
func Open(path string) (*Store, error) {
	conn, err := db.OpenRaw(path)
	if err != nil {
		return nil, fmt.Errorf("crdtdb: open %s: %w", path, err)
	}
	if err := migrate(conn); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("crdtdb: migrate: %w", err)
	}
	return &Store{conn: conn, path: path}, nil
}

// Close closes the underlying SQLite handle.
func (s *Store) Close() error { return s.conn.Close() }

// Path returns the on-disk path of the DB file.
func (s *Store) Path() string { return s.path }

// CountChanges returns how many change rows the log holds.
func (s *Store) CountChanges() (int, error) {
	var n int
	err := s.conn.QueryRow(`SELECT COUNT(*) FROM crdt_changes`).Scan(&n)
	return n, err
}

// CountSnapshots returns how many compaction snapshots the log holds.
func (s *Store) CountSnapshots() (int, error) {
	var n int
	err := s.conn.QueryRow(`SELECT COUNT(*) FROM crdt_snapshots`).Scan(&n)
	return n, err
}

// KnownHashes returns every change hash currently stored. Used to seed
// a sync.State so GenerateSyncMessage skips already-stored changes.
func (s *Store) KnownHashes() (map[[32]byte]struct{}, error) {
	rows, err := s.conn.Query(`SELECT hash FROM crdt_changes`)
	if err != nil {
		return nil, fmt.Errorf("crdtdb: select hashes: %w", err)
	}
	defer rows.Close()
	known := make(map[[32]byte]struct{})
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if len(raw) != 32 {
			continue
		}
		var h [32]byte
		copy(h[:], raw)
		known[h] = struct{}{}
	}
	return known, rows.Err()
}

// AppendChangesFromDoc extracts every change in doc not already stored
// and appends it (atomically inside one transaction). Idempotent.
//
// Implementation: builds a sync.State pre-marked with known hashes,
// asks the Doc for its sync message (which skips known), decodes the
// message back into per-change chunks, and inserts each.
func (s *Store) AppendChangesFromDoc(doc *crdt.Doc) (int, error) {
	known, err := s.KnownHashes()
	if err != nil {
		return 0, err
	}
	state := crdtsync.NewState()
	for h := range known {
		state.MarkSent(h)
	}

	msg, ok := doc.GenerateSyncMessage(state)
	if !ok || msg == nil {
		return 0, nil
	}
	decoded, err := crdtsync.DecodeMessage(msg)
	if err != nil {
		return 0, fmt.Errorf("crdtdb: decode sync message: %w", err)
	}
	if len(decoded.Changes) == 0 {
		return 0, nil
	}

	tx, err := s.conn.Begin()
	if err != nil {
		return 0, fmt.Errorf("crdtdb: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	inserted := 0
	for _, chunk := range decoded.Changes {
		change, err := crdt.DecodeChangeChunk(chunk)
		if err != nil {
			return inserted, fmt.Errorf("crdtdb: decode change chunk: %w", err)
		}
		hash := [32]byte(change.Hash)
		if _, exists := known[hash]; exists {
			continue
		}
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO crdt_changes
				(hash, actor_id, seq, start_op, time, message, ops_blob)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			hash[:], change.ActorID, change.Seq, change.StartOp,
			change.Time.UTC().UnixNano(), change.Message, chunk,
		); err != nil {
			return inserted, fmt.Errorf("crdtdb: insert change %s: %w", hex.EncodeToString(hash[:8]), err)
		}
		for _, dep := range change.Deps {
			depBytes := [32]byte(dep)
			if _, err := tx.Exec(`
				INSERT OR IGNORE INTO crdt_change_deps (change_hash, dep_hash)
				VALUES (?, ?)`, hash[:], depBytes[:]); err != nil {
				return inserted, fmt.Errorf("crdtdb: insert dep: %w", err)
			}
		}
		inserted++
	}

	// Refresh heads table: drop all + insert current Doc heads.
	if _, err := tx.Exec(`DELETE FROM crdt_heads`); err != nil {
		return inserted, fmt.Errorf("crdtdb: clear heads: %w", err)
	}
	for _, head := range decoded.Heads {
		h := head // copy
		if _, err := tx.Exec(`INSERT INTO crdt_heads (hash) VALUES (?)`, h[:]); err != nil {
			return inserted, fmt.Errorf("crdtdb: insert head: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return inserted, fmt.Errorf("crdtdb: commit: %w", err)
	}
	return inserted, nil
}

// LoadAllInto loads every stored change into doc by routing the
// encoded chunks through ReceiveSyncMessage. Order is by (start_op,
// hash) so dependency ordering is respected.
func (s *Store) LoadAllInto(doc *crdt.Doc) (int, error) {
	rows, err := s.conn.Query(`
		SELECT ops_blob FROM crdt_changes
		ORDER BY start_op, hex(hash)`)
	if err != nil {
		return 0, fmt.Errorf("crdtdb: select all changes: %w", err)
	}
	defer rows.Close()

	var chunks [][]byte
	for rows.Next() {
		var blob []byte
		if err := rows.Scan(&blob); err != nil {
			return 0, err
		}
		chunks = append(chunks, blob)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(chunks) == 0 {
		return 0, nil
	}

	// Re-package the chunks into a sync Message and let the Doc absorb
	// them via the normal receive path (handles missing-dep ordering).
	msg, err := crdtsync.EncodeMessage(crdtsync.Message{
		Version: crdtsync.MessageTypeV1,
		Changes: chunks,
	})
	if err != nil {
		return 0, fmt.Errorf("crdtdb: encode replay msg: %w", err)
	}
	state := crdtsync.NewState()
	if err := doc.ReceiveSyncMessage(state, msg); err != nil {
		return 0, fmt.Errorf("crdtdb: receive replay msg: %w", err)
	}
	return len(chunks), nil
}

// HistoryRow is one change-log entry returned by History.
type HistoryRow struct {
	Hash    string    `json:"hash"`
	ActorID string    `json:"actor"`
	Seq     uint64    `json:"seq"`
	StartOp uint64    `json:"start_op"`
	Time    time.Time `json:"time"`
	Message string    `json:"message,omitempty"`
}

// History returns the most recent N rows (newest first by time, then seq).
// limit ≤ 0 returns everything.
func (s *Store) History(limit int) ([]HistoryRow, error) {
	q := `SELECT hash, actor_id, seq, start_op, time, message FROM crdt_changes ORDER BY time DESC, seq DESC`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.conn.Query(q)
	if err != nil {
		return nil, fmt.Errorf("crdtdb: history: %w", err)
	}
	defer rows.Close()
	var out []HistoryRow
	for rows.Next() {
		var (
			hash    []byte
			actor   string
			seq     uint64
			startOp uint64
			tNanos  int64
			msg     string
		)
		if err := rows.Scan(&hash, &actor, &seq, &startOp, &tNanos, &msg); err != nil {
			return nil, err
		}
		out = append(out, HistoryRow{
			Hash:    hex.EncodeToString(hash),
			ActorID: actor,
			Seq:     seq,
			StartOp: startOp,
			Time:    time.Unix(0, tNanos).UTC(),
			Message: msg,
		})
	}
	return out, rows.Err()
}

// Heads returns the current frontier change hashes (hex-encoded).
func (s *Store) Heads() ([]string, error) {
	rows, err := s.conn.Query(`SELECT hash FROM crdt_heads`)
	if err != nil {
		return nil, fmt.Errorf("crdtdb: heads: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var hash []byte
		if err := rows.Scan(&hash); err != nil {
			return nil, err
		}
		out = append(out, hex.EncodeToString(hash))
	}
	return out, rows.Err()
}

// Compact takes a fresh Doc snapshot and stores it in crdt_snapshots,
// optionally pruning crdt_changes rows whose hash is <= the snapshot
// boundary. For Phase 2 we keep all changes; pruning is a Phase 2.5
// add-on once we have a clear "what's the boundary" story.
func (s *Store) Compact(doc *crdt.Doc) error {
	blob, err := doc.Save()
	if err != nil {
		return fmt.Errorf("crdtdb: doc.Save: %w", err)
	}
	heads, err := s.Heads()
	if err != nil {
		return err
	}
	var boundary string
	if len(heads) > 0 {
		boundary = heads[0]
	}
	if _, err := s.conn.Exec(`
		INSERT OR IGNORE INTO crdt_snapshots (at_change_hash, taken_at, blob)
		VALUES (?, ?, ?)`, boundary, time.Now().UTC().UnixNano(), blob); err != nil {
		return fmt.Errorf("crdtdb: snapshot insert: %w", err)
	}
	return nil
}

// migrate creates the schema on a fresh DB. Idempotent.
func migrate(conn *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS crdt_changes (
			hash       BLOB PRIMARY KEY,
			actor_id   TEXT NOT NULL,
			seq        INTEGER NOT NULL,
			start_op   INTEGER NOT NULL,
			time       INTEGER NOT NULL,
			message    TEXT,
			ops_blob   BLOB NOT NULL,
			UNIQUE(actor_id, seq)
		)`,
		`CREATE TABLE IF NOT EXISTS crdt_change_deps (
			change_hash BLOB NOT NULL,
			dep_hash    BLOB NOT NULL,
			PRIMARY KEY (change_hash, dep_hash)
		)`,
		`CREATE TABLE IF NOT EXISTS crdt_heads (
			hash BLOB PRIMARY KEY
		)`,
		`CREATE TABLE IF NOT EXISTS crdt_snapshots (
			at_change_hash TEXT PRIMARY KEY,
			taken_at       INTEGER NOT NULL,
			blob           BLOB NOT NULL
		)`,
	}
	for _, q := range stmts {
		if _, err := conn.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// ErrNoSnapshot signals there's no compaction snapshot to read.
var ErrNoSnapshot = errors.New("crdtdb: no snapshot")
