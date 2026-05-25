// Command hypha is the Hyphae CLI.
//
// v0.1.1 surface:
//
//	hypha index    rebuild              walk install root, populate SQLite
//	hypha recall   <query>              FTS5 search, summary+anchors output
//	hypha spore    submit <file>        validate, write to inbox, emit + persist receipt
//	hypha cap      issue ...            issue a local capability token + persist receipt
//	hypha identity init --name X ...    generate an Ed25519 identity + .key sidecar
//	hypha identity list                 list identities in the org catalog
//	hypha graft    <spore-id> --as <id> apply spore proposed_writes to canonical files
//	hypha receipts list [filters]       query the audit log
package main

import (
	"database/sql"
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
	"github.com/odvcencio/hyphae/internal/graft"
	"github.com/odvcencio/hyphae/internal/identity"
	"github.com/odvcencio/hyphae/internal/parser"
	"github.com/odvcencio/hyphae/internal/recall"
	"github.com/odvcencio/hyphae/internal/receipts"
	"github.com/odvcencio/hyphae/internal/spore"
	"github.com/odvcencio/hyphae/internal/types"
)

const usage = `hypha — Hyphae v0.1.1 CLI

Usage:
  hypha index    rebuild [--root <path>]
  hypha recall   <query> [--limit N] [--max-tokens N] [--shape headline|summary+anchors] [--format json|text]
  hypha spore    submit <file>
  hypha cap      issue --subject <uri> --space <uri> [--permissions p1,p2] [--expires 24h]
  hypha identity init --name <name> --authority <auth> --space <uri> [--expires 1y]
  hypha identity list
  hypha graft    <spore-id> --as <identity-uri> [--space <hypha-uri>]
  hypha receipts list [--space <uri>] [--subject <uri>] [--action <name>] [--since 24h] [--limit N]

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
	case "identity":
		return cmdIdentity(rest)
	case "graft":
		return cmdGraft(rest)
	case "receipts":
		return cmdReceipts(rest)
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

	// Persist the receipt to the audit log.
	if conn, dbErr := openIndex(root); dbErr == nil {
		defer conn.Close()
		if wErr := receipts.Write(conn, receipt); wErr != nil && !errors.Is(wErr, receipts.ErrAlreadyExists) {
			fmt.Fprintf(os.Stderr, "warn: failed to persist receipt: %v\n", wErr)
		}
	} else {
		fmt.Fprintf(os.Stderr, "warn: receipt not persisted (index unavailable: %v)\n", dbErr)
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

	// Record an audit receipt for the issuance.
	rcpt := types.Receipt{
		ID:              "hypha-receipt:" + time.Now().UTC().Format("2006-01-02") + ":capissue-" + cap.ID,
		SpaceID:         cap.SpaceID,
		SubjectID:       cap.Subject,
		SubjectKind:     "agent",
		Action:          "cap:issue",
		Status:          "issued",
		ContentHash:     "",
		IdentityID:      cap.IssuedBy,
		CreatedAt:       cap.IssuedAt,
		PermissionsUsed: []string{"permission:grant"},
		NextState:       "active",
	}
	if wErr := receipts.Write(conn, rcpt); wErr != nil && !errors.Is(wErr, receipts.ErrAlreadyExists) {
		fmt.Fprintf(os.Stderr, "warn: failed to persist cap:issue receipt: %v\n", wErr)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{
		"token":      cap.ID,
		"capability": cap,
	})
}

// --- identity --------------------------------------------------------------

func cmdIdentity(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: hypha identity init|list")
	}
	switch args[0] {
	case "init":
		return cmdIdentityInit(args[1:])
	case "list":
		return cmdIdentityList(args[1:])
	default:
		return fmt.Errorf("unknown identity subcommand %q", args[0])
	}
}

func cmdIdentityInit(args []string) error {
	fs := flag.NewFlagSet("identity init", flag.ContinueOnError)
	name := fs.String("name", "", "bare username (e.g. odvcencio)")
	authority := fs.String("authority", "", "URI authority (e.g. m31labs)")
	space := fs.String("space", "", "owning space URI (e.g. hypha://m31labs/hyphae)")
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}
	if *name == "" || *authority == "" || *space == "" {
		return errors.New("--name, --authority, and --space are required")
	}

	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	dir := filepath.Join(root, ".catalog", "identities")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	id, priv, err := identity.Generate(*authority, *name, *space)
	if err != nil {
		return fmt.Errorf("generate: %w", err)
	}
	mdPath, keyPath, err := identity.Save(dir, id, priv)
	if err != nil {
		return fmt.Errorf("save: %w", err)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{
		"identity":     id,
		"identityFile": mdPath,
		"privateKey":   keyPath + " (mode 0600)",
	})
}

func cmdIdentityList(args []string) error {
	_ = args
	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	dir := filepath.Join(root, ".catalog", "identities")

	list, err := identity.List(dir)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintf(os.Stderr, "no identities found at %s\n", dir)
		return nil
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(list)
}

// --- graft -----------------------------------------------------------------

func cmdGraft(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: hypha graft <spore-id> --as <identity-uri> [--space <hypha-uri>]")
	}
	fs := flag.NewFlagSet("graft", flag.ContinueOnError)
	grafter := fs.String("as", "", "grafter identity URI (recorded in the receipt)")
	spaceURI := fs.String("space", "", "space URI override (auto-detected from inbox if omitted)")
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: hypha graft <spore-id> --as <identity-uri> [--space <hypha-uri>]")
	}
	sporeID := fs.Arg(0)
	if *grafter == "" {
		return errors.New("--as <identity-uri> is required")
	}

	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	conn, err := openIndex(root)
	if err != nil {
		return err
	}
	defer conn.Close()

	// Find the spore's space root: either explicit --space, or scan all spaces.
	var spaceRoot string
	if *spaceURI != "" {
		spaceRoot, err = spaceURIToPath(root, *spaceURI)
		if err != nil {
			return err
		}
	} else {
		spaceRoot, err = findSporeSpaceRoot(root, sporeID)
		if err != nil {
			return err
		}
	}

	result, err := graft.Apply(conn, root, spaceRoot, sporeID, *grafter)
	if err != nil {
		return fmt.Errorf("graft: %w", err)
	}

	// Persist the graft receipt to the audit log.
	if wErr := receipts.Write(conn, result.Receipt); wErr != nil && !errors.Is(wErr, receipts.ErrAlreadyExists) {
		fmt.Fprintf(os.Stderr, "warn: failed to persist graft receipt: %v\n", wErr)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\nGrafted %s → status: %s (applied %d, skipped %d)\n",
		result.SporeID, result.NewSporeStatus, len(result.AppliedWrites), len(result.SkippedWrites))
	return nil
}

// findSporeSpaceRoot scans every space's inbox/agents/ for a spore whose
// frontmatter id matches sporeID. Returns the space root on first match.
func findSporeSpaceRoot(root, sporeID string) (string, error) {
	spaces, err := listSpaces(root)
	if err != nil {
		return "", err
	}
	for _, s := range spaces {
		inbox := filepath.Join(s.Path, "inbox", "agents")
		entries, err := os.ReadDir(inbox)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(inbox, e.Name()))
			if err != nil {
				continue
			}
			if bytesContainsID(data, sporeID) {
				return s.Path, nil
			}
		}
	}
	return "", fmt.Errorf("spore %q not found in any installed space's inbox/agents/ (try --space)", sporeID)
}

// bytesContainsID looks for a frontmatter `id: <sporeID>` line. Simple
// substring match is enough for v0.1.1 — collision risk is negligible.
func bytesContainsID(data []byte, sporeID string) bool {
	return strings.Contains(string(data), "id: "+sporeID+"\n") ||
		strings.Contains(string(data), "id: "+sporeID+"\r\n")
}

// --- receipts list ---------------------------------------------------------

func cmdReceipts(args []string) error {
	if len(args) == 0 || args[0] != "list" {
		return errors.New("usage: hypha receipts list [--space <uri>] [--subject <uri>] [--action <name>] [--since 24h] [--limit N]")
	}
	fs := flag.NewFlagSet("receipts list", flag.ContinueOnError)
	spaceID := fs.String("space", "", "filter by space URI")
	subject := fs.String("subject", "", "filter by subject id")
	action := fs.String("action", "", "filter by action (e.g. spore:create, graft, cap:issue)")
	since := fs.String("since", "", "Go duration; receipts created within the last N units")
	limit := fs.Int("limit", 50, "max results")
	if err := fs.Parse(reorderFlagsFirst(args[1:])); err != nil {
		return err
	}

	var sinceT time.Time
	if *since != "" {
		d, err := time.ParseDuration(*since)
		if err != nil {
			return fmt.Errorf("--since %q: %w", *since, err)
		}
		sinceT = time.Now().UTC().Add(-d)
	}

	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	conn, err := openIndex(root)
	if err != nil {
		return err
	}
	defer conn.Close()

	out, err := receipts.List(conn, receipts.ListFilter{
		SpaceID:   *spaceID,
		SubjectID: *subject,
		Action:    *action,
		Since:     sinceT,
		Limit:     *limit,
	})
	if err != nil {
		return err
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// openIndex opens the SQLite index at the default path under root.
func openIndex(root string) (*sql.DB, error) {
	return db.Open(filepath.Join(root, ".index", "hyphae.db"))
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
