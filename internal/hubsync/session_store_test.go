package hubsync

import (
	"path/filepath"
	"testing"
	"time"

	"m31labs.dev/hyphae/internal/db"
)

func TestDBSessionStore(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	defer conn.Close()
	s := dbSessionStore{conn: conn}

	if err := s.create("tok-1", "alice", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("create: %v", err)
	}
	if login, ok := s.lookup("tok-1"); !ok || login != "alice" {
		t.Fatalf("lookup = (%q, %v), want (alice, true)", login, ok)
	}
	// Wrong token does not resolve.
	if _, ok := s.lookup("nope"); ok {
		t.Error("unknown token should not resolve")
	}
	// The raw token is not stored — only its hash.
	var raw int
	_ = conn.QueryRow(`SELECT count(*) FROM admin_sessions WHERE token_hash = 'tok-1'`).Scan(&raw)
	if raw != 0 {
		t.Error("raw token must not be stored as token_hash")
	}
	s.drop("tok-1")
	if _, ok := s.lookup("tok-1"); ok {
		t.Error("dropped session should not resolve")
	}
}

func TestDBSessionStoreExpiry(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	defer conn.Close()
	s := dbSessionStore{conn: conn}

	if err := s.create("expired", "bob", time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, ok := s.lookup("expired"); ok {
		t.Error("expired session should not resolve")
	}
}

func TestMemSessionStore(t *testing.T) {
	s := newMemSessionStore()
	_ = s.create("t", "carol", time.Now().Add(time.Hour))
	if login, ok := s.lookup("t"); !ok || login != "carol" {
		t.Fatalf("lookup = (%q, %v), want (carol, true)", login, ok)
	}
	if err := s.create("ex", "dave", time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, ok := s.lookup("ex"); ok {
		t.Error("expired mem session should not resolve")
	}
	s.drop("t")
	if _, ok := s.lookup("t"); ok {
		t.Error("dropped mem session should not resolve")
	}
}
