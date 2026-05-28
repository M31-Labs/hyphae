// Package db opens and migrates the Hyphae SQLite index.
//
// The index lives at $HYPHAE_HOME/.index/hyphae.db (default
// $HOME/.hyphae/.index/hyphae.db). It is rebuildable from the filesystem
// at any time via `hypha index rebuild`.
package db

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// DefaultPath returns the default SQLite index path.
// Resolves HYPHAE_HOME → $HOME/.hyphae as the install root.
func DefaultPath() (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, ".index", "hyphae.db"), nil
}

// Root returns the Hyphae install root (HYPHAE_HOME or ~/.hyphae).
func Root() (string, error) {
	if v := os.Getenv("HYPHAE_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".hyphae"), nil
}

// Open opens the SQLite DB at path (creating parent dirs as needed) and
// runs the embedded schema migration. Safe to call repeatedly.
func Open(path string) (*sql.DB, error) {
	conn, err := OpenRaw(path)
	if err != nil {
		return nil, err
	}
	if err := Migrate(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// OpenRaw opens the SQLite DB at path (creating parent dirs as needed)
// without applying the canonical Hyphae index schema. Used by packages
// (e.g. crdtdb) that own their own schema and just want a connection.
func OpenRaw(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("hyphae/db: mkdir dir: %w", err)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)", path)
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("hyphae/db: open sqlite: %w", err)
	}
	return conn, nil
}

// Migrate runs the embedded schema. Idempotent: every statement uses
// CREATE ... IF NOT EXISTS.
func Migrate(conn *sql.DB) error {
	// modernc.org/sqlite executes one statement at a time via Exec, but a
	// single multi-statement payload is fine in practice. Be defensive and
	// split on bare semicolons at line ends.
	for _, stmt := range splitStatements(schemaSQL) {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := conn.Exec(stmt); err != nil {
			return fmt.Errorf("hyphae/db: migrate (%.60q…): %w", stmt, err)
		}
	}
	return nil
}

// splitStatements splits a SQL script into individual statements on bare
// `;\n` boundaries. Adequate for our embedded schema; not a general parser.
func splitStatements(src string) []string {
	var out []string
	var cur strings.Builder
	for _, line := range strings.Split(src, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "--") || trim == "" {
			continue
		}
		cur.WriteString(line)
		cur.WriteString("\n")
		if strings.HasSuffix(trim, ";") {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	if rest := strings.TrimSpace(cur.String()); rest != "" {
		out = append(out, rest)
	}
	return out
}
