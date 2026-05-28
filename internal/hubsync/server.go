// Package hubsync wires Hyphae's per-space CRDT shadows into the
// gosx websocket hub (server side) and provides a matching client for
// the `hypha sync --peer ws://…` subcommand.
//
// Server architecture: one *hub.Hub per installed space, each
// registering its Shadow's Doc via `hub.SyncDoc("hyphae", doc)` (so
// every endpoint multiplexes exactly one CRDT doc with the fixed
// prefix byte). Each space gets its own URL path:
// /sync/<urlencoded-space-uri>.
//
// Auth (optional, --require-auth on the server) validates a Bearer
// token via internal/capability before the websocket upgrade. The
// token's `space_id` must match the requested space.
package hubsync

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"m31labs.dev/gosx/crdt"
	"m31labs.dev/gosx/hub"
	"m31labs.dev/hyphae/internal/capability"
	"m31labs.dev/hyphae/internal/crdtshadow"
)

// SyncDocName is the single doc name every space-scoped Hub registers
// internally. Clients don't need to know it — every connection is
// already scoped to one space by URL.
const SyncDocName = "hyphae"

// PathPrefix is the URL path prefix every space endpoint lives under.
// Example: /sync/hypha%3A%2F%2Fmyorg%2Fknowledge
const PathPrefix = "/sync/"

// Server mounts a gosx Hub per installed space on a single HTTP mux.
type Server struct {
	mu          sync.Mutex
	mux         *http.ServeMux
	hubs        map[string]*hub.Hub // spaceURI → hub
	registry    *crdtshadow.Registry
	installRoot string
	authConn    *sql.DB // capability table; nil disables auth
	requireAuth bool
}

// NewServer builds a Server that uses registry to back per-space
// shadows. installRoot is the Hyphae install root (HYPHAE_HOME).
// authConn, when non-nil, lets the server validate Bearer tokens via
// internal/capability; pass nil + requireAuth=false to disable auth.
func NewServer(installRoot string, registry *crdtshadow.Registry, authConn *sql.DB, requireAuth bool) *Server {
	return &Server{
		mux:         http.NewServeMux(),
		hubs:        make(map[string]*hub.Hub),
		registry:    registry,
		installRoot: installRoot,
		authConn:    authConn,
		requireAuth: requireAuth,
	}
}

// Register attaches a space to the server, opening its shadow and
// mounting /sync/<spaceURI> on the mux. Idempotent.
func (s *Server) Register(spaceURI string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.hubs[spaceURI]; ok {
		return nil
	}
	spaceRoot, err := crdtshadow.SpaceURIToPath(s.installRoot, spaceURI)
	if err != nil {
		return err
	}
	shadow, err := s.registry.Get(spaceRoot, spaceURI)
	if err != nil {
		return fmt.Errorf("hubsync: open shadow %s: %w", spaceURI, err)
	}
	h := hub.New("hyphae-" + spaceURI)
	h.SyncDoc(SyncDocName, shadow.Doc())
	s.hubs[spaceURI] = h

	path := PathPrefix + url.PathEscape(spaceURI)
	s.mux.HandleFunc(path, s.handler(spaceURI, h, shadow))

	// Persist on every CRDT change so the SQLite log stays current.
	shadow.Doc().OnChange(func(_ []crdt.Patch) {
		_, _ = shadow.Store().AppendChangesFromDoc(shadow.Doc())
	})
	return nil
}

// Mux returns the HTTP mux for embedding in a parent server. Add
// `/healthz`, `/spaces`, or any other admin endpoints from the caller.
func (s *Server) Mux() *http.ServeMux { return s.mux }

// Hubs returns a copy of the spaceURI → *hub.Hub map, for inspection
// or tests.
func (s *Server) Hubs() map[string]*hub.Hub {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]*hub.Hub, len(s.hubs))
	for k, v := range s.hubs {
		out[k] = v
	}
	return out
}

func (s *Server) handler(spaceURI string, h *hub.Hub, sh *crdtshadow.Shadow) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.requireAuth {
			if err := s.authorize(r, spaceURI); err != nil {
				http.Error(w, err.Error(), http.StatusUnauthorized)
				return
			}
		}
		h.ServeHTTP(w, r)
	}
}

func (s *Server) authorize(r *http.Request, spaceURI string) error {
	if s.authConn == nil {
		return fmt.Errorf("auth required but no capability DB configured")
	}
	token := extractToken(r)
	if token == "" {
		return fmt.Errorf("missing Bearer token")
	}
	cap, err := capability.Verify(s.authConn, token)
	if err != nil {
		return fmt.Errorf("invalid token: %w", err)
	}
	if cap.SpaceID != spaceURI {
		return fmt.Errorf("token scoped to %q, not %q", cap.SpaceID, spaceURI)
	}
	return nil
}

func extractToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return r.URL.Query().Get("token")
}

// Serve runs the HTTP server on addr until the context is cancelled.
// Returns the actual addr once listening (useful for tests that pass
// "127.0.0.1:0").
func (s *Server) Serve(ctx context.Context, addr string, ready chan<- string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	listener, err := newListener(addr)
	if err != nil {
		return fmt.Errorf("hubsync: listen %s: %w", addr, err)
	}
	if ready != nil {
		select {
		case ready <- listener.Addr().String():
		default:
		}
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

// EndpointURL builds the websocket URL for one space relative to a
// hub's base URL ("ws://host:port" or "wss://…"). Convenience for the
// client side.
func EndpointURL(baseURL, spaceURI string) string {
	base := strings.TrimRight(baseURL, "/")
	return base + PathPrefix + url.PathEscape(spaceURI)
}

func newListener(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}
