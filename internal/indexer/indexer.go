// Package indexer promotes canonical Markdown files into the SQLite index.
package indexer

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"m31labs.dev/hyphae/internal/parser"
	"m31labs.dev/hyphae/internal/recall"
	"m31labs.dev/hyphae/internal/types"
)

const DefaultBatchSize = 64

// Promotion reports the rows derived from one canonical file.
type Promotion struct {
	Object  types.Object
	Anchors int
	Edges   int
}

// RebuildStats describes a bounded space rebuild.
type RebuildStats struct {
	Objects        int           `json:"objects"`
	Anchors        int           `json:"anchors"`
	Edges          int           `json:"edges"`
	MarkdownSeen   int           `json:"markdown_seen"`
	FilesParsed    int           `json:"files_parsed"`
	FilesSkipped   int           `json:"files_skipped"`
	DirsSkipped    int           `json:"dirs_skipped"`
	BytesRead      int64         `json:"bytes_read"`
	Skips          []parser.Skip `json:"skips,omitempty"`
	SkipsTruncated bool          `json:"skips_truncated,omitempty"`
}

// RebuildSpace streams one space through the parser and index in bounded
// batches. It never stores the whole space in memory.
//
// RebuildSpace is authoritative for the parser-derived rows of a space: after
// it returns, the objects/anchors/parser-edges/FTS rows for that space reflect
// exactly the Markdown files currently on disk. Objects whose source file was
// renamed or deleted are pruned (see PruneMissing), so `index rebuild` never
// leaves phantom rows that resolve via `hypha show`/`recall`.
func RebuildSpace(conn *sql.DB, spaceRoot, spaceID string, walkOpts parser.WalkOptions, batchSize int) (RebuildStats, error) {
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}
	normSpaceID := normalizeSpaceID(spaceID)

	var objects []types.Object
	var anchors []types.Anchor
	var edges []types.Edge
	// seen tracks every object id promoted from a file that still exists on
	// disk, so PruneMissing can drop rows whose source file is gone.
	seen := make(map[string]struct{})
	flush := func() error {
		if len(objects) == 0 {
			return nil
		}
		if err := recall.IndexBatch(conn, objects); err != nil {
			return err
		}
		if err := PersistObjectsAnchorsEdges(conn, spaceRoot, objects, anchors, edges); err != nil {
			return err
		}
		objects = objects[:0]
		anchors = anchors[:0]
		edges = edges[:0]
		return nil
	}

	walkStats, err := parser.WalkSpaceWithOptions(spaceRoot, normSpaceID, walkOpts, func(item parser.ParsedFile) error {
		seen[item.Object.ID] = struct{}{}
		objects = append(objects, item.Object)
		anchors = append(anchors, item.Anchors...)
		edges = append(edges, item.Edges...)
		if len(objects) >= batchSize {
			return flush()
		}
		return nil
	})
	if err != nil {
		return RebuildStats{}, err
	}
	if err := flush(); err != nil {
		return RebuildStats{}, err
	}
	if err := PruneMissing(conn, normSpaceID, seen); err != nil {
		return RebuildStats{}, err
	}
	return RebuildStats{
		Objects:        walkStats.Objects,
		Anchors:        walkStats.Anchors,
		Edges:          walkStats.Edges,
		MarkdownSeen:   walkStats.MarkdownSeen,
		FilesParsed:    walkStats.FilesParsed,
		FilesSkipped:   walkStats.FilesSkipped,
		DirsSkipped:    walkStats.DirsSkipped,
		BytesRead:      walkStats.BytesRead,
		Skips:          walkStats.Skips,
		SkipsTruncated: walkStats.SkipsTruncated,
	}, nil
}

// PromoteFile parses one canonical Markdown file and updates the object,
// anchor, parser-derived edge, and FTS rows for it.
func PromoteFile(conn *sql.DB, spaceRoot, spaceID, path string) (Promotion, error) {
	spaceID = normalizeSpaceID(spaceID)
	obj, anchors, edges, err := parser.ParseFile(path, spaceID)
	if err != nil {
		return Promotion{}, err
	}
	if err := recall.Index(conn, obj); err != nil {
		return Promotion{}, err
	}
	if err := PersistObjectsAnchorsEdges(conn, spaceRoot, []types.Object{obj}, anchors, edges); err != nil {
		return Promotion{}, err
	}
	return Promotion{Object: obj, Anchors: len(anchors), Edges: len(edges)}, nil
}

// PromoteFiles promotes several canonical files and returns aggregate counts.
func PromoteFiles(conn *sql.DB, spaceRoot, spaceID string, paths []string) (objects, anchors, edges int, err error) {
	seen := map[string]bool{}
	for _, path := range paths {
		if seen[path] {
			continue
		}
		seen[path] = true
		p, perr := PromoteFile(conn, spaceRoot, spaceID, path)
		if perr != nil {
			return objects, anchors, edges, fmt.Errorf("promote %s: %w", path, perr)
		}
		objects++
		anchors += p.Anchors
		edges += p.Edges
	}
	return objects, anchors, edges, nil
}

