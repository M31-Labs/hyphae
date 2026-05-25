// Package receipts is the data-access layer for the receipts table.
//
// Receipts are append-only audit records. Every contribute or graft operation
// writes exactly one receipt. The package enforces immutability: Write is
// idempotent on ID (returns ErrAlreadyExists if the row already exists) and
// there is no Update or Delete.
package receipts

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/odvcencio/hyphae/internal/types"
)

// ErrAlreadyExists is returned by Write when a receipt with the same ID
// already exists. Callers may ignore or treat as a real error.
var ErrAlreadyExists = errors.New("receipts: receipt id already exists")

// buildMetadataJSON merges PermissionsUsed/NextState from the Receipt with any
// caller-supplied MetadataJSON.  The structured fields (permissions_used,
// next_state) WIN over matching keys in MetadataJSON.
func buildMetadataJSON(r types.Receipt) (string, error) {
	// Start with the caller's MetadataJSON as the base map (if any).
	base := map[string]any{}
	if r.MetadataJSON != "" {
		if err := json.Unmarshal([]byte(r.MetadataJSON), &base); err != nil {
			return "", fmt.Errorf("receipts: unmarshal caller MetadataJSON: %w", err)
		}
	}

	// Overlay structured fields — they always win.
	if len(r.PermissionsUsed) > 0 {
		base["permissions_used"] = r.PermissionsUsed
	}
	if r.NextState != "" {
		base["next_state"] = r.NextState
	}

	if len(base) == 0 {
		return "", nil
	}
	b, err := json.Marshal(base)
	if err != nil {
		return "", fmt.Errorf("receipts: marshal metadata_json: %w", err)
	}
	return string(b), nil
}

// parseMetadataJSON extracts PermissionsUsed and NextState from the stored
// metadata_json string and populates the relevant Receipt fields.
func parseMetadataJSON(metaJSON string, r *types.Receipt) error {
	if metaJSON == "" {
		return nil
	}
	r.MetadataJSON = metaJSON

	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(metaJSON), &m); err != nil {
		// Malformed stored JSON: surface via MetadataJSON but don't fail.
		return nil
	}

	if raw, ok := m["permissions_used"]; ok {
		if err := json.Unmarshal(raw, &r.PermissionsUsed); err != nil {
			return fmt.Errorf("receipts: unmarshal permissions_used: %w", err)
		}
	}
	if raw, ok := m["next_state"]; ok {
		if err := json.Unmarshal(raw, &r.NextState); err != nil {
			return fmt.Errorf("receipts: unmarshal next_state: %w", err)
		}
	}
	return nil
}

