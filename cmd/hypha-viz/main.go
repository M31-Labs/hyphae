//go:build !js

// Command hypha-viz is a local knowledge-graph visualization server for Hyphae.
//
// It opens the Hyphae SQLite index and spins up a GoSX server with a
// force-directed graph viewer, a search bar, and a detail panel — all
// rendered server-side. The canvas is now a //gosx:engine surface whose
// WASM is compiled transparently at startup via surface.Discover.
//
// Usage:
//
//	hypha-viz [--addr 127.0.0.1:7777] [--root <hyphae-home>]
//
// Visit http://127.0.0.1:7777 after starting.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/odvcencio/gosx"
	"github.com/odvcencio/gosx/engine/surface"
	"github.com/odvcencio/gosx/server"
	"github.com/odvcencio/hyphae/cmd/hypha-viz/graphsurface"
	"github.com/odvcencio/hyphae/internal/db"
	"github.com/odvcencio/hyphae/internal/vizdata"
)

const usageText = `hypha-viz — Hyphae knowledge-graph visualization server

Usage:
  hypha-viz [--addr <host:port>] [--root <hyphae-home>]

Flags:
  --addr   listen address (default: 127.0.0.1:7777)
  --root   Hyphae install root (default: HYPHAE_HOME or ~/.hyphae)

Visit http://127.0.0.1:7777 to view the graph after starting.
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "hypha-viz:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("hypha-viz", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usageText) }

	addr := fs.String("addr", "127.0.0.1:7777", "listen address")
	root := fs.String("root", "", "Hyphae install root (default: HYPHAE_HOME or ~/.hyphae)")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}

	// Resolve root.
	resolvedRoot := *root
	if resolvedRoot == "" {
		r, err := db.Root()
		if err != nil {
			return fmt.Errorf("resolve hyphae root: %w", err)
		}
		resolvedRoot = r
	}

	dbPath := filepath.Join(resolvedRoot, ".index", "hyphae.db")
	conn, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open db %s: %w", dbPath, err)
	}
	defer conn.Close()

	// Discover .gsx engine surfaces and compile their WASM modules.
	// Walks the project root, compiles each //gosx:engine surface to a
	// per-surface WASM (cached by content hash), and registers asset routes.
	// Must be called before the HTTP server starts.
	//
	// Note: surface.Discover uses the working directory as the project root.
	// Run hypha-viz from the module root (cd ~/work/hyphae && hypha-viz) or
	// set GOSX_DISABLE_SURFACE_AUTO=1 to skip when running from other dirs.
	if err := surface.Discover("."); err != nil {
		log.Printf("hypha-viz: surface discover: %v (continuing without WASM)", err)
	}

	// Build the GoSX app.
	app := server.New()

	// Disable the default public-dir serving (no public/ dir in this binary).
	app.SetPublicDir("")

	// Page route: GET / — full graph viewer shell.
	// Load the initial graph data server-side so the surface props include
	// nodes and edges; the WASM module receives them at mount time without a
	// separate fetch round-trip.
	app.Route("/", func(r *http.Request) gosx.Node {
		props := loadInitialGraph(conn)
		return BuildGraphPage(props)
	})

	// API: full or subgraph JSON.
	app.API("GET /api/graph", handleGraph(conn))

	// API: FTS5 search.
	app.API("GET /api/search", handleSearch(conn))

	// API: object detail by path param.
	app.API("GET /api/object/{id}", handleObject(conn))

	// Compose the surface WASM asset handler so /gosx/engines/*.wasm is served.
	app.Mount("/gosx/engines/", surface.Handler())

	fmt.Printf("hypha-viz listening on http://%s\n", *addr)
	fmt.Printf("  graph db: %s\n", dbPath)
	log.Fatal(app.ListenAndServe(*addr))
	return nil
}

// loadInitialGraph fetches the full graph from the DB and converts it to
// graphsurface.GraphProps for embedding in the surface placeholder. Errors are
// silently logged; an empty GraphProps is returned on failure.
func loadInitialGraph(conn *sql.DB) graphsurface.GraphProps {
	resp, err := vizdata.FullGraph(conn, nil, 500)
	if err != nil {
		log.Printf("hypha-viz: load initial graph: %v", err)
		return graphsurface.GraphProps{}
	}
	nodes := make([]graphsurface.GraphNode, len(resp.Nodes))
	for i, n := range resp.Nodes {
		nodes[i] = graphsurface.GraphNode{ID: n.ID, Label: n.Label, Type: n.Type}
	}
	edges := make([]graphsurface.GraphEdge, len(resp.Edges))
	for i, e := range resp.Edges {
		edges[i] = graphsurface.GraphEdge{From: e.From, To: e.To, Kind: e.Kind}
	}
	return graphsurface.GraphProps{Nodes: nodes, Edges: edges}
}
