package read

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"m31labs.dev/hyphae/internal/db"
	internalpulse "m31labs.dev/hyphae/internal/pulse"
)

const defaultObjectLimit = 500
const defaultSporeLimit = 200
const defaultReceiptLimit = 200
const defaultEdgeLimit = 100

// DefaultIndexPath returns the default path to the Hyphae SQLite index.
// Equivalent to $HYPHAE_HOME/.index/hyphae.db, defaulting to ~/.hyphae.
func DefaultIndexPath() (string, error) {
	return db.DefaultPath()
}

// Reader is a read-only view over the Hyphae SQLite index.
// Obtain one with OpenIndex. Call Close when done.
type Reader struct {
	conn *sql.DB
}

// OpenIndex opens the Hyphae index at path in read-only WAL mode and runs the
// embedded schema migration (idempotent CREATE IF NOT EXISTS statements).
// Returns a Reader ready for queries.
func OpenIndex(path string) (*Reader, error) {
	conn, err := db.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read: open index: %w", err)
	}
	return &Reader{conn: conn}, nil
}

// Close releases the underlying database connection.
func (r *Reader) Close() error {
	if r == nil || r.conn == nil {
		return nil
	}
	return r.conn.Close()
}

// Spaces returns all spaces visible in the index. It first queries the spaces
// table (populated by federation / capability flows). If that table is empty
// — which is the case for installations that only run `hypha index rebuild`
// without federation — it falls back to synthesising minimal Space entries
// from the distinct space_id values in the objects table, using the files
// table to recover root_path where available. The fallback entries will have
// empty URI/Authority/Scope/Visibility/TrustDefault fields.
func (r *Reader) Spaces() ([]Space, error) {
	rows, err := r.conn.Query(`
		SELECT id, uri, authority, scope, visibility, root_path, trust_default,
		       created_at, COALESCE(metadata_json, '')
		FROM spaces ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("read: spaces query: %w", err)
	}
	defer rows.Close()

	var out []Space
	for rows.Next() {
		var s Space
		var createdAt string
		if err := rows.Scan(
			&s.ID, &s.URI, &s.Authority, &s.Scope, &s.Visibility,
			&s.RootPath, &s.TrustDefault, &createdAt, &s.MetadataJSON,
		); err != nil {
			return nil, fmt.Errorf("read: spaces scan: %w", err)
		}
		if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
			s.CreatedAt = t
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Fallback: if the spaces table is empty (local-only installations that
	// never run the federation handshake), synthesise minimal Space entries
	// from distinct space_id values found in the objects table.
	// We recover root_path by stripping the file's relative path from the
	// first matching files row — this is best-effort and may be empty.
	if len(out) == 0 {
		out, err = r.spacesFromObjects()
		if err != nil {
			return nil, err
		}
	}

	return out, nil
}

// spacesFromObjects synthesises Space entries from distinct space_id values
// in the objects table. root_path is recovered from the files table when
// possible by inspecting the path column of an associated file.
func (r *Reader) spacesFromObjects() ([]Space, error) {
	rows, err := r.conn.Query(`SELECT DISTINCT space_id FROM objects ORDER BY space_id`)
	if err != nil {
		return nil, fmt.Errorf("read: spaces fallback query: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("read: spaces fallback scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]Space, 0, len(ids))
	for _, id := range ids {
		s := Space{ID: id}
		// Best-effort: find the root_path by taking the path of one file in
		// this space and walking up to the segment before the space component.
		// The files table stores paths relative to the space root, so the
		// root itself is stored in a separate config — we just leave RootPath
		// empty here; callers that need it should use the files table directly.
		_ = r.conn.QueryRow(
			`SELECT COALESCE(f.path,'') FROM files f
			 JOIN objects o ON o.file_id = f.id
			 WHERE o.space_id = ? LIMIT 1`, id,
		).Scan(&s.RootPath)
		// RootPath from files is a relative path within the space, not the
		// absolute root — clear it to avoid misleading callers.
		s.RootPath = ""
		out = append(out, s)
	}
	return out, nil
}

// Objects returns objects matching filter. An empty filter returns all objects
// (capped at defaultObjectLimit rows, newest first).
func (r *Reader) Objects(filter ObjectFilter) ([]Object, error) {
	q := `SELECT id, type, space_id, file_id, COALESCE(status,''), COALESCE(title,''),
	             COALESCE(tags_json,''), COALESCE(summary,''), COALESCE(metadata_json,''),
	             updated_at
	      FROM objects WHERE 1=1`
	args := []any{}

	if filter.SpaceID != "" {
		q += " AND space_id = ?"
		args = append(args, filter.SpaceID)
	}
	if filter.Type != "" {
		q += " AND type = ?"
		args = append(args, filter.Type)
	}

	q += " ORDER BY updated_at DESC LIMIT ?"
	args = append(args, defaultObjectLimit)

	rows, err := r.conn.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("read: objects query: %w", err)
	}
	defer rows.Close()

	var out []Object
	for rows.Next() {
		var o Object
		var updatedAt string
		if err := rows.Scan(
			&o.ID, &o.Type, &o.SpaceID, &o.FileID, &o.Status, &o.Title,
			&o.TagsJSON, &o.Summary, &o.MetadataJSON, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("read: objects scan: %w", err)
		}
		if t, err := time.Parse(time.RFC3339, updatedAt); err == nil {
			o.UpdatedAt = t
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Spores returns spores matching filter (capped at defaultSporeLimit, newest first).
func (r *Reader) Spores(filter SporeFilter) ([]Spore, error) {
	q := `SELECT id, space_id, COALESCE(file_id,''), status, trust,
	             COALESCE(agent_identity_id,''), COALESCE(task_id,''), COALESCE(run_id,''),
	             content_hash, COALESCE(token_count,0), COALESCE(receipt_id,''),
	             submitted_at, reviewed_at, COALESCE(reviewed_by,''),
	             COALESCE(decision,''), COALESCE(decision_reason,''), COALESCE(metadata_json,'')
	      FROM spores WHERE 1=1`
	args := []any{}

	if filter.SpaceID != "" {
		q += " AND space_id = ?"
		args = append(args, filter.SpaceID)
	}
	if filter.Status != "" {
		q += " AND status = ?"
		args = append(args, filter.Status)
	}

	q += " ORDER BY submitted_at DESC LIMIT ?"
	args = append(args, defaultSporeLimit)

	rows, err := r.conn.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("read: spores query: %w", err)
	}
	defer rows.Close()

	var out []Spore
	for rows.Next() {
		var s Spore
		var submittedAt string
		var reviewedAt sql.NullString
		if err := rows.Scan(
			&s.ID, &s.SpaceID, &s.FileID, &s.Status, &s.Trust,
			&s.AgentIdentityID, &s.TaskID, &s.RunID,
			&s.ContentHash, &s.TokenCount, &s.ReceiptID,
			&submittedAt, &reviewedAt, &s.ReviewedBy,
			&s.Decision, &s.DecisionReason, &s.MetadataJSON,
		); err != nil {
			return nil, fmt.Errorf("read: spores scan: %w", err)
		}
		if t, err := time.Parse(time.RFC3339, submittedAt); err == nil {
			s.SubmittedAt = t
		}
		if reviewedAt.Valid && reviewedAt.String != "" {
			if t, err := time.Parse(time.RFC3339, reviewedAt.String); err == nil {
				s.ReviewedAt = &t
			}
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Receipts returns audit receipts matching filter (capped at defaultReceiptLimit, newest first).
func (r *Reader) Receipts(filter ReceiptFilter) ([]Receipt, error) {
	q := `SELECT id, space_id, subject_id, subject_kind, action, status,
	             COALESCE(content_hash,''), COALESCE(identity_id,''),
	             created_at, expires_at, COALESCE(metadata_json,'')
	      FROM receipts WHERE 1=1`
	args := []any{}

	if filter.SpaceID != "" {
		q += " AND space_id = ?"
		args = append(args, filter.SpaceID)
	}
	if filter.SubjectID != "" {
		q += " AND subject_id = ?"
		args = append(args, filter.SubjectID)
	}
	if filter.Action != "" {
		q += " AND action = ?"
		args = append(args, filter.Action)
	}
	if !filter.Since.IsZero() {
		q += " AND created_at >= ?"
		args = append(args, filter.Since.UTC().Format(time.RFC3339))
	}

	q += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, defaultReceiptLimit)

	rows, err := r.conn.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("read: receipts query: %w", err)
	}
	defer rows.Close()

	var out []Receipt
	for rows.Next() {
		var rec Receipt
		var createdAt string
		var expiresAt sql.NullString
		if err := rows.Scan(
			&rec.ID, &rec.SpaceID, &rec.SubjectID, &rec.SubjectKind,
			&rec.Action, &rec.Status, &rec.ContentHash, &rec.IdentityID,
			&createdAt, &expiresAt, &rec.MetadataJSON,
		); err != nil {
			return nil, fmt.Errorf("read: receipts scan: %w", err)
		}
		if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
			rec.CreatedAt = t
		}
		if expiresAt.Valid && expiresAt.String != "" {
			if t, err := time.Parse(time.RFC3339, expiresAt.String); err == nil {
				rec.ExpiresAt = &t
			}
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// Edges returns all edges where src_id OR dst_id matches srcOrDst.
// Results are capped at defaultEdgeLimit rows.
func (r *Reader) Edges(srcOrDst string) ([]Edge, error) {
	q := `SELECT id, kind, src_id, dst_id, COALESCE(confidence,0),
	             COALESCE(derivation,''), COALESCE(agent_source,''),
	             COALESCE(created_by,''), created_at, COALESCE(metadata_json,'')
	      FROM edges WHERE src_id = ? OR dst_id = ?
	      LIMIT ?`

	rows, err := r.conn.Query(q, srcOrDst, srcOrDst, defaultEdgeLimit)
	if err != nil {
		return nil, fmt.Errorf("read: edges query: %w", err)
	}
	defer rows.Close()

	var out []Edge
	for rows.Next() {
		var e Edge
		var createdAt string
		if err := rows.Scan(
			&e.ID, &e.Kind, &e.SrcID, &e.DstID, &e.Confidence,
			&e.Derivation, &e.AgentSource, &e.CreatedBy,
			&createdAt, &e.MetadataJSON,
		); err != nil {
			return nil, fmt.Errorf("read: edges scan: %w", err)
		}
		if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
			e.CreatedAt = t
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Pulse returns the pulse for the given window label (e.g. "30d"). It first
// checks the pulse_cache table for a valid (non-expired) entry. On a cache
// miss it runs the live aggregation query via internal/pulse.Compute and
// caches the result for 1 hour.
//
// Pulse is returned as a plain DTO with no internal types on the surface.
func (r *Reader) Pulse(window string) (Pulse, error) {
	// Map the window label to a duration for the live-compute path.
	dur, err := parseWindowLabel(window)
	if err != nil {
		return Pulse{}, fmt.Errorf("read: pulse: %w", err)
	}

	// Try cache first (TTL = 1 hour).
	cached, err := internalpulse.Cached(r.conn, "", window, time.Hour)
	if err == nil {
		return convertPulse(cached), nil
	}
	// ErrNoCache or anything else: fall through to live compute.

	live, err := internalpulse.Compute(r.conn, "", dur)
	if err != nil {
		return Pulse{}, fmt.Errorf("read: pulse compute: %w", err)
	}

	// Store for next caller (best-effort; ignore errors).
	_ = internalpulse.Store(r.conn, live, time.Hour)

	return convertPulse(live), nil
}

// convertPulse maps an internal pulse.Pulse to the public DTO.
func convertPulse(p internalpulse.Pulse) Pulse {
	out := Pulse{
		Space:       p.Space,
		Window:      p.Window,
		WindowStart: p.WindowStart,
		ComputedAt:  p.ComputedAt,
		Activity: PulseActivity{
			SporesSubmitted: p.Activity.SporesSubmitted,
			GraftsApplied:   p.Activity.GraftsApplied,
			NewObjects:      p.Activity.NewObjects,
			NewEdges:        p.Activity.NewEdges,
		},
	}
	for _, i := range p.TopInitiatives {
		out.TopInitiatives = append(out.TopInitiatives, PulseInitiative{
			ID:           i.ID,
			Title:        i.Title,
			InboundEdges: i.InboundEdges,
		})
	}
	for _, z := range p.HotZones {
		out.HotZones = append(out.HotZones, PulseHotZone{
			ObjectID:     z.ObjectID,
			Title:        z.Title,
			Type:         z.Type,
			GraftEdgesIn: z.GraftEdgesIn,
			NewEdgesOut:  z.NewEdgesOut,
		})
	}
	for _, pr := range p.RecentPressure {
		out.RecentPressure = append(out.RecentPressure, PulsePressure{
			Kind:  pr.Kind,
			Topic: pr.Topic,
			Count: pr.Count,
		})
	}
	for _, k := range p.EdgeKindDist {
		out.EdgeKindDist = append(out.EdgeKindDist, PulseKindCount{
			Kind:  k.Kind,
			Count: k.Count,
		})
	}
	return out
}

// parseWindowLabel converts a window label like "30d", "7d", "90d" to a
// time.Duration. Only day-based labels (e.g. "30d") are supported.
func parseWindowLabel(label string) (time.Duration, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		return 30 * 24 * time.Hour, nil
	}
	if strings.HasSuffix(label, "d") {
		var days int
		if _, err := fmt.Sscanf(label, "%dd", &days); err == nil && days > 0 {
			return time.Duration(days) * 24 * time.Hour, nil
		}
	}
	return 0, fmt.Errorf("unsupported window label %q (expected e.g. \"30d\")", label)
}
