// Command hypha-viz is a local knowledge-graph visualization server for Hyphae.
//
// It opens the Hyphae SQLite index and spins up a GoSX server with a
// force-directed graph viewer, a search bar, and a detail panel — all
// rendered server-side with no external JS dependencies.
//
// Usage:
//
//	hypha-viz [--addr 127.0.0.1:7777] [--root <hyphae-home>]
//
// Visit http://127.0.0.1:7777 after starting.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/odvcencio/gosx"
	"github.com/odvcencio/gosx/server"
	"github.com/odvcencio/hyphae/internal/db"
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

	// Build the GoSX app.
	app := server.New()

	// Disable the default public-dir serving (no public/ dir in this binary).
	app.SetPublicDir("")

	// Page route: GET / — full graph viewer shell.
	app.Route("/", func(r *http.Request) gosx.Node {
		return BuildGraphPage()
	})

	// API: full or subgraph JSON.
	app.API("GET /api/graph", handleGraph(conn))

	// API: FTS5 search.
	app.API("GET /api/search", handleSearch(conn))

	// API: object detail by path param.
	app.API("GET /api/object/{id}", handleObject(conn))

	fmt.Printf("hypha-viz listening on http://%s\n", *addr)
	fmt.Printf("  graph db: %s\n", dbPath)
	log.Fatal(app.ListenAndServe(*addr))
	return nil
}
