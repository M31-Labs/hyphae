package hubsync

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"sync"
	"time"
)

// sessionStore persists admin sessions. Two implementations:
//   - dbSessionStore: SQLite-backed, survives hub restarts (the default
//     when the hub has an index DB).
//   - memSessionStore: in-memory, lost on restart (fallback when no DB
//     is available, e.g. tests or a DB-less local run).
//
// The raw session token lives only in the user's cookie. The store keys
// on its SHA-256 hash so a leaked store cannot be replayed as a cookie.
type sessionStore interface {
	create(token, login string, exp time.Time) error
	lookup(token string) (login string, ok bool)
	drop(token string)
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ─── SQLite-backed ──────────────────────────────────────────────────────────

type dbSessionStore struct{ conn *sql.DB }

func (s dbSessionStore) create(token, login string, exp time.Time) error {
	now := time.Now().UTC()
	// Opportunistically reap expired rows so the table stays small.
	_, _ = s.conn.Exec(`DELETE FROM admin_sessions WHERE expires_at < ?`, now.Format(time.RFC3339))
	_, err := s.conn.Exec(`
		INSERT INTO admin_sessions (token_hash, login, created_at, expires_at)
		VALUES (?, ?, ?, ?)`,
		hashToken(token), login, now.Format(time.RFC3339), exp.UTC().Format(time.RFC3339))
	return err
}

func (s dbSessionStore) lookup(token string) (string, bool) {
	var login, expiresAt string
	err := s.conn.QueryRow(`
		SELECT login, expires_at FROM admin_sessions WHERE token_hash = ?`,
		hashToken(token)).Scan(&login, &expiresAt)
	if err != nil {
		return "", false
	}
	exp, perr := time.Parse(time.RFC3339, expiresAt)
	if perr != nil || time.Now().UTC().After(exp) {
		s.drop(token)
		return "", false
	}
	return login, true
}

func (s dbSessionStore) drop(token string) {
	_, _ = s.conn.Exec(`DELETE FROM admin_sessions WHERE token_hash = ?`, hashToken(token))
}

// ─── in-memory fallback ───────────────────────────────────────────────────

type memSessionStore struct {
	mu sync.Mutex
	m  map[string]session
}

func newMemSessionStore() *memSessionStore {
	return &memSessionStore{m: make(map[string]session)}
}

func (s *memSessionStore) create(token, login string, exp time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, v := range s.m { // reap expired
		if now.After(v.exp) {
			delete(s.m, k)
		}
	}
	s.m[hashToken(token)] = session{login: login, exp: exp}
	return nil
}

func (s *memSessionStore) lookup(token string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[hashToken(token)]
	if !ok || time.Now().After(v.exp) {
		delete(s.m, hashToken(token))
		return "", false
	}
	return v.login, true
}

func (s *memSessionStore) drop(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, hashToken(token))
}
