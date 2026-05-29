// Package mcp implements a Model Context Protocol server over stdio for the
// Hyphae read surface. Speaks JSON-RPC 2.0 line-delimited; one JSON object
// per line on stdin/stdout. See https://modelcontextprotocol.io for the
// protocol details.
//
// Scope (v1): read-only tools — recall, show, pulse, assess, spaces/spore/
// trace/graph/receipts list. Mutating operations (graft, spore submit,
// trace lifecycle) stay CLI-only; an MCP shell-out is the right level of
// friction for those.
package mcp

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// ProtocolVersion is the MCP version this server advertises.
const ProtocolVersion = "2024-11-05"

// ServerInfo identifies the server to MCP clients.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Server is the long-lived stdio MCP server.
type Server struct {
	conn        *sql.DB
	installRoot string
	in          io.Reader
	out         io.Writer
	log         io.Writer
	info        ServerInfo
	tools       []toolSpec
	idleTimeout time.Duration // 0 = no timeout (run until stdin closes)
}

// SetIdleTimeout makes Serve return (drain) after d elapses with no input.
// Zero (the default) disables it — Serve then runs until stdin closes. Used
// by `hypha mcp serve` so an idle or hung session can't pin the daemon's
// memory indefinitely.
func (s *Server) SetIdleTimeout(d time.Duration) { s.idleTimeout = d }

// NewServer builds an MCP server bound to a hyphae index DB and the
// install root that owns its spaces.
func NewServer(conn *sql.DB, installRoot string, info ServerInfo) *Server {
	s := &Server{
		conn:        conn,
		installRoot: installRoot,
		in:          os.Stdin,
		out:         os.Stdout,
		log:         os.Stderr,
		info:        info,
	}
	s.tools = buildTools(s)
	return s
}

// Serve runs the request/response loop until stdin closes, a fatal
// transport error occurs, or — when SetIdleTimeout is set — the client
// stays idle longer than the timeout. Reading happens on a goroutine so the
// idle timer can fire while we're blocked waiting for the next message.
func (s *Server) Serve() error {
	lines := make(chan []byte)
	scanErr := make(chan error, 1)
	done := make(chan struct{})
	defer close(done) // releases the reader goroutine when Serve returns

	go func() {
		scanner := bufio.NewScanner(s.in)
		// MCP messages can be larger than the default 64 KiB scanner buffer.
		scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		for scanner.Scan() {
			// scanner reuses its internal buffer; copy before handing off.
			b := append([]byte(nil), scanner.Bytes()...)
			select {
			case lines <- b:
			case <-done:
				return
			}
		}
		scanErr <- scanner.Err()
		close(lines)
	}()

	var idleC <-chan time.Time
	var timer *time.Timer
	if s.idleTimeout > 0 {
		timer = time.NewTimer(s.idleTimeout)
		defer timer.Stop()
		idleC = timer.C
	}

	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return <-scanErr
			}
			if timer != nil {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(s.idleTimeout)
			}
			if len(line) == 0 {
				continue
			}
			var msg rpcRequest
			if err := json.Unmarshal(line, &msg); err != nil {
				fmt.Fprintf(s.log, "mcp: bad json from client: %v\n", err)
				continue
			}
			s.dispatch(msg)
		case <-idleC:
			fmt.Fprintf(s.log, "mcp: idle for %s, draining\n", s.idleTimeout)
			return nil
		}
	}
}

// rpcRequest is one JSON-RPC 2.0 request envelope. Notifications have no
// id; requests have an id of any JSON type (number, string, null).
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcResponse is one JSON-RPC 2.0 response envelope; exactly one of Result
// or Error is set.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

const (
	errParse          = -32700
	errInvalidRequest = -32600
	errMethodNotFound = -32601
	errInvalidParams  = -32602
	errInternal       = -32603
)

func (s *Server) dispatch(req rpcRequest) {
	switch req.Method {
	case "initialize":
		s.handleInitialize(req)
	case "notifications/initialized", "initialized":
		// One-way notification from client — nothing to send back.
	case "tools/list":
		s.handleToolsList(req)
	case "tools/call":
		s.handleToolCall(req)
	case "ping":
		s.reply(req.ID, map[string]any{})
	default:
		if req.ID != nil {
			s.replyError(req.ID, errMethodNotFound, "unknown method: "+req.Method, nil)
		}
	}
}

func (s *Server) handleInitialize(req rpcRequest) {
	result := map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": s.info,
	}
	s.reply(req.ID, result)
}

func (s *Server) handleToolsList(req rpcRequest) {
	tools := make([]map[string]any, 0, len(s.tools))
	for _, t := range s.tools {
		tools = append(tools, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": t.InputSchema,
		})
	}
	s.reply(req.ID, map[string]any{"tools": tools})
}

func (s *Server) handleToolCall(req rpcRequest) {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.replyError(req.ID, errInvalidParams, "tools/call params: "+err.Error(), nil)
		return
	}
	var spec *toolSpec
	for i := range s.tools {
		if s.tools[i].Name == p.Name {
			spec = &s.tools[i]
			break
		}
	}
	if spec == nil {
		s.replyError(req.ID, errMethodNotFound, "no such tool: "+p.Name, nil)
		return
	}

	data, err := spec.Handler(p.Arguments)
	if err != nil {
		s.reply(req.ID, map[string]any{
			"content": []any{
				map[string]any{"type": "text", "text": err.Error()},
			},
			"isError": true,
		})
		return
	}

	// Token-conscious response: single-line full-key JSON envelope by
	// default; callers can opt into compact (short-key) via the per-tool
	// `format` arg. Lists get budget-aware row trimming with a TRUNCATED
	// warning when the cap kicks in.
	opts := optsFromArgs(p.Arguments, spec.DefaultMaxTokens)
	text := render(p.Name, data, opts)
	s.reply(req.ID, map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": text},
		},
	})
}

func (s *Server) reply(id json.RawMessage, result any) {
	if id == nil {
		// notifications don't get replies.
		return
	}
	resp := rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
	s.write(resp)
}

func (s *Server) replyError(id json.RawMessage, code int, msg string, data any) {
	if id == nil {
		return
	}
	resp := rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg, Data: data}}
	s.write(resp)
}

func (s *Server) write(v any) {
	enc := json.NewEncoder(s.out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(s.log, "mcp: encode response: %v\n", err)
	}
}
