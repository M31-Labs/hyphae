// Command hypha is the Hyphae CLI.
//
// v0.1 surface:
//
//	hypha index rebuild              walk install root, populate SQLite
//	hypha recall <query>             FTS5 search, summary+anchors output
//	hypha spore submit <file>        validate, write to inbox, emit receipt
//	hypha cap issue ...              issue a local (unsigned) capability token
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/odvcencio/hyphae/internal/capability"
	"github.com/odvcencio/hyphae/internal/db"
	"github.com/odvcencio/hyphae/internal/parser"
	"github.com/odvcencio/hyphae/internal/recall"
	"github.com/odvcencio/hyphae/internal/spore"
	"github.com/odvcencio/hyphae/internal/types"
)

const usage = `hypha — Hyphae v0.1 CLI

Usage:
  hypha index   rebuild [--root <path>]
  hypha recall  <query> [--limit N] [--max-tokens N] [--shape headline|summary+anchors] [--format json|text]
  hypha spore   submit <file>
  hypha cap     issue --subject <uri> --space <uri> [--permissions p1,p2] [--expires 24h]

Environment:
  HYPHAE_HOME    install root (default: $HOME/.hyphae)
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "hypha:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}
	group, rest := args[0], args[1:]
	switch group {
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	case "index":
		return cmdIndex(rest)
	case "recall":
		return cmdRecall(rest)
	case "spore":
		return cmdSpore(rest)
	case "cap":
		return cmdCap(rest)
	default:
		return fmt.Errorf("unknown command %q (try `hypha help`)", group)
	}
}

// --- index rebuild ----------------------------------------------------------

func cmdIndex(args []string) error {
	if len(args) == 0 || args[0] != "rebuild" {
		return errors.New("usage: hypha index rebuild [--root <path>]")
	}
	fs := flag.NewFlagSet("index rebuild", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "install root override (default: HYPHAE_HOME or ~/.hyphae)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	root, err := resolveRoot(*rootFlag)
	if err != nil {
		return err
	}
	dbPath := filepath.Join(root, ".index", "hyphae.db")
	conn, err := db.Open(dbPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	spaces, err := listSpaces(root)
	if err != nil {
		return err
	}
	if len(spaces) == 0 {
		return fmt.Errorf("no spaces found under %s/spaces/", root)
	}

	var totalObj, totalAnc, totalEdg int
	for _, sp := range spaces {
		fmt.Fprintf(os.Stderr, "indexing %s …\n", sp.URI)
		objects, anchors, edges, err := parser.WalkSpace(sp.Path, sp.URI, false)
		if err != nil {
			return fmt.Errorf("walk %s: %w", sp.Path, err)
		}
		if err := recall.IndexBatch(conn, objects); err != nil {
			return fmt.Errorf("index %s: %w", sp.URI, err)
		}
		totalObj += len(objects)
		totalAnc += len(anchors)
		totalEdg += len(edges)
	}

	fmt.Fprintf(os.Stderr, "indexed %d objects, %d anchors, %d edges across %d space(s)\n",
		totalObj, totalAnc, totalEdg, len(spaces))
	fmt.Fprintf(os.Stderr, "db: %s\n", dbPath)
	return nil
}

// --- recall -----------------------------------------------------------------

func cmdRecall(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: hypha recall <query> [flags]")
	}
	fs := flag.NewFlagSet("recall", flag.ContinueOnError)
	limit := fs.Int("limit", 12, "max anchor candidates before budgeting")
	maxTokens := fs.Int("max-tokens", 800, "response token cap")
	shape := fs.String("shape", "summary+anchors", "headline | summary+anchors | count_only")
	format := fs.String("format", "json", "json | text")
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: hypha recall <query> [flags]")
	}
	query := strings.Join(fs.Args(), " ")

	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	conn, err := db.Open(filepath.Join(root, ".index", "hyphae.db"))
	if err != nil {
		return err
	}
	defer conn.Close()

	resp, err := recall.Recall(conn, query, *limit, types.Budget{
		MaxResponseTokens: *maxTokens,
		Shape:             types.ResponseShape(*shape),
	})
	if err != nil {
		return err
	}

	switch *format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(resp)
	case "text":
		fmt.Println(resp.Summary)
		for _, a := range resp.Anchors {
			fmt.Printf("  %s  %s\n", a.URI, a.Title)
		}
		fmt.Fprintf(os.Stderr, "(%d anchors, %d tokens used)\n", len(resp.Anchors), resp.TokensUsed)
		return nil
	default:
		return fmt.Errorf("unknown --format %q (expected json|text)", *format)
	}
}

// --- spore submit -----------------------------------------------------------

func cmdSpore(args []string) error {
	if len(args) == 0 || args[0] != "submit" {
		return errors.New("usage: hypha spore submit <file>")
	}
	if len(args) < 2 {
		return errors.New("usage: hypha spore submit <file>")
	}
	path := args[1]

	source, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	sp, verrs := spore.Parse(source)
	if len(verrs) > 0 {
		for _, e := range verrs {
			fmt.Fprintln(os.Stderr, "  ", e.Error())
		}
		return fmt.Errorf("spore %s failed validation (%d errors)", path, len(verrs))
	}

	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	spaceRoot, err := spaceURIToPath(root, sp.SpaceID)
	if err != nil {
		return err
	}

	filePath, receipt, err := spore.Submit(sp, spaceRoot)
	if err != nil {
		return fmt.Errorf("submit: %w", err)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(map[string]any{
		"receipt":  receipt,
		"filePath": filePath,
	}); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\nReported back to Hyphae: %s\n", filePath)
	return nil
}

// --- cap issue --------------------------------------------------------------

func cmdCap(args []string) error {
	if len(args) == 0 || args[0] != "issue" {
		return errors.New("usage: hypha cap issue --subject <uri> --space <uri> [--permissions p1,p2] [--expires 24h]")
	}
	fs := flag.NewFlagSet("cap issue", flag.ContinueOnError)
	subject := fs.String("subject", "", "identity URI for the token subject")
	space := fs.String("space", "", "hypha:// URI of the target space")
	perms := fs.String("permissions", "memory:recall,spore:create", "comma-separated permission names")
	expiresFlag := fs.String("expires", "24h", "token lifetime (Go duration)")
	maxRecall := fs.Int("max-recall-results", 25, "limits.max_recall_results")
	maxResponseTokens := fs.Int("max-response-tokens", 800, "limits.max_response_tokens")
	maxSpores := fs.Int("max-spores", 3, "limits.max_spores")
	maxBytes := fs.Int("max-bytes", 200000, "limits.max_bytes")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *subject == "" || *space == "" {
		return errors.New("--subject and --space are required")
	}
	expires, err := time.ParseDuration(*expiresFlag)
	if err != nil {
		return fmt.Errorf("--expires %q: %w", *expiresFlag, err)
	}
	permList := splitCSV(*perms)
	if len(permList) == 0 {
		return errors.New("--permissions must list at least one permission")
	}

	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	conn, err := db.Open(filepath.Join(root, ".index", "hyphae.db"))
	if err != nil {
		return err
	}
	defer conn.Close()

	cap, err := capability.Issue(conn, *subject, *space, permList, types.Limits{
		MaxRecallResults:  *maxRecall,
		MaxResponseTokens: *maxResponseTokens,
		MaxSpores:         *maxSpores,
		MaxBytes:          *maxBytes,
	}, expires)
	if err != nil {
		return err
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{
		"token":      cap.ID,
		"capability": cap,
	})
}

// --- helpers ---------------------------------------------------------------

type spaceEntry struct {
	URI  string // e.g. "m31labs/hyphae"
	Path string // absolute filesystem path
}

func resolveRoot(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	return db.Root()
}

// listSpaces enumerates immediate children of <root>/spaces/. Each subdir
// named "<authority>-<name>" becomes URI "<authority>/<name>".
func listSpaces(root string) ([]spaceEntry, error) {
	dir := filepath.Join(root, "spaces")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read spaces dir %s: %w", dir, err)
	}
	out := make([]spaceEntry, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// "<authority>-<name>" → "<authority>/<name>" (single hyphen split)
		uri := name
		if i := strings.Index(name, "-"); i > 0 {
			uri = name[:i] + "/" + name[i+1:]
		}
		out = append(out, spaceEntry{URI: uri, Path: filepath.Join(dir, name)})
	}
	return out, nil
}

// spaceURIToPath maps "hypha://<authority>/<name>" or "<authority>/<name>"
// to "<root>/spaces/<authority>-<name>/". Refuses unknown spaces.
func spaceURIToPath(root, uri string) (string, error) {
	stripped := strings.TrimPrefix(uri, "hypha://")
	parts := strings.SplitN(stripped, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("invalid space URI %q", uri)
	}
	dir := filepath.Join(root, "spaces", parts[0]+"-"+parts[1])
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return "", fmt.Errorf("space %q not installed at %s", uri, dir)
	}
	return dir, nil
}

// reorderFlagsFirst moves -flag/--flag arguments to the front so stdlib
// `flag.Parse` (which stops at the first positional) accepts flag/positional
// in any order. Heuristic: every flag is assumed to take a value unless it
// uses `--flag=value` form or is the last arg.
func reorderFlagsFirst(args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		pos = append(pos, a)
	}
	return append(flags, pos...)
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