// Write persists a Receipt to the receipts table.
// Idempotent on ID: if a receipt with the same ID exists, returns
// ErrAlreadyExists without modifying the row. Receipts are append-only.
func Write(conn *sql.DB, r types.Receipt) error {
	if r.ID == "" {
		return errors.New("receipts: ID required")
	}
	if r.SpaceID == "" {
		return errors.New("receipts: SpaceID required")
	}

	metaJSON, err := buildMetadataJSON(r)
	if err != nil {
		return err
	}

	var expiresAt *string
	if r.ExpiresAt != nil {
		s := r.ExpiresAt.UTC().Format(time.RFC3339)
		expiresAt = &s
	}

	_, err = conn.Exec(`
		INSERT INTO receipts
			(id, space_id, subject_id, subject_kind, action, status,
			 content_hash, identity_id, created_at, expires_at, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.SpaceID, r.SubjectID, r.SubjectKind, r.Action, r.Status,
		r.ContentHash, r.IdentityID,
		r.CreatedAt.UTC().Format(time.RFC3339),
		expiresAt,
		nullableString(metaJSON),
	)
	if err != nil {
		// SQLite signals a UNIQUE constraint violation with error code 1555
		// (SQLITE_CONSTRAINT_PRIMARYKEY) or the generic constraint message.
		if isUniqueConstraintErr(err) {
			return ErrAlreadyExists
		}
		return fmt.Errorf("receipts: insert: %w", err)
	}
	return nil
}

// Get fetches one receipt by ID.
func Get(conn *sql.DB, id string) (types.Receipt, error) {
	row := conn.QueryRow(`
		SELECT id, space_id, subject_id, subject_kind, action, status,
		       content_hash, identity_id, created_at, expires_at, metadata_json
		FROM receipts WHERE id = ?`, id)

	return scanReceipt(row)
}

// ListFilter controls which receipts List returns.
type ListFilter struct {
	SpaceID   string
	SubjectID string    // empty = all subjects
	Action    string    // empty = all actions
	Since     time.Time // zero = all time
	Limit     int       // required; capped to 1000
}

// List returns receipts matching the filter, newest first.
// Pass empty SpaceID to include all spaces.
// Pass zero Since to include all time.
// Limit must be > 0 (capped to 1000).
func List(conn *sql.DB, f ListFilter) ([]types.Receipt, error) {
	if f.Limit <= 0 {
		return nil, errors.New("receipts: Limit must be > 0")
	}
	if f.Limit > 1000 {
		f.Limit = 1000
	}

	query := `
		SELECT id, space_id, subject_id, subject_kind, action, status,
		       content_hash, identity_id, created_at, expires_at, metadata_json
		FROM receipts
		WHERE 1=1`

	args := []any{}

	if f.SpaceID != "" {
		query += " AND space_id = ?"
		args = append(args, f.SpaceID)
	}
	if f.SubjectID != "" {
		query += " AND subject_id = ?"
		args = append(args, f.SubjectID)
	}
	if f.Action != "" {
		query += " AND action = ?"
		args = append(args, f.Action)
	}
	if !f.Since.IsZero() {
		query += " AND created_at >= ?"
		args = append(args, f.Since.UTC().Format(time.RFC3339))
	}

	query += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, f.Limit)

	rows, err := conn.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("receipts: list query: %w", err)
	}
	defer rows.Close()

	var out []types.Receipt
	for rows.Next() {
		r, err := scanReceipt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("receipts: list rows: %w", err)
	}
	return out, nil
}

// scanner abstracts *sql.Row and *sql.Rows so scanReceipt works for both.
type scanner interface {
	Scan(dest ...any) error
}

func scanReceipt(s scanner) (types.Receipt, error) {
	var (
		r                        types.Receipt
		contentHash, identityID  sql.NullString
		createdAt                string
		expiresAt                sql.NullString
		metaJSON                 sql.NullString
	)
	if err := s.Scan(
		&r.ID, &r.SpaceID, &r.SubjectID, &r.SubjectKind, &r.Action, &r.Status,
		&contentHash, &identityID, &createdAt, &expiresAt, &metaJSON,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return types.Receipt{}, fmt.Errorf("receipts: not found")
		}
		return types.Receipt{}, fmt.Errorf("receipts: scan: %w", err)
	}

	if contentHash.Valid {
		r.ContentHash = contentHash.String
	}
	if identityID.Valid {
		r.IdentityID = identityID.String
	}

	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return types.Receipt{}, fmt.Errorf("receipts: parse created_at %q: %w", createdAt, err)
	}
	r.CreatedAt = t

	if expiresAt.Valid && expiresAt.String != "" {
		t, err := time.Parse(time.RFC3339, expiresAt.String)
		if err != nil {
			return types.Receipt{}, fmt.Errorf("receipts: parse expires_at %q: %w", expiresAt.String, err)
		}
		r.ExpiresAt = &t
	}

	if metaJSON.Valid {
		if err := parseMetadataJSON(metaJSON.String, &r); err != nil {
			return types.Receipt{}, err
		}
	}

	return r, nil
}

// nullableString converts an empty string to a nil interface (NULL in SQLite).
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// isUniqueConstraintErr reports whether err is a SQLite UNIQUE/PRIMARY KEY
// constraint violation.
func isUniqueConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// modernc.org/sqlite surfaces these as: "UNIQUE constraint failed: ..."
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed")
}
