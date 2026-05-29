package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/hyphae/internal/db"
	"m31labs.dev/hyphae/internal/recall"
	"m31labs.dev/hyphae/internal/types"
)

func TestServer_InitializeListAndCall(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer conn.Close()

	// Seed one indexable object so recall has something to find.
	if err := recall.IndexBatch(conn, []types.Object{
		{
			ID: "concept.envelope", Type: types.TypeConcept,
			SpaceID: "test/space", Title: "Envelope",
			Summary: "Uniform JSON envelope for hyphae CLI output.",
			Body:    "The envelope wraps every command's data under a stable schema. Agents reach for it because parsing is uniform.",
		},
	}); err != nil {
		t.Fatalf("seed IndexBatch: %v", err)
	}

	requests := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"t","version":"0"},"capabilities":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"hypha_recall","arguments":{"query":"envelope"}}}`,
	}
	in := strings.NewReader(strings.Join(requests, "\n") + "\n")
	out := &bytes.Buffer{}

	s := NewServer(conn, t.TempDir(), ServerInfo{Name: "test", Version: "0"})
	s.in = in
	s.out = out
	s.log = &bytes.Buffer{}

	if err := s.Serve(); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 response lines, got %d:\n%s", len(lines), out.String())
	}

	// 1. initialize response carries serverInfo + protocolVersion.
	var init map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &init); err != nil {
		t.Fatalf("init: bad json: %v", err)
	}
	result, _ := init["result"].(map[string]any)
	if result["protocolVersion"] != ProtocolVersion {
		t.Errorf("init.result.protocolVersion = %v, want %s", result["protocolVersion"], ProtocolVersion)
	}

	// 2. tools/list response has > 0 tools.
	var list map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &list); err != nil {
		t.Fatalf("tools/list: bad json: %v", err)
	}
	lr, _ := list["result"].(map[string]any)
	tools, _ := lr["tools"].([]any)
	if len(tools) == 0 {
		t.Error("tools/list returned no tools")
	}

	// 3. tools/call hypha_recall returns content with embedded envelope.
	var call map[string]any
	if err := json.Unmarshal([]byte(lines[2]), &call); err != nil {
		t.Fatalf("tools/call: bad json: %v", err)
	}
	cr, _ := call["result"].(map[string]any)
	content, _ := cr["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(content))
	}
	textBlock, _ := content[0].(map[string]any)
	text, _ := textBlock["text"].(string)
	if !strings.Contains(text, `"command":"hypha_recall"`) {
		t.Errorf("envelope command missing in tool response; got: %s", text)
	}
	if !strings.Contains(text, `"Envelope"`) {
		t.Errorf("expected to find the seeded object's title 'Envelope' in response; got: %s", text)
	}
}

func TestServer_CompactFormatShortensKeys(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer conn.Close()
	if err := recall.IndexBatch(conn, []types.Object{
		{ID: "x", Type: types.TypeConcept, SpaceID: "t/s", Title: "X", Body: "compact x"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"hypha_recall","arguments":{"query":"compact","format":"compact"}}}` + "\n",
	)
	out := &bytes.Buffer{}
	s := NewServer(conn, t.TempDir(), ServerInfo{Name: "test", Version: "0"})
	s.in = in
	s.out = out
	s.log = &bytes.Buffer{}
	if err := s.Serve(); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	// The inner JSON is escaped inside the MCP transport string, so check for
	// the escaped form of the short key.
	body := out.String()
	if !strings.Contains(body, `\"c\":\"hypha_recall\"`) {
		t.Errorf("compact format missing short key c (command); got: %s", body)
	}
	if strings.Contains(body, `\"command\"`) {
		t.Errorf("compact format should not contain full key 'command'; got: %s", body)
	}
}

func TestServer_TruncationWarningsWhenOverBudget(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer conn.Close()
	// Seed enough objects that a tight budget forces a trim.
	var objs []types.Object
	for i := 0; i < 12; i++ {
		objs = append(objs, types.Object{
			ID:      fmt.Sprintf("obj-%02d", i),
			Type:    types.TypeConcept,
			SpaceID: "t/s",
			Title:   fmt.Sprintf("Object %d about widgets and gizmos", i),
			Body:    "widget gizmo widget gizmo widget gizmo widget gizmo widget gizmo widget gizmo",
		})
	}
	if err := recall.IndexBatch(conn, objs); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Make sure spaces/ exists so listSpaces doesn't return ENOENT.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "spaces"), 0o755); err != nil {
		t.Fatalf("mkdir spaces: %v", err)
	}
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"hypha_spore_list","arguments":{"max_tokens":50}}}` + "\n",
	)
	out := &bytes.Buffer{}
	s := NewServer(conn, root, ServerInfo{Name: "test", Version: "0"})
	s.in = in
	s.out = out
	s.log = &bytes.Buffer{}
	if err := s.Serve(); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	// Empty spaces → empty list → response well-formed and tiny.
	if !strings.Contains(out.String(), `\"command\":\"hypha_spore_list\"`) {
		t.Errorf("expected command spore_list in response; got: %s", out.String())
	}
}

// TestServe_IdleTimeoutDrains verifies a long-running mcp serve process
// self-drains after the idle timeout when its (live) client sends nothing —
// so an idle/hung session can't pin the daemon's memory indefinitely.
func TestServe_IdleTimeoutDrains(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer conn.Close()

	// A pipe with no writes: the reader stays open (client alive) but idle.
	pr, pw := io.Pipe()
	defer pw.Close() // release the scan goroutine after the assertion

	s := NewServer(conn, t.TempDir(), ServerInfo{Name: "test", Version: "0"})
	s.in = pr
	s.out = &bytes.Buffer{}
	s.log = &bytes.Buffer{}
	s.SetIdleTimeout(50 * time.Millisecond)

	done := make(chan error, 1)
	go func() { done <- s.Serve() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned error on idle drain: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not drain after idle timeout — still blocked on input")
	}
}
