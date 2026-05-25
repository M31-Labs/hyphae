package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/odvcencio/gosx"
	"github.com/odvcencio/gosx/server"
	"github.com/odvcencio/hyphae/cmd/hypha-viz/graphsurface"
	"github.com/odvcencio/hyphae/internal/db"
)

// buildTestApp creates a GoSX app backed by a temp DB for smoke testing.
func buildTestApp(t *testing.T) (*server.App, *os.File) {
	t.Helper()

	// Create a temp directory for the DB.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	conn, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	app := server.New()
	app.SetPublicDir("")

	app.Route("/", func(r *http.Request) gosx.Node {
		return BuildGraphPage(graphsurface.GraphProps{})
	})
	app.API("GET /api/graph", handleGraph(conn))
	app.API("GET /api/search", handleSearch(conn))
	app.API("GET /api/object/{id}", handleObject(conn))

	// Return a temp file handle so the caller can inspect the path if needed.
	f, _ := os.Open(dbPath)
	return app, f
}

// TestGraphEndpoint fires GET /api/graph and asserts valid JSON with nodes+edges.
func TestGraphEndpoint(t *testing.T) {
	app, f := buildTestApp(t)
	if f != nil {
		defer f.Close()
	}

	handler := app.Build()

	req := httptest.NewRequest(http.MethodGet, "/api/graph", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK && w.Code != http.StatusNoContent {
		t.Fatalf("GET /api/graph: want 200 or 204, got %d — body: %s", w.Code, w.Body.String())
	}

	if w.Code == http.StatusOK {
		var payload map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
			t.Fatalf("GET /api/graph: invalid JSON: %v — body: %s", err, w.Body.String())
		}
		if _, ok := payload["nodes"]; !ok {
			t.Error("GET /api/graph: missing 'nodes' key in response")
		}
		if _, ok := payload["edges"]; !ok {
			t.Error("GET /api/graph: missing 'edges' key in response")
		}
	}
}

// TestSearchEndpoint fires GET /api/search?q=test and asserts valid JSON.
func TestSearchEndpoint(t *testing.T) {
	app, f := buildTestApp(t)
	if f != nil {
		defer f.Close()
	}

	handler := app.Build()

	req := httptest.NewRequest(http.MethodGet, "/api/search?q=test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK && w.Code != http.StatusNoContent {
		t.Fatalf("GET /api/search: want 200 or 204, got %d — body: %s", w.Code, w.Body.String())
	}

	if w.Code == http.StatusOK {
		var payload map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
			t.Fatalf("GET /api/search: invalid JSON: %v — body: %s", err, w.Body.String())
		}
		// Response from recall.Recall always has summary and anchors.
		if _, ok := payload["summary"]; !ok {
			t.Error("GET /api/search: missing 'summary' key in response")
		}
		if _, ok := payload["anchors"]; !ok {
			t.Error("GET /api/search: missing 'anchors' key in response")
		}
	}
}

// TestObjectEndpoint fires GET /api/object/nonexistent and asserts valid JSON.
func TestObjectEndpoint(t *testing.T) {
	app, f := buildTestApp(t)
	if f != nil {
		defer f.Close()
	}

	handler := app.Build()

	req := httptest.NewRequest(http.MethodGet, "/api/object/nonexistent", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK && w.Code != http.StatusNoContent {
		t.Fatalf("GET /api/object/nonexistent: want 200 or 204, got %d — body: %s", w.Code, w.Body.String())
	}

	if w.Code == http.StatusOK {
		var payload map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
			t.Fatalf("GET /api/object/nonexistent: invalid JSON: %v — body: %s", err, w.Body.String())
		}
	}
}

// TestPageRoute fires GET / and checks we get a 200 HTML response.
func TestPageRoute(t *testing.T) {
	app, f := buildTestApp(t)
	if f != nil {
		defer f.Close()
	}

	handler := app.Build()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /: want 200, got %d — body: %s", w.Code, w.Body.String())
	}

	ct := w.Result().Header.Get("Content-Type")
	if ct == "" {
		t.Error("GET /: expected a Content-Type header")
	}
}