// PersistObjectsAnchorsEdges writes parser output to objects/anchors/edges
// tables. It refreshes parser-derived rows for each object without touching
// graft/manual edges.
func PersistObjectsAnchorsEdges(conn *sql.DB, spacePath string, objects []types.Object, anchors []types.Anchor, edges []types.Edge) error {
	tx, err := conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	now := time.Now().UTC().Format(time.RFC3339)

	for _, o := range objects {
		if _, err := tx.Exec(`DELETE FROM anchors WHERE object_id = ?`, o.ID); err != nil {
			return fmt.Errorf("delete stale anchors %q: %w", o.ID, err)
		}
		if _, err := tx.Exec(`
			DELETE FROM edges
			WHERE src_id = ?
			  AND COALESCE(derivation, '') IN ('frontmatter', 'linkref', 'wikilink')`,
			o.ID); err != nil {
			return fmt.Errorf("delete stale parser edges %q: %w", o.ID, err)
		}

		tagsJSON := "[]"
		if len(o.Tags) > 0 {
			if b, jerr := json.Marshal(o.Tags); jerr == nil {
				tagsJSON = string(b)
			}
		}
		metadataJSON, err := objectMetadataJSON(o)
		if err != nil {
			return err
		}
		fileID, _ := filepath.Rel(spacePath, o.FilePath)
		if fileID == "" {
			fileID = o.FilePath
		}
		updated := now
		if !o.UpdatedAt.IsZero() {
			updated = o.UpdatedAt.UTC().Format(time.RFC3339)
		}
		if _, err := tx.Exec(`
			INSERT INTO objects (id, type, space_id, file_id, status, title, tags_json, summary, metadata_json, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				type = excluded.type,
				space_id = excluded.space_id,
				file_id = excluded.file_id,
				status = excluded.status,
				title = excluded.title,
				tags_json = excluded.tags_json,
				summary = excluded.summary,
				metadata_json = excluded.metadata_json,
				updated_at = excluded.updated_at`,
			o.ID, string(o.Type), o.SpaceID, fileID, o.Status, o.Title, tagsJSON, o.Summary, metadataJSON, updated); err != nil {
			return fmt.Errorf("upsert object %q: %w", o.ID, err)
		}
	}

	for _, a := range anchors {
		if _, err := tx.Exec(`
			INSERT INTO anchors (id, object_id, heading_path, start_byte, end_byte, start_line, end_line, node_kind)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				object_id = excluded.object_id,
				heading_path = excluded.heading_path,
				start_byte = excluded.start_byte,
				end_byte = excluded.end_byte,
				start_line = excluded.start_line,
				end_line = excluded.end_line,
				node_kind = excluded.node_kind`,
			a.ID, a.ObjectID, a.HeadingPath, a.StartByte, a.EndByte, a.StartLine, a.EndLine, a.NodeKind); err != nil {
			return fmt.Errorf("upsert anchor %q: %w", a.ID, err)
		}
	}

	for _, e := range edges {
		conf := e.Confidence
		if conf == 0 {
			conf = 1.0
		}
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO edges (id, kind, src_id, dst_id, confidence, derivation, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			e.ID, string(e.Kind), e.SrcID, e.DstID, conf, e.Derivation, now); err != nil {
			return fmt.Errorf("upsert edge %q: %w", e.ID, err)
		}
	}

	return tx.Commit()
}

// PruneMissing removes parser-derived rows for objects in spaceID that were
// NOT seen during the walk — i.e. whose source Markdown file was renamed or
// deleted since the last index. This is what makes `index rebuild`
// authoritative: without it, the upsert path leaves orphaned objects (and
// their anchors / FTS rows / parser edges) behind, producing phantom
// `hypha show`/`recall` hits that point at deleted files.
//
// spaceID must be the bare/normalized space id (e.g. "m31labs/hyphae"), the
// same form stored in objects.space_id. seen holds the ids of every object
// promoted from a file that still exists on disk.
//
// Scope mirrors PersistObjectsAnchorsEdges: only parser-derived edges
// ('frontmatter', 'linkref', 'wikilink') are removed, so manually-authored or
// graft-applied edges survive a rebuild. Anchors and FTS rows are owned
// entirely by the parser, so they are removed unconditionally for orphans.
func PruneMissing(conn *sql.DB, spaceID string, seen map[string]struct{}) error {
	tx, err := conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	rows, err := tx.Query(`SELECT id FROM objects WHERE space_id = ?`, spaceID)
	if err != nil {
		return fmt.Errorf("prune: list objects for %q: %w", spaceID, err)
	}
	var orphans []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close() //nolint:errcheck
			return fmt.Errorf("prune: scan object id: %w", err)
		}
		if _, ok := seen[id]; !ok {
			orphans = append(orphans, id)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close() //nolint:errcheck
		return fmt.Errorf("prune: iterate objects: %w", err)
	}
	rows.Close() //nolint:errcheck

	for _, id := range orphans {
		if _, err := tx.Exec(`DELETE FROM objects_fts WHERE id = ?`, id); err != nil {
			return fmt.Errorf("prune: delete fts row %q: %w", id, err)
		}
		if _, err := tx.Exec(`DELETE FROM anchors WHERE object_id = ?`, id); err != nil {
			return fmt.Errorf("prune: delete anchors %q: %w", id, err)
		}
		if _, err := tx.Exec(`
			DELETE FROM edges
			WHERE src_id = ?
			  AND COALESCE(derivation, '') IN ('frontmatter', 'linkref', 'wikilink')`,
			id); err != nil {
			return fmt.Errorf("prune: delete parser edges %q: %w", id, err)
		}
		if _, err := tx.Exec(`DELETE FROM objects WHERE id = ?`, id); err != nil {
			return fmt.Errorf("prune: delete object %q: %w", id, err)
		}
	}

	return tx.Commit()
}

func objectMetadataJSON(o types.Object) (string, error) {
	if len(o.Frontmtr) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(o.Frontmtr)
	if err != nil {
		return "", fmt.Errorf("marshal object metadata %q: %w", o.ID, err)
	}
	return string(b), nil
}

func normalizeSpaceID(spaceID string) string {
	const prefix = "hypha://"
	if len(spaceID) >= len(prefix) && spaceID[:len(prefix)] == prefix {
		return spaceID[len(prefix):]
	}
	return spaceID
}
