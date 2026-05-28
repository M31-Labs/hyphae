// Package capability implements v0.1 local capability tokens.
//
// v0.1 tokens are unsigned: the token IS the capability ID (a random UUID),
// and verification is a DB lookup. Ed25519 signing lands in v0.1.1.
package capability

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"m31labs.dev/hyphae/internal/types"
)

// Issue persists a new capability and returns it. expires is the lifetime
// from now; pass 0 for the default of 24h.
func Issue(conn *sql.DB, subject, spaceID string, perms []string, limits types.Limits, expires time.Duration) (types.Capability, error) {
	if subject == "" {
		return types.Capability{}, errors.New("capability: subject required")
	}
	if spaceID == "" {
		return types.Capability{}, errors.New("capability: space required")
	}
	if len(perms) == 0 {
		return types.Capability{}, errors.New("capability: at least one permission required")
	}
	if expires <= 0 {
		expires = 24 * time.Hour
	}

	now := time.Now().UTC()
	cap := types.Capability{
		ID:          newID(),
		Subject:     subject,
		SpaceID:     spaceID,
		Permissions: perms,
		Limits:      limits,
		IssuedBy:    subject, // v0.1: self-issuance; OIDC flow lands in v0.3
		IssuedAt:    now,
		ExpiresAt:   now.Add(expires),
	}

	permsJSON, err := json.Marshal(cap.Permissions)
	if err != nil {
		return types.Capability{}, fmt.Errorf("capability: marshal perms: %w", err)
	}
	limitsJSON, err := json.Marshal(cap.Limits)
	if err != nil {
		return types.Capability{}, fmt.Errorf("capability: marshal limits: %w", err)
	}

	_, err = conn.Exec(`
		INSERT INTO capabilities
			(id, subject_identity_id, space_id, permissions_json, limits_json, issued_by, issued_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		cap.ID, cap.Subject, cap.SpaceID, string(permsJSON), string(limitsJSON),
		cap.IssuedBy, cap.IssuedAt.Format(time.RFC3339), cap.ExpiresAt.Format(time.RFC3339),
	)
	if err != nil {
		return types.Capability{}, fmt.Errorf("capability: insert: %w", err)
	}
	return cap, nil
}

// Verify looks up a capability by token (which is the ID in v0.1), returning
// the live capability if it exists, is not expired, and is not revoked.
func Verify(conn *sql.DB, token string) (*types.Capability, error) {
	row := conn.QueryRow(`
		SELECT id, subject_identity_id, space_id, permissions_json, limits_json,
		       issued_by, issued_at, expires_at, revoked_at
		FROM capabilities WHERE id = ?`, token)

	var (
		c                                  types.Capability
		permsJSON, limitsJSON              string
		issuedAt, expiresAt                string
		revokedAt                          sql.NullString
	)
	if err := row.Scan(&c.ID, &c.Subject, &c.SpaceID, &permsJSON, &limitsJSON,
		&c.IssuedBy, &issuedAt, &expiresAt, &revokedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("capability: token not found")
		}
		return nil, fmt.Errorf("capability: verify scan: %w", err)
	}

	if err := json.Unmarshal([]byte(permsJSON), &c.Permissions); err != nil {
		return nil, fmt.Errorf("capability: unmarshal perms: %w", err)
	}
	if limitsJSON != "" {
		if err := json.Unmarshal([]byte(limitsJSON), &c.Limits); err != nil {
			return nil, fmt.Errorf("capability: unmarshal limits: %w", err)
		}
	}
	if c.IssuedAt, _ = time.Parse(time.RFC3339, issuedAt); c.IssuedAt.IsZero() {
		return nil, fmt.Errorf("capability: invalid issued_at: %q", issuedAt)
	}
	if c.ExpiresAt, _ = time.Parse(time.RFC3339, expiresAt); c.ExpiresAt.IsZero() {
		return nil, fmt.Errorf("capability: invalid expires_at: %q", expiresAt)
	}
	if revokedAt.Valid {
		t, _ := time.Parse(time.RFC3339, revokedAt.String)
		c.RevokedAt = &t
		if !t.IsZero() {
			return nil, errors.New("capability: token revoked")
		}
	}
	if time.Now().UTC().After(c.ExpiresAt) {
		return nil, errors.New("capability: token expired")
	}
	return &c, nil
}

// List returns capabilities, newest first. spaceID, when non-empty, filters
// to one space. includeRevoked controls whether revoked tokens are returned;
// expired-but-not-revoked tokens are always included (the caller decides how
// to present them). The token plaintext is the ID, so callers must treat the
// returned IDs as secrets.
func List(conn *sql.DB, spaceID string, includeRevoked bool) ([]types.Capability, error) {
	q := `
		SELECT id, subject_identity_id, space_id, permissions_json, limits_json,
		       issued_by, issued_at, expires_at, revoked_at
		FROM capabilities`
	var (
		clauses []string
		args    []any
	)
	if spaceID != "" {
		clauses = append(clauses, "space_id = ?")
		args = append(args, spaceID)
	}
	if !includeRevoked {
		clauses = append(clauses, "revoked_at IS NULL")
	}
	if len(clauses) > 0 {
		q += " WHERE " + strings.Join(clauses, " AND ")
	}
	q += " ORDER BY issued_at DESC"

	rows, err := conn.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("capability: list: %w", err)
	}
	defer rows.Close()

	var out []types.Capability
	for rows.Next() {
		var (
			c                     types.Capability
			permsJSON, limitsJSON string
			issuedAt, expiresAt   string
			revokedAt             sql.NullString
		)
		if err := rows.Scan(&c.ID, &c.Subject, &c.SpaceID, &permsJSON, &limitsJSON,
			&c.IssuedBy, &issuedAt, &expiresAt, &revokedAt); err != nil {
			return nil, fmt.Errorf("capability: list scan: %w", err)
		}
		if err := json.Unmarshal([]byte(permsJSON), &c.Permissions); err != nil {
			return nil, fmt.Errorf("capability: list unmarshal perms: %w", err)
		}
		if limitsJSON != "" {
			if err := json.Unmarshal([]byte(limitsJSON), &c.Limits); err != nil {
				return nil, fmt.Errorf("capability: list unmarshal limits: %w", err)
			}
		}
		c.IssuedAt, _ = time.Parse(time.RFC3339, issuedAt)
		c.ExpiresAt, _ = time.Parse(time.RFC3339, expiresAt)
		if revokedAt.Valid {
			if t, err := time.Parse(time.RFC3339, revokedAt.String); err == nil {
				c.RevokedAt = &t
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Revoke marks a capability revoked. It remains in the DB for audit.
func Revoke(conn *sql.DB, token string) error {
	res, err := conn.Exec(`UPDATE capabilities SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		time.Now().UTC().Format(time.RFC3339), token)
	if err != nil {
		return fmt.Errorf("capability: revoke: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("capability: token not found or already revoked")
	}
	return nil
}

// newID returns a 32-hex-char random capability id (128 bits of entropy).
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "cap_" + hex.EncodeToString(b[:])
}
