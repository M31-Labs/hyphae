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
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"m31labs.dev/hyphae/internal/analyze"
	"m31labs.dev/hyphae/internal/assess"
	"m31labs.dev/hyphae/internal/atomicfs"
	"m31labs.dev/hyphae/internal/capability"
	"m31labs.dev/hyphae/internal/crdtconflict"
	"m31labs.dev/hyphae/internal/crdtshadow"
	"m31labs.dev/hyphae/internal/db"
	"m31labs.dev/hyphae/internal/envelope"
	"m31labs.dev/hyphae/internal/graft"
	"m31labs.dev/hyphae/internal/graph"
	"m31labs.dev/hyphae/internal/hubsync"
	"m31labs.dev/hyphae/internal/identity"
	"m31labs.dev/hyphae/internal/mcp"
	"m31labs.dev/hyphae/internal/parser"
	"m31labs.dev/hyphae/internal/peers"
	"m31labs.dev/hyphae/internal/pulse"
	"m31labs.dev/hyphae/internal/recall"
	"m31labs.dev/hyphae/internal/receipts"
	"m31labs.dev/hyphae/internal/spore"
	"m31labs.dev/hyphae/internal/syncbundle"
	"m31labs.dev/hyphae/internal/trace"
	"m31labs.dev/hyphae/internal/types"
	mdppfmt "m31labs.dev/mdpp/fmt"
)

const hyphaeVersion = "0.1.8"

const usage = `hypha — Hyphae v0.1.8 CLI

Usage:
  hypha index    rebuild [--root <path>]
  hypha recall   <query> [--limit N] [--max-tokens N] [--shape headline|summary+anchors] [--format text|json|compact]
  hypha show     <id-or-hypha-uri> [--path] [--json] [--frontmatter] [--body]
  hypha spaces   list [--format text|json|compact]
  hypha spore    submit <file> [--sign --as <identity-uri>] [--format text|json|compact]
  hypha spore    list   [--space <uri>] [--status <state>] [--since 24h] [--limit N] [--format text|json|compact]
  hypha spore    accept <spore-id> --as <identity> [--reason "..."] [--space <uri>] [--format text|json|compact]
  hypha spore    reject <spore-id> --as <identity> [--reason "..."] [--space <uri>] [--format text|json|compact]
  hypha cap      issue --subject <uri> --space <uri> [--permissions p1,p2] [--expires 24h] [--format text|json|compact]
  hypha identity init --name <name> --authority <auth> --space <uri> [--expires 1y] [--format text|json|compact]
  hypha identity list [--format text|json|compact]
  hypha graft    <spore-id> --as <identity-uri> [--space <hypha-uri>] [--verify] [--no-fmt] [--dry-run] [--diff] [--apply] [--format text|json|compact]
  hypha graph    backlinks <object-id> [--kind k1,k2] [--limit N] [--format text|json|compact]
  hypha graph    related   <object-id> [--kind k1,k2] [--limit N] [--format text|json|compact]
  hypha graph    trace     <object-id> [--kind derived_from,cites] [--max-depth 4] [--format text|json|compact]
  hypha pulse    [--space <uri>] [--window 30d] [--ttl 5m] [--format text|json|compact]
  hypha assess   change --task <text> [--files p1,p2] [--diff-summary <text>] [--space <uri>] [--source <path>] [--format text|json|compact]
  hypha assess   task   --task <text> [--space <uri>] [--format text|json|compact]
  hypha assess   pr     --task <text> --base <ref> [--space <uri>] [--source <path>] [--format text|json|compact]
  hypha trace    start  --agent <uri> [--task <id>] [--phase <text>] [--space <uri>] [--format text|json|compact]
  hypha trace    tick   <trace-id> "<checkpoint>" [--space <uri>] [--format text|json|compact]
  hypha trace    done   <trace-id> [--status succeeded|failed|killed|superseded] [--link-spore <id>] [--space <uri>] [--format text|json|compact]
  hypha trace    list   [--active] [--agent <uri>] [--space <uri>] [--format text|json|compact]
  hypha trace    history [--similar <q>] [--task <id>] [--agent <uri>] [--include-open] [--limit N] [--space <uri>] [--format text|json|compact]
  hypha trace    tail   [--id <trace-id>] [--agent <uri>] [--interval 1s] [--timeout 5m] [--space <uri>]
  hypha trace    reap   [--older-than 1h] [--space <uri>] [--format text|json|compact]
  hypha analyze  <kind> [target] [--space <uri>] [--source <path>] [--diff-ref <ref>] [--max-depth N] [--refresh]
                       kinds: impact, callgraph, refs, hotspot, dead, review
  hypha analyze  list   [--kind <k>] [--space <uri>] [--target-file <path>] [--format text|json|compact]
  hypha analyze  refresh <id> [--space <uri>] [--source <path>]
  hypha db       history  --space <uri> [--limit N] [--format text|json|compact]
  hypha db       compact  --space <uri> [--format text|json|compact]
  hypha sync     export   --space <uri> [--out <file>] [--format text|json|compact]
  hypha sync     import   [--space <uri>] [--in <file>] [--format text|json|compact]
  hypha sync     pull     --peer <ws-url> --space <uri> [--token X] [--once] [--timeout 30s] [--format text|json|compact]
  hypha hub      serve    [--addr 127.0.0.1:7777] [--require-auth] [--admin] [--base-url <url>]
  hypha conflict list     --space <uri> [--format text|json|compact]
  hypha conflict show     <id> --space <uri> [--format text|json|compact]
  hypha conflict resolve  <id> --space <uri> --keep <actor-prefix> [--format text|json|compact]
  hypha peer     add      <uri> [--name <name>] [--format text|json|compact]
  hypha peer     list     [--format text|json|compact]
  hypha peer     remove   <name-or-uri> [--format text|json|compact]
  hypha receipts list   [--space <uri>] [--subject <uri>] [--action <name>] [--since 24h] [--limit N] [--format text|json|compact]
  hypha mcp      serve                              MCP stdio server (JSON-RPC 2.0; read-only tools)

Separate binary for the browser visualization (GoSX-based):
  hypha-viz       [--addr 127.0.0.1:7777] [--root <hyphae-home>]

Output formats:
  --format text       human-readable. Default when stdout is a terminal.
  --format json       full-key indented JSON envelope (Envelope schema v1).
  --format compact    same data, single-line + documented short-key map.
                      Default when stdout is piped or redirected.
  HYPHAE_FORMAT       env override for the auto-detected default.

Environment:
  HYPHAE_HOME                  install root (default: $HOME/.hyphae)
  HYPHAE_FORMAT                default output format when --format is not given
  HYPHAE_GITHUB_CLIENT_ID      GitHub OAuth app client id. With CLIENT_SECRET
                               and 'hub serve --admin --base-url', gates the
                               admin UI behind GitHub login.
  HYPHAE_GITHUB_CLIENT_SECRET  GitHub OAuth app client secret
  HYPHAE_ADMIN_LOGINS          comma-separated GitHub logins allowed admin
                               access (empty = no one); required for the gate
`

func main() {
	envelope.SetHyphaeVersion(hyphaeVersion)
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "hypha:", err)
		os.Exit(1)
	}
}

// formatFlag declares the standard --format flag on fs. Pass the result to
// envelope.ParseFormat after fs.Parse to resolve text|json|compact (and
// auto-detect when blank).
func formatFlag(fs *flag.FlagSet) *string {
	return fs.String("format", "", "text | json | compact (default: text on TTY, compact on pipe)")
}

// emit is a tiny convenience wrapper around envelope.Emit. The text
// renderer can be nil for commands that do not yet have a human view.
func emit(command string, data any, formatStr string, text envelope.TextRenderer) error {
	f, err := envelope.ParseFormat(formatStr)
	if err != nil {
		return err
	}
	return envelope.Emit(os.Stdout, envelope.New(command, data), f, text)
}

// emitErr writes a typed error envelope for command and returns the error
// the caller should propagate (so the binary still exits non-zero).
func emitErr(command string, formatStr string, code, message, hint string) error {
	f, ferr := envelope.ParseFormat(formatStr)
	if ferr != nil {
		f = envelope.AutoDetect()
	}
	env := envelope.NewError(command, envelope.Note{Code: code, Message: message, Hint: hint})
	_ = envelope.Emit(os.Stdout, env, f, nil)
	return errors.New(message)
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
	case "graph":
		return cmdGraph(rest)
	case "pulse":
		return cmdPulse(rest)
	case "assess":
		return cmdAssess(rest)
	case "show":
		return cmdShow(rest)
	case "spaces":
		return cmdSpaces(rest)
	case "trace":
		return cmdTrace(rest)
	case "analyze":
		return cmdAnalyze(rest)
	case "db":
		return cmdDB(rest)
	case "sync":
		return cmdSync(rest)
	case "peer":
		return cmdPeer(rest)
	case "hub":
		return cmdHub(rest)
	case "conflict":
		return cmdConflict(rest)
	case "mcp":
		return cmdMCP(rest)
	default:
		return fmt.Errorf("unknown command %q (try `hypha help`)", group)
	}
}

// --- db --------------------------------------------------------------------

func cmdDB(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: hypha db history|compact [...]")
	}
	switch args[0] {
	case "history":
		return cmdDBHistory(args[1:])
	case "compact":
		return cmdDBCompact(args[1:])
	default:
		return fmt.Errorf("unknown db subcommand %q (try `history`, `compact`)", args[0])
	}
}

func cmdDBHistory(args []string) error {
	fs := flag.NewFlagSet("db history", flag.ContinueOnError)
	spaceFlag := fs.String("space", "", "space URI (required)")
	limit := fs.Int("limit", 50, "max rows (0 = all)")
	format := formatFlag(fs)
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}
	if *spaceFlag == "" {
		return errors.New("--space <uri> is required")
	}
	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	sh, err := crdtshadow.Default.Get(mustResolveSpace(root, *spaceFlag), *spaceFlag)
	if err != nil {
		return err
	}
	rows, err := sh.Store().History(*limit)
	if err != nil {
		return err
	}
	heads, _ := sh.Store().Heads()
	count, _ := sh.Store().CountChanges()
	payload := map[string]any{
		"space":         *spaceFlag,
		"db":            sh.DBPath(),
		"total_changes": count,
		"heads":         heads,
		"rows":          rows,
	}
	return emit("db history", payload, *format, func(w io.Writer, _ any) error {
		fmt.Fprintf(w, "Space: %s  (%d change(s) total)\n", *spaceFlag, count)
		fmt.Fprintf(w, "  db:    %s\n", sh.DBPath())
		fmt.Fprintf(w, "  heads: %s\n", strings.Join(heads, ", "))
		if len(rows) == 0 {
			fmt.Fprintln(w, "  (no changes)")
			return nil
		}
		fmt.Fprintln(w)
		for _, r := range rows {
			short := r.Hash
			if len(short) > 12 {
				short = short[:12]
			}
			fmt.Fprintf(w, "  %s  %s  %s  seq=%d  %s\n",
				r.Time.Format(time.RFC3339), short, r.ActorID[:min(8, len(r.ActorID))], r.Seq, r.Message)
		}
		return nil
	})
}

func cmdDBCompact(args []string) error {
	fs := flag.NewFlagSet("db compact", flag.ContinueOnError)
	spaceFlag := fs.String("space", "", "space URI (required)")
	format := formatFlag(fs)
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}
	if *spaceFlag == "" {
		return errors.New("--space <uri> is required")
	}
	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	sh, err := crdtshadow.Default.Get(mustResolveSpace(root, *spaceFlag), *spaceFlag)
	if err != nil {
		return err
	}
	if err := sh.Store().Compact(sh.Doc()); err != nil {
		return err
	}
	snapshots, _ := sh.Store().CountSnapshots()
	payload := map[string]any{
		"space":     *spaceFlag,
		"db":        sh.DBPath(),
		"snapshots": snapshots,
	}
	return emit("db compact", payload, *format, func(w io.Writer, _ any) error {
		fmt.Fprintf(w, "Compacted %s\n", *spaceFlag)
		fmt.Fprintf(w, "  db:        %s\n", sh.DBPath())
		fmt.Fprintf(w, "  snapshots: %d\n", snapshots)
		return nil
	})
}

func mustResolveSpace(installRoot, spaceURI string) string {
	p, err := crdtshadow.SpaceURIToPath(installRoot, spaceURI)
	if err != nil {
		return filepath.Join(installRoot, "spaces", "unknown")
	}
	return p
}

// --- sync ------------------------------------------------------------------

func cmdSync(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: hypha sync export|import|pull [...]")
	}
	switch args[0] {
	case "export":
		return cmdSyncExport(args[1:])
	case "import":
		return cmdSyncImport(args[1:])
	case "pull":
		return cmdSyncPull(args[1:])
	default:
		return fmt.Errorf("unknown sync subcommand %q (try `export`, `import`, `pull`)", args[0])
	}
}

func cmdSyncPull(args []string) error {
	fs := flag.NewFlagSet("sync pull", flag.ContinueOnError)
	peer := fs.String("peer", "", "peer base URL, e.g. ws://hub.internal:7777 (required)")
	spaceFlag := fs.String("space", "", "space URI (required)")
	token := fs.String("token", "", "Bearer token if the hub requires auth")
	once := fs.Bool("once", true, "single sync pass (default); pass --once=false to stay connected")
	timeout := fs.Duration("timeout", 30*time.Second, "abort if the exchange doesn't finish in this duration")
	format := formatFlag(fs)
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}
	if *peer == "" || *spaceFlag == "" {
		return errors.New("--peer and --space are required")
	}
	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	sh, err := crdtshadow.Default.Get(mustResolveSpace(root, *spaceFlag), *spaceFlag)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	stats, err := hubsync.Pull(ctx, hubsync.SchemeForBase(*peer), *spaceFlag, *token, sh, *once)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return emit("sync pull", stats, *format, func(w io.Writer, _ any) error {
		fmt.Fprintf(w, "Pulled from %s\n", *peer)
		fmt.Fprintf(w, "  space:    %s\n", *spaceFlag)
		fmt.Fprintf(w, "  frames:   sent=%d received=%d\n", stats.FramesSent, stats.FramesRecv)
		fmt.Fprintf(w, "  bytes:    sent=%d received=%d\n", stats.BytesSent, stats.BytesReceived)
		fmt.Fprintf(w, "  changes:  %d → %d (Δ %d)\n", stats.ChangesBefore, stats.ChangesAfter, stats.ChangesAfter-stats.ChangesBefore)
		return nil
	})
}

// --- conflict --------------------------------------------------------------

func cmdConflict(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: hypha conflict list|show|resolve [...]")
	}
	switch args[0] {
	case "list":
		return cmdConflictList(args[1:])
	case "show":
		return cmdConflictShow(args[1:])
	case "resolve":
		return cmdConflictResolve(args[1:])
	default:
		return fmt.Errorf("unknown conflict subcommand %q (try `list`, `show`, `resolve`)", args[0])
	}
}

func cmdConflictList(args []string) error {
	fs := flag.NewFlagSet("conflict list", flag.ContinueOnError)
	spaceFlag := fs.String("space", "", "space URI (required)")
	format := formatFlag(fs)
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}
	if *spaceFlag == "" {
		return errors.New("--space <uri> is required")
	}
	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	sh, err := crdtshadow.Default.Get(mustResolveSpace(root, *spaceFlag), *spaceFlag)
	if err != nil {
		return err
	}
	conflicts, err := crdtconflict.Detect(sh.Store())
	if err != nil {
		return err
	}
	return emit("conflict list", conflicts, *format, func(w io.Writer, _ any) error {
		if len(conflicts) == 0 {
			fmt.Fprintln(w, "(no conflicts)")
			return nil
		}
		for _, c := range conflicts {
			actors := make([]string, 0, len(c.Entries))
			for _, e := range c.Entries {
				short := e.ActorID
				if len(short) > 8 {
					short = short[:8]
				}
				actors = append(actors, short)
			}
			scope := c.Prefix
			tail := c.Tail
			if scope == "" {
				scope = "?"
				tail = c.Key
			}
			fmt.Fprintf(w, "  %-32s  %s  [%s]\n", c.ID, scope+":"+tail, strings.Join(actors, ", "))
		}
		fmt.Fprintf(w, "\n(%d conflict(s))\n", len(conflicts))
		return nil
	})
}

func cmdConflictShow(args []string) error {
	fs := flag.NewFlagSet("conflict show", flag.ContinueOnError)
	spaceFlag := fs.String("space", "", "space URI (required)")
	format := formatFlag(fs)
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}
	if *spaceFlag == "" || fs.NArg() == 0 {
		return errors.New("usage: hypha conflict show <id> --space <uri>")
	}
	needle := fs.Arg(0)
	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	sh, err := crdtshadow.Default.Get(mustResolveSpace(root, *spaceFlag), *spaceFlag)
	if err != nil {
		return err
	}
	conflicts, err := crdtconflict.Detect(sh.Store())
	if err != nil {
		return err
	}
	c, err := crdtconflict.Find(conflicts, needle)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"id":      c.ID,
		"key":     c.Key,
		"prefix":  c.Prefix,
		"tail":    c.Tail,
		"entries": c.Entries,
	}
	return emit("conflict show", payload, *format, func(w io.Writer, _ any) error {
		fmt.Fprintf(w, "Conflict: %s\n", c.ID)
		fmt.Fprintf(w, "  scope: %s : %s\n\n", c.Prefix, c.Tail)
		for _, e := range c.Entries {
			short := e.ActorID
			if len(short) > 8 {
				short = short[:8]
			}
			fmt.Fprintf(w, "  ── actor %s ──  %s  (%d bytes)\n", short, e.Time.Format(time.RFC3339), e.ValueLen)
			fmt.Fprintf(w, "    change: %s\n", e.ChangeHash[:16])
			if e.Message != "" {
				fmt.Fprintf(w, "    msg:    %s\n", e.Message)
			}
			// Print a small preview of the value if it looks like text.
			if e.ValueLen > 0 && e.ValueLen < 400 && isTextish(e.Value) {
				fmt.Fprintf(w, "    bytes:\n")
				for _, line := range strings.Split(strings.TrimRight(string(e.Value), "\n"), "\n") {
					fmt.Fprintf(w, "      | %s\n", line)
				}
			}
			fmt.Fprintln(w)
		}
		return nil
	})
}

func cmdConflictResolve(args []string) error {
	fs := flag.NewFlagSet("conflict resolve", flag.ContinueOnError)
	spaceFlag := fs.String("space", "", "space URI (required)")
	keep := fs.String("keep", "", "actor id (prefix) whose value should win")
	format := formatFlag(fs)
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}
	if *spaceFlag == "" || fs.NArg() == 0 || *keep == "" {
		return errors.New("usage: hypha conflict resolve <id> --space <uri> --keep <actor-prefix>")
	}
	needle := fs.Arg(0)
	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	sh, err := crdtshadow.Default.Get(mustResolveSpace(root, *spaceFlag), *spaceFlag)
	if err != nil {
		return err
	}
	conflicts, err := crdtconflict.Detect(sh.Store())
	if err != nil {
		return err
	}
	c, err := crdtconflict.Find(conflicts, needle)
	if err != nil {
		return err
	}
	entry, err := crdtconflict.PickEntry(c, *keep)
	if err != nil {
		return err
	}
	if err := sh.ResolveConflict(c.Key, entry.Value); err != nil {
		return err
	}
	// Re-materialize any canonical sections this affected.
	materialized, _ := sh.MaterializeAll()

	payload := map[string]any{
		"id":                 c.ID,
		"key":                c.Key,
		"kept_actor":         entry.ActorID,
		"kept_change":        entry.ChangeHash,
		"materialized_files": materialized,
	}
	return emit("conflict resolve", payload, *format, func(w io.Writer, _ any) error {
		fmt.Fprintf(w, "Resolved %s\n", c.ID)
		short := entry.ActorID
		if len(short) > 8 {
			short = short[:8]
		}
		fmt.Fprintf(w, "  kept:     actor=%s change=%s\n", short, entry.ChangeHash[:16])
		if len(materialized) > 0 {
			fmt.Fprintf(w, "  files:    %d canonical file(s) re-materialized\n", len(materialized))
			for _, p := range materialized {
				fmt.Fprintf(w, "    - %s\n", p)
			}
		}
		return nil
	})
}

func isTextish(b []byte) bool {
	for _, c := range b {
		if c == '\n' || c == '\t' || c == '\r' {
			continue
		}
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

// --- hub -------------------------------------------------------------------

func cmdHub(args []string) error {
	if len(args) == 0 || args[0] != "serve" {
		return errors.New("usage: hypha hub serve [flags]")
	}
	return cmdHubServe(args[1:])
}

func cmdHubServe(args []string) error {
	fs := flag.NewFlagSet("hub serve", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:7777", "listen address")
	requireAuth := fs.Bool("require-auth", false, "require a valid cap-token Bearer header on every connection")
	admin := fs.Bool("admin", false, "mount the server-rendered admin UI at /admin (keys, peers, audit)")
	baseURL := fs.String("base-url", "", "public base URL of this hub (e.g. https://hub.example.com), used for the GitHub OAuth callback")
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}

	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	spaces, err := listSpaces(root)
	if err != nil {
		return err
	}

	// The admin surface needs the index DB (capabilities + receipts) even
	// when --require-auth is off, so open it for either flag.
	var authConn *sql.DB
	if *requireAuth || *admin {
		conn, dbErr := openIndex(root)
		if dbErr != nil {
			return fmt.Errorf("hub serve: open auth DB: %w", dbErr)
		}
		defer conn.Close()
		authConn = conn
	}

	srv := hubsync.NewServer(root, crdtshadow.Default, authConn, *requireAuth)
	registered := 0
	for _, sp := range spaces {
		uri := "hypha://" + sp.URI
		if err := srv.Register(uri); err != nil {
			fmt.Fprintf(os.Stderr, "warn: register %s: %v\n", uri, err)
			continue
		}
		registered++
	}
	if registered == 0 {
		return fmt.Errorf("hub serve: no spaces could be registered under %s", root)
	}

	// Mount the admin UI (+ optional GitHub OAuth gate) on the same mux.
	var oauthEnabled bool
	if *admin {
		oauth := hubsync.NewOAuth(hubsync.OAuthConfig{
			ClientID:     os.Getenv("HYPHAE_GITHUB_CLIENT_ID"),
			ClientSecret: os.Getenv("HYPHAE_GITHUB_CLIENT_SECRET"),
			BaseURL:      *baseURL,
			AdminLogins:  splitCSV(os.Getenv("HYPHAE_ADMIN_LOGINS")),
		})
		// OAuth needs a base URL for its callback; disable it (the admin
		// surface then runs ungated, local-only) if one wasn't provided.
		if oauth != nil && *baseURL == "" {
			fmt.Fprintln(os.Stderr, "hub: GitHub OAuth env set but --base-url missing; OAuth gate disabled (admin is ungated — bind 127.0.0.1 only)")
			oauth = nil
		}
		if oauth != nil && oauth.AllowedCount() == 0 {
			fmt.Fprintln(os.Stderr, "hub: warning: GitHub OAuth gate enabled but HYPHAE_ADMIN_LOGINS is empty — no one will be granted admin access")
		}
		oauthEnabled = oauth != nil
		hubsync.NewAdmin(root, authConn, srv, oauth).Mount(srv.Mux())
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	ready := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx, *addr, ready) }()

	select {
	case actual := <-ready:
		fmt.Fprintf(os.Stderr, "hub: listening on %s  (%d space(s))\n", actual, registered)
		if *requireAuth {
			fmt.Fprintln(os.Stderr, "hub: auth required (Bearer token)")
		}
		if *admin {
			fmt.Fprintf(os.Stderr, "hub: admin UI at %s/admin\n", strings.TrimRight(nonEmpty(*baseURL, "http://"+actual), "/"))
			if oauthEnabled {
				fmt.Fprintln(os.Stderr, "hub: admin gated by GitHub OAuth (allowlist)")
			} else {
				fmt.Fprintln(os.Stderr, "hub: admin UNGATED — keep this bound to 127.0.0.1 or front it with auth")
			}
		}
		for _, sp := range spaces {
			fmt.Fprintf(os.Stderr, "  hypha://%s → %s%s%s\n",
				sp.URI,
				schemeForListenAddr(actual),
				hubsync.PathPrefix,
				urlEscape("hypha://"+sp.URI),
			)
		}
	case err := <-errCh:
		return err
	}

	return <-errCh
}

func schemeForListenAddr(addr string) string {
	return "ws://" + addr
}

func urlEscape(s string) string {
	return url.PathEscape(s)
}

func cmdSyncExport(args []string) error {
	fs := flag.NewFlagSet("sync export", flag.ContinueOnError)
	spaceFlag := fs.String("space", "", "space URI (required)")
	out := fs.String("out", "", "output file path; `-` for stdout (default: <space>.bundle in cwd)")
	format := formatFlag(fs)
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}
	if *spaceFlag == "" {
		return errors.New("--space <uri> is required")
	}
	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	sh, err := crdtshadow.Default.Get(mustResolveSpace(root, *spaceFlag), *spaceFlag)
	if err != nil {
		return err
	}
	bundle, err := syncbundle.Export(sh.Doc(), *spaceFlag)
	if err != nil {
		return err
	}
	data, err := bundle.Marshal()
	if err != nil {
		return err
	}
	target := *out
	if target == "" {
		target = bundleDefaultName(*spaceFlag)
	}
	if target == "-" {
		if _, err := os.Stdout.Write(data); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "exported bundle to stdout")
	} else {
		if err := atomicfs.WriteFile(target, data, 0o644); err != nil {
			return err
		}
	}
	payload := map[string]any{
		"space":       *spaceFlag,
		"out":         target,
		"from_actor":  bundle.FromActor,
		"from_heads":  bundle.FromHeads,
		"bundle_size": len(data),
	}
	return emit("sync export", payload, *format, func(w io.Writer, _ any) error {
		fmt.Fprintf(w, "Exported %s\n", *spaceFlag)
		fmt.Fprintf(w, "  out:   %s\n", target)
		fmt.Fprintf(w, "  bytes: %d\n", len(data))
		fmt.Fprintf(w, "  heads: %s\n", strings.Join(bundle.FromHeads, ", "))
		return nil
	})
}

func cmdSyncImport(args []string) error {
	fs := flag.NewFlagSet("sync import", flag.ContinueOnError)
	spaceFlag := fs.String("space", "", "space URI (default: take from bundle)")
	in := fs.String("in", "-", "input file path; `-` for stdin")
	format := formatFlag(fs)
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}
	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	var raw []byte
	if *in == "-" {
		raw, err = io.ReadAll(os.Stdin)
	} else {
		raw, err = os.ReadFile(*in)
	}
	if err != nil {
		return fmt.Errorf("sync import: read: %w", err)
	}
	bundle, err := syncbundle.Unmarshal(raw)
	if err != nil {
		return err
	}
	targetSpace := *spaceFlag
	if targetSpace == "" {
		targetSpace = bundle.Space
	}
	if targetSpace == "" {
		return errors.New("sync import: bundle missing space and no --space override")
	}
	if *spaceFlag != "" && bundle.Space != "" && *spaceFlag != bundle.Space {
		fmt.Fprintf(os.Stderr, "warn: --space %q overrides bundle space %q\n", *spaceFlag, bundle.Space)
	}
	sh, err := crdtshadow.Default.Get(mustResolveSpace(root, targetSpace), targetSpace)
	if err != nil {
		return err
	}
	delta, err := syncbundle.Import(sh.Doc(), bundle)
	if err != nil {
		return err
	}
	if _, err := sh.Store().AppendChangesFromDoc(sh.Doc()); err != nil {
		return fmt.Errorf("sync import: persist: %w", err)
	}
	var materialized []string
	if delta > 0 {
		materialized, _ = sh.MaterializeAll()
	}
	payload := map[string]any{
		"space":              targetSpace,
		"in":                 *in,
		"changes_absorbed":   delta,
		"materialized_files": materialized,
		"from_actor":         bundle.FromActor,
		"from_heads":         bundle.FromHeads,
	}
	return emit("sync import", payload, *format, func(w io.Writer, _ any) error {
		fmt.Fprintf(w, "Imported %s\n", targetSpace)
		fmt.Fprintf(w, "  from:     %s (actor %s)\n", *in, bundle.FromActor)
		fmt.Fprintf(w, "  absorbed: %d new change(s)\n", delta)
		if len(materialized) > 0 {
			fmt.Fprintf(w, "  files:    %d canonical file(s) updated on disk\n", len(materialized))
			for _, p := range materialized {
				fmt.Fprintf(w, "    - %s\n", p)
			}
		}
		return nil
	})
}

func bundleDefaultName(spaceURI string) string {
	rest := strings.TrimPrefix(spaceURI, "hypha://")
	rest = strings.ReplaceAll(rest, "/", "-")
	if rest == "" {
		rest = "space"
	}
	return rest + ".bundle"
}

// --- peer ------------------------------------------------------------------

func cmdPeer(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: hypha peer add|list|remove [...]")
	}
	switch args[0] {
	case "add":
		return cmdPeerAdd(args[1:])
	case "list":
		return cmdPeerList(args[1:])
	case "remove", "rm":
		return cmdPeerRemove(args[1:])
	default:
		return fmt.Errorf("unknown peer subcommand %q (try `add`, `list`, `remove`)", args[0])
	}
}

func cmdPeerAdd(args []string) error {
	fs := flag.NewFlagSet("peer add", flag.ContinueOnError)
	name := fs.String("name", "", "peer name (default: derived from URI)")
	format := formatFlag(fs)
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: hypha peer add <uri> [--name <name>]")
	}
	uri := fs.Arg(0)
	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	added, err := peers.Add(root, *name, uri)
	if err != nil {
		return err
	}
	return emit("peer add", added, *format, func(w io.Writer, _ any) error {
		fmt.Fprintf(w, "Added peer %s → %s\n", added.Name, added.URI)
		return nil
	})
}

func cmdPeerList(args []string) error {
	fs := flag.NewFlagSet("peer list", flag.ContinueOnError)
	format := formatFlag(fs)
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}
	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	list, err := peers.List(root)
	if err != nil {
		return err
	}
	return emit("peer list", list, *format, func(w io.Writer, data any) error {
		ps, ok := data.([]peers.Peer)
		if !ok {
			return fmt.Errorf("peer list: text renderer got %T", data)
		}
		if len(ps) == 0 {
			fmt.Fprintln(w, "(no peers)")
			return nil
		}
		for _, p := range ps {
			fmt.Fprintf(w, "  %-20s  %s   added %s\n", p.Name, p.URI, p.AddedAt.Format(time.RFC3339))
		}
		return nil
	})
}

func cmdPeerRemove(args []string) error {
	fs := flag.NewFlagSet("peer remove", flag.ContinueOnError)
	format := formatFlag(fs)
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: hypha peer remove <name-or-uri>")
	}
	needle := fs.Arg(0)
	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	removed, err := peers.Remove(root, needle)
	if err != nil {
		return err
	}
	return emit("peer remove", removed, *format, func(w io.Writer, _ any) error {
		fmt.Fprintf(w, "Removed peer %s → %s\n", removed.Name, removed.URI)
		return nil
	})
}

// --- mcp -------------------------------------------------------------------

func cmdMCP(args []string) error {
	if len(args) == 0 || args[0] != "serve" {
		return errors.New("usage: hypha mcp serve")
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
	srv := mcp.NewServer(conn, root, mcp.ServerInfo{
		Name:    "hyphae",
		Version: hyphaeVersion,
	})
	return srv.Serve()
}

// --- pulse -----------------------------------------------------------------

func cmdPulse(args []string) error {
	fs := flag.NewFlagSet("pulse", flag.ContinueOnError)
	spaceURI := fs.String("space", "", "filter by space URI (default: all spaces)")
	windowStr := fs.String("window", "30d", "Go duration window (e.g. 7d, 30d, q2 → 90d)")
	ttlStr := fs.String("ttl", "5m", "cache TTL; pass 0 to force recompute")
	format := formatFlag(fs)
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}

	window, err := parseFlexDuration(*windowStr)
	if err != nil {
		return fmt.Errorf("--window %q: %w", *windowStr, err)
	}
	ttl, err := time.ParseDuration(*ttlStr)
	if err != nil {
		return fmt.Errorf("--ttl %q: %w", *ttlStr, err)
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

	var p pulse.Pulse
	if ttl <= 0 {
		p, err = pulse.Compute(conn, *spaceURI, window)
	} else {
		p, err = pulse.ComputeAndCache(conn, *spaceURI, window, ttl)
	}
	if err != nil {
		return err
	}

	return emit("pulse", p, *format, func(w io.Writer, data any) error {
		pp, ok := data.(pulse.Pulse)
		if !ok {
			return fmt.Errorf("pulse: text renderer got %T", data)
		}
		printPulseText(w, pp)
		return nil
	})
}

// parseFlexDuration accepts Go durations plus a few human-friendly shorthands:
// "Nd" → N*24h, "q1".."q4" → 90d. Falls back to time.ParseDuration.
func parseFlexDuration(s string) (time.Duration, error) {
	if len(s) > 1 && s[len(s)-1] == 'd' {
		n, err := time.ParseDuration(s[:len(s)-1] + "h")
		if err == nil {
			return n * 24, nil
		}
	}
	if len(s) == 2 && s[0] == 'q' && s[1] >= '1' && s[1] <= '4' {
		return 90 * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

// printPulseText renders a Pulse in a compact human format.
func printPulseText(w io.Writer, p pulse.Pulse) {
	fmt.Fprintf(w, "Pulse: %s (window %s, %d activity tokens used)\n",
		nonEmpty(p.Space, "<all spaces>"), p.Window, p.TokensUsed)
	fmt.Fprintf(w, "  Activity: %d spores submitted, %d grafts applied, %d new objects, %d new edges\n",
		p.Activity.SporesSubmitted, p.Activity.GraftsApplied, p.Activity.NewObjects, p.Activity.NewEdges)
	if len(p.TopInitiatives) > 0 {
		fmt.Fprintln(w, "  Top initiatives:")
		for _, t := range p.TopInitiatives {
			fmt.Fprintf(w, "    %s — %s  (%d inbound)\n", t.ID, t.Title, t.InboundEdges)
		}
	}
	if len(p.HotZones) > 0 {
		fmt.Fprintln(w, "  Hot zones:")
		for _, h := range p.HotZones {
			fmt.Fprintf(w, "    %s [%s]  graft_in=%d  new_out=%d\n", h.ObjectID, h.Type, h.GraftEdgesIn, h.NewEdgesOut)
		}
	}
	if len(p.RecentPressure) > 0 {
		fmt.Fprintln(w, "  Recent pressure:")
		for _, pr := range p.RecentPressure {
			fmt.Fprintf(w, "    %s (%s) ×%d\n", pr.Kind, pr.Topic, pr.Count)
		}
	}
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func cmdAssess(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: hypha assess change|task|pr [...]")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "change":
		return cmdAssessChange(rest)
	case "task":
		return cmdAssessTask(rest)
	case "pr":
		return cmdAssessPR(rest)
	default:
		return fmt.Errorf("unknown assess subcommand %q (try `change`, `task`, `pr`)", sub)
	}
}

// cmdAssessPR derives a file list and diff summary from a git ref range,
// then runs the assess.Change scorer. Thin convenience over `assess change`.
// With --space and an installed canopy index, also folds cached impact
// analyses into hot_zone (same as `assess change`).
func cmdAssessPR(args []string) error {
	fs := flag.NewFlagSet("assess pr", flag.ContinueOnError)
	task := fs.String("task", "", "natural-language description of the PR (required)")
	base := fs.String("base", "origin/main", "git ref to diff against")
	source := fs.String("source", "", "source repo path (defaults via space convention; otherwise cwd)")
	spaceURI := fs.String("space", "", "space URI (default: all spaces)")
	windowStr := fs.String("window", "30d", "Go duration window for recent-pressure aggregation")
	budgetTokens := fs.Int("budget-tokens", 1500, "soft response token budget")
	format := formatFlag(fs)
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}
	if strings.TrimSpace(*task) == "" {
		return errors.New("assess pr requires --task <text>")
	}

	window, err := parseFlexDuration(*windowStr)
	if err != nil {
		return fmt.Errorf("--window %q: %w", *windowStr, err)
	}

	// Resolve source path.
	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	srcPath := *source
	if srcPath == "" && *spaceURI != "" {
		if p, err := resolveSourceForSpace(*spaceURI, ""); err == nil {
			srcPath = p
		}
	}
	if srcPath == "" {
		cwd, _ := os.Getwd()
		srcPath = cwd
	}

	// Derive --files via `git diff --name-only <base>...HEAD`.
	files, err := gitChangedFiles(srcPath, *base)
	if err != nil {
		return fmt.Errorf("assess pr: derive files from %s: %w", *base, err)
	}
	if len(files) == 0 {
		return fmt.Errorf("assess pr: no changed files vs %s (nothing to score)", *base)
	}

	// Derive a diff-summary from `git diff --stat <base>...HEAD` (one line).
	diffSummary, _ := gitDiffStat(srcPath, *base)

	conn, err := openIndex(root)
	if err != nil {
		return err
	}
	defer conn.Close()

	req := assess.ChangeRequest{
		Task:         *task,
		ChangedFiles: files,
		DiffSummary:  diffSummary,
		Space:        *spaceURI,
		Window:       window,
		Budget:       types.Budget{MaxResponseTokens: *budgetTokens, Shape: types.ShapeCitedSpans},
		SourcePath:   srcPath,
	}
	if *spaceURI != "" {
		if sr, _, err := resolveSpaceForTrace(root, *spaceURI); err == nil {
			req.SpaceRoot = sr
		}
	}

	res, err := assess.Change(conn, req)
	if err != nil {
		return err
	}

	payload := map[string]any{
		"task":          *task,
		"base_ref":      *base,
		"changed_files": files,
		"diff_summary":  diffSummary,
		"result":        res,
	}
	return emit("assess pr", payload, *format, func(w io.Writer, _ any) error {
		fmt.Fprintf(w, "Base ref:      %s\n", *base)
		fmt.Fprintf(w, "Changed files: %d\n", len(files))
		if diffSummary != "" {
			fmt.Fprintf(w, "Diff summary:  %s\n", diffSummary)
		}
		fmt.Fprintln(w)
		printAssessText(w, res)
		return nil
	})
}

// gitChangedFiles returns the file paths changed between base...HEAD.
func gitChangedFiles(workdir, base string) ([]string, error) {
	cmd := exec.Command("git", "diff", "--name-only", base+"...HEAD")
	cmd.Dir = workdir
	out, err := cmd.Output()
	if err != nil {
		// Fall back: maybe base is the current diff (e.g. "HEAD").
		cmd2 := exec.Command("git", "diff", "--name-only", base)
		cmd2.Dir = workdir
		out, err = cmd2.Output()
		if err != nil {
			return nil, err
		}
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

// gitDiffStat returns a single-line diff summary ("4 files changed, 23 insertions(+), 8 deletions(-)").
func gitDiffStat(workdir, base string) (string, error) {
	cmd := exec.Command("git", "diff", "--shortstat", base+"...HEAD")
	cmd.Dir = workdir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// cmdAssessTask runs the alignment scorer with task-only input (no files,
// no diff_summary). Same engine, same JSON shape — useful when you're
// scoping a task before any diff exists.
func cmdAssessTask(args []string) error {
	fs := flag.NewFlagSet("assess task", flag.ContinueOnError)
	task := fs.String("task", "", "natural-language description of the proposed task")
	spaceURI := fs.String("space", "", "filter scoring to one space URI (default: all spaces)")
	windowStr := fs.String("window", "30d", "Go duration window for recent-pressure aggregation")
	budgetTokens := fs.Int("budget-tokens", 1200, "soft response token budget (advisory)")
	format := formatFlag(fs)
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}
	if strings.TrimSpace(*task) == "" {
		return errors.New("assess task requires --task <text>")
	}

	window, err := parseFlexDuration(*windowStr)
	if err != nil {
		return fmt.Errorf("--window %q: %w", *windowStr, err)
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

	res, err := assess.Change(conn, assess.ChangeRequest{
		Task:   *task,
		Space:  *spaceURI,
		Window: window,
		Budget: types.Budget{MaxResponseTokens: *budgetTokens, Shape: types.ShapeCitedSpans},
	})
	if err != nil {
		return err
	}

	return emit("assess task", res, *format, func(w io.Writer, data any) error {
		r, ok := data.(assess.Result)
		if !ok {
			return fmt.Errorf("assess task: text renderer got %T", data)
		}
		printAssessText(w, r)
		return nil
	})
}

func cmdAssessChange(args []string) error {
	fs := flag.NewFlagSet("assess change", flag.ContinueOnError)
	task := fs.String("task", "", "natural-language description of the proposed change")
	filesCSV := fs.String("files", "", "comma-separated list of changed file paths")
	diffSummary := fs.String("diff-summary", "", "short summary of the diff")
	spaceURI := fs.String("space", "", "filter scoring to one space URI (default: all spaces)")
	source := fs.String("source", "", "source repo path (enables canopy-cache enrichment; defaults via space convention)")
	windowStr := fs.String("window", "30d", "Go duration window for recent-pressure aggregation")
	budgetTokens := fs.Int("budget-tokens", 1200, "soft response token budget (advisory)")
	format := formatFlag(fs)
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}

	window, err := parseFlexDuration(*windowStr)
	if err != nil {
		return fmt.Errorf("--window %q: %w", *windowStr, err)
	}

	var files []string
	for _, p := range strings.Split(*filesCSV, ",") {
		if p = strings.TrimSpace(p); p != "" {
			files = append(files, p)
		}
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

	req := assess.ChangeRequest{
		Task:         *task,
		ChangedFiles: files,
		DiffSummary:  *diffSummary,
		Space:        *spaceURI,
		Window:       window,
		Budget:       types.Budget{MaxResponseTokens: *budgetTokens, Shape: types.ShapeCitedSpans},
	}

	// Best-effort opportunistic canopy enrichment: if --space resolves to an
	// installed space and (--source or convention) gives us a source repo,
	// assess.Change will fold cached impact analyses into hot_zone.
	if *spaceURI != "" {
		if sr, _, err := resolveSpaceForTrace(root, *spaceURI); err == nil {
			req.SpaceRoot = sr
		}
		if sp, err := resolveSourceForSpace(*spaceURI, *source); err == nil {
			req.SourcePath = sp
		}
	}

	res, err := assess.Change(conn, req)
	if err != nil {
		return err
	}

	return emit("assess change", res, *format, func(w io.Writer, data any) error {
		r, ok := data.(assess.Result)
		if !ok {
			return fmt.Errorf("assess change: text renderer got %T", data)
		}
		printAssessText(w, r)
		return nil
	})
}

func printAssessText(w io.Writer, r assess.Result) {
	fmt.Fprintf(w, "Alignment:      %s\n", r.Alignment)
	fmt.Fprintf(w, "Score:          %.2f\n", r.Score)
	fmt.Fprintf(w, "Recommendation: %s\n", r.Recommendation)
	if len(r.MatchedInitiatives) > 0 {
		fmt.Fprintln(w, "\nMatched initiatives:")
		for _, m := range r.MatchedInitiatives {
			fmt.Fprintf(w, "  - %s  (score %.2f)\n      %s — %s\n", m.ID, m.Score, nonEmpty(m.Title, "(no title)"), m.Reason)
		}
	}
	if len(r.RecentPressure) > 0 {
		fmt.Fprintln(w, "\nRecent pressure:")
		for _, p := range r.RecentPressure {
			fmt.Fprintf(w, "  - %s\n", p)
		}
	}
	if r.HotZone != nil {
		hz := r.HotZone
		fmt.Fprintln(w, "\nHot zone:")
		if hz.Path != "" {
			fmt.Fprintf(w, "  path:         %s\n", hz.Path)
		}
		fmt.Fprintf(w, "  grafts/14d:   %d\n", hz.Commits14d)
		fmt.Fprintf(w, "  incidents/14d:%d\n", hz.Incidents14d)
		if hz.AffectedSymbols > 0 {
			fmt.Fprintf(w, "  affected:     %d symbols, %d files\n", hz.AffectedSymbols, len(hz.AffectedFiles))
		}
		if hz.AnalysisID != "" {
			stale := ""
			if hz.AnalysisStale {
				stale = " (STALE)"
			}
			fmt.Fprintf(w, "  analysis:     %s%s\n", hz.AnalysisID, stale)
		}
	}
	fmt.Fprintf(w, "\nTokens used: %d\n", r.TokensUsed)
}

// --- index rebuild ----------------------------------------------------------

func cmdIndex(args []string) error {
	if len(args) == 0 || args[0] != "rebuild" {
		return errors.New("usage: hypha index rebuild [--root <path>]")
	}
	fs := flag.NewFlagSet("index rebuild", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "install root override (default: HYPHAE_HOME or ~/.hyphae)")
	format := formatFlag(fs)
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
			return fmt.Errorf("index FTS %s: %w", sp.URI, err)
		}
		if err := persistObjectsAnchorsEdges(conn, sp.Path, objects, anchors, edges); err != nil {
			return fmt.Errorf("index tables %s: %w", sp.URI, err)
		}
		totalObj += len(objects)
		totalAnc += len(anchors)
		totalEdg += len(edges)
	}

	payload := map[string]any{
		"db":              dbPath,
		"spaces_indexed":  len(spaces),
		"objects_indexed": totalObj,
		"anchors_indexed": totalAnc,
		"edges_indexed":   totalEdg,
	}
	return emit("index rebuild", payload, *format, func(w io.Writer, _ any) error {
		fmt.Fprintf(w, "Indexed %d objects, %d anchors, %d edges across %d space(s)\n",
			totalObj, totalAnc, totalEdg, len(spaces))
		fmt.Fprintf(w, "  db: %s\n", dbPath)
		return nil
	})
}

// persistObjectsAnchorsEdges writes parser output to objects/anchors/edges
// tables. Uses one transaction per space for atomicity + speed. UPSERTs are
// idempotent so re-indexing the same space is safe.
func persistObjectsAnchorsEdges(conn *sql.DB, spacePath string, objects []types.Object, anchors []types.Anchor, edges []types.Edge) error {
	tx, err := conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	now := time.Now().UTC().Format(time.RFC3339)

	for _, o := range objects {
		tagsJSON := "[]"
		if len(o.Tags) > 0 {
			if b, jerr := json.Marshal(o.Tags); jerr == nil {
				tagsJSON = string(b)
			}
		}
		// file_id: use the path relative to space root as a stable synthetic id.
		fileID, _ := filepath.Rel(spacePath, o.FilePath)
		if fileID == "" {
			fileID = o.FilePath
		}
		updated := now
		if !o.UpdatedAt.IsZero() {
			updated = o.UpdatedAt.UTC().Format(time.RFC3339)
		}
		if _, err := tx.Exec(`
			INSERT INTO objects (id, type, space_id, file_id, status, title, tags_json, summary, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				type = excluded.type,
				space_id = excluded.space_id,
				file_id = excluded.file_id,
				status = excluded.status,
				title = excluded.title,
				tags_json = excluded.tags_json,
				summary = excluded.summary,
				updated_at = excluded.updated_at`,
			o.ID, string(o.Type), o.SpaceID, fileID, o.Status, o.Title, tagsJSON, o.Summary, updated); err != nil {
			return fmt.Errorf("upsert object %q: %w", o.ID, err)
		}
	}

	for _, a := range anchors {
		if _, err := tx.Exec(`
			INSERT INTO anchors (id, object_id, heading_path, start_byte, end_byte, start_line, end_line, node_kind)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				object_id = excluded.object_id,
				heading_path = excluded.heading_path,
				start_byte = excluded.start_byte,
				end_byte = excluded.end_byte,
				start_line = excluded.start_line,
				end_line = excluded.end_line,
				node_kind = excluded.node_kind`,
			a.ID, a.ObjectID, a.HeadingPath, a.StartByte, a.EndByte, a.StartLine, a.EndLine, a.NodeKind); err != nil {
			return fmt.Errorf("upsert anchor %q: %w", a.ID, err)
		}
	}

	for _, e := range edges {
		conf := e.Confidence
		if conf == 0 {
			conf = 1.0
		}
		// INSERT OR IGNORE: parser-derived edges are idempotent by (kind, src, dst);
		// any prior graft-derived edge keeps its richer metadata.
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO edges (id, kind, src_id, dst_id, confidence, derivation, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			e.ID, string(e.Kind), e.SrcID, e.DstID, conf, e.Derivation, now); err != nil {
			return fmt.Errorf("upsert edge %q: %w", e.ID, err)
		}
	}

	return tx.Commit()
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
	format := formatFlag(fs)
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

	return emit("recall", resp, *format, func(w io.Writer, data any) error {
		r, ok := data.(recall.Response)
		if !ok {
			return fmt.Errorf("recall: text renderer got %T", data)
		}
		fmt.Fprintln(w, r.Summary)
		for _, h := range r.Hits {
			fmt.Fprintf(w, "\n  %s  %s\n", h.URI, h.Title)
			for _, sn := range h.Snippets {
				fmt.Fprintf(w, "      %s\n", sn.Text)
				fmt.Fprintf(w, "        ↳ %s  (L%d-%d)\n", sn.Citation.Anchor, sn.Citation.Line, sn.Citation.EndLine)
			}
		}
		fmt.Fprintf(os.Stderr, "\n(%d hits, %d tokens used)\n", len(r.Hits), r.TokensUsed)
		return nil
	})
}

// --- spore submit -----------------------------------------------------------

func cmdSpore(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: hypha spore submit|list|accept|reject [...]")
	}
	switch args[0] {
	case "submit":
		return cmdSporeSubmit(args[1:])
	case "list":
		return cmdSporeList(args[1:])
	case "accept":
		return cmdSporeReview(args[1:], "accepted")
	case "reject":
		return cmdSporeReview(args[1:], "rejected")
	default:
		return fmt.Errorf("unknown spore subcommand %q (try `submit`, `list`, `accept`, `reject`)", args[0])
	}
}

// cmdSporeReview flips an unreviewed spore to `accepted` or `rejected`,
// writes a receipt, and updates the file in place. No canonical writes —
// that's still `hypha graft`'s job. Useful for queuing spores for later
// graft, or formally rejecting a contribution without applying it.
func cmdSporeReview(args []string, newStatus string) error {
	fs := flag.NewFlagSet("spore review", flag.ContinueOnError)
	spaceFlag := fs.String("space", "", "space URI containing the spore")
	asURI := fs.String("as", "", "reviewer identity URI (recorded in the receipt)")
	reason := fs.String("reason", "", "optional human-readable reason (recorded in metadata)")
	format := formatFlag(fs)
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: hypha spore %s <spore-id> --as <identity> [--reason \"...\"] [--space <uri>]", newStatus)
	}
	sporeID := fs.Arg(0)
	if strings.TrimSpace(*asURI) == "" {
		return errors.New("--as <identity-uri> is required")
	}

	root, err := resolveRoot("")
	if err != nil {
		return err
	}

	// Locate the spore file across spaces (or in the named space).
	var spacePath, spaceURI string
	if *spaceFlag != "" {
		sr, su, err := resolveSpaceForTrace(root, *spaceFlag)
		if err != nil {
			return err
		}
		spacePath = sr
		spaceURI = su
	} else {
		sr, err := findSporeSpaceRoot(root, sporeID)
		if err != nil {
			return err
		}
		spacePath = sr
		// Derive space URI from path basename ("m31labs-hyphae" → "hypha://m31labs/hyphae").
		spaceURI = spaceURIFromDir(spacePath)
	}
	sporePath, err := findSporeFilePath(spacePath, sporeID)
	if err != nil {
		return err
	}

	// Read, flip status, write back. Only allow flips from unreviewed.
	data, err := os.ReadFile(sporePath)
	if err != nil {
		return err
	}
	cur, ok := readFrontmatterField(data, "status")
	if !ok {
		return fmt.Errorf("spore review: %s has no status field", sporeID)
	}
	if cur != "unreviewed" {
		return fmt.Errorf("spore review: status is %q (only unreviewed spores can be reviewed); for already-graphed spores use `hypha graft`", cur)
	}
	updated := writeFrontmatterField(data, "status", newStatus)
	if err := atomicfs.WriteFile(sporePath, updated, 0o644); err != nil {
		return fmt.Errorf("spore review: write %s: %w", sporePath, err)
	}

	// Persist a receipt.
	action := "spore:" + newStatus // "spore:accepted" or "spore:rejected"
	metadata := ""
	if strings.TrimSpace(*reason) != "" {
		if b, err := json.Marshal(map[string]string{"reason": *reason}); err == nil {
			metadata = string(b)
		}
	}
	hash := sha256.Sum256(updated)
	receipt := types.Receipt{
		ID:              fmt.Sprintf("hypha-receipt:%s:%s:%s", action, time.Now().UTC().Format("2006-01-02"), shortHash(hash[:])),
		SpaceID:         spaceURI,
		SubjectID:       sporeID,
		SubjectKind:     "spore",
		Action:          action,
		Status:          "ok",
		ContentHash:     fmt.Sprintf("%x", hash[:]),
		IdentityID:      *asURI,
		CreatedAt:       time.Now().UTC(),
		PermissionsUsed: []string{"spore:review"},
		NextState:       newStatus,
		MetadataJSON:    metadata,
	}
	if conn, dbErr := openIndex(root); dbErr == nil {
		defer conn.Close()
		if wErr := receipts.Write(conn, receipt); wErr != nil && !errors.Is(wErr, receipts.ErrAlreadyExists) {
			fmt.Fprintf(os.Stderr, "warn: persist receipt: %v\n", wErr)
		}
	}

	// Mirror status flip + receipt into the per-space CRDT shadow.
	crdtshadow.MirrorSporeStatus(root, spaceURI, sporeID, newStatus)
	crdtshadow.MirrorReceipt(root, receipt)

	out := map[string]any{
		"spore_id":     sporeID,
		"status_was":   cur,
		"status_now":   newStatus,
		"reviewer":     *asURI,
		"path":         sporePath,
		"receipt_id":   receipt.ID,
		"content_hash": receipt.ContentHash,
	}
	return emit("spore "+newStatus, out, *format, func(w io.Writer, _ any) error {
		fmt.Fprintf(w, "Spore %s: %s → %s\n", sporeID, cur, newStatus)
		fmt.Fprintf(w, "  Reviewer: %s\n", *asURI)
		fmt.Fprintf(w, "  Receipt:  %s\n", receipt.ID)
		fmt.Fprintf(w, "  Path:     %s\n", sporePath)
		return nil
	})
}

// readFrontmatterField extracts the value of a top-level `key: value` field
// from a YAML frontmatter block (the bytes between the first two `---`).
// Returns ("", false) on miss.
func readFrontmatterField(data []byte, key string) (string, bool) {
	s := string(data)
	if !strings.HasPrefix(s, "---\n") {
		return "", false
	}
	rest := s[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		end = strings.Index(rest, "\n---\r\n")
	}
	if end < 0 {
		return "", false
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		if !strings.HasPrefix(line, key+":") {
			continue
		}
		v := strings.TrimSpace(strings.TrimPrefix(line, key+":"))
		v = strings.Trim(v, `"`)
		return v, true
	}
	return "", false
}

// writeFrontmatterField replaces (or appends, on miss) the value of a
// top-level field in the YAML frontmatter block. Pure text edit; preserves
// surrounding formatting.
func writeFrontmatterField(data []byte, key, value string) []byte {
	s := string(data)
	if !strings.HasPrefix(s, "---\n") {
		return data
	}
	rest := s[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return data
	}
	fmBlock := rest[:end]
	var nb strings.Builder
	nb.WriteString("---\n")
	replaced := false
	for _, line := range strings.Split(fmBlock, "\n") {
		if !replaced && strings.HasPrefix(line, key+":") {
			fmt.Fprintf(&nb, "%s: %s\n", key, value)
			replaced = true
			continue
		}
		nb.WriteString(line)
		nb.WriteString("\n")
	}
	if !replaced {
		fmt.Fprintf(&nb, "%s: %s\n", key, value)
	}
	nb.WriteString("---\n")
	nb.WriteString(rest[end+len("\n---\n"):])
	return []byte(nb.String())
}

// spaceURIFromDir reconstructs a hypha:// URI from a space dir path.
// "/home/.../spaces/m31labs-hyphae" → "hypha://m31labs/hyphae".
func spaceURIFromDir(spaceDir string) string {
	base := filepath.Base(spaceDir)
	// First "-" separates authority from name in our scaffold convention.
	if idx := strings.Index(base, "-"); idx > 0 {
		return "hypha://" + base[:idx] + "/" + base[idx+1:]
	}
	return "hypha://" + base
}

// shortHash returns the first 7 hex chars of a SHA digest.
func shortHash(sum []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, 7)
	for _, b := range sum[:4] { // 4 bytes = 8 hex chars; trim to 7
		out = append(out, hexdigits[b>>4], hexdigits[b&0x0f])
	}
	return string(out[:7])
}

func cmdSporeSubmit(rest []string) error {
	fs := flag.NewFlagSet("spore submit", flag.ContinueOnError)
	sign := fs.Bool("sign", false, "Ed25519-sign the spore before submission")
	signer := fs.String("as", "", "signer identity URI (required with --sign)")
	format := formatFlag(fs)
	if err := fs.Parse(reorderFlagsFirst(rest)); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: hypha spore submit <file> [--sign --as <identity-uri>]")
	}
	path := fs.Arg(0)

	source, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	// Parse for validation (also validates we have a usable spore before we sign).
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

	var filePath string
	var receipt types.Receipt

	if *sign {
		if *signer == "" {
			return errors.New("--sign requires --as <identity-uri>")
		}
		identDir := filepath.Join(root, ".catalog", "identities")
		signerName := identityNameFromURI(*signer)
		if signerName == "" {
			return fmt.Errorf("--as %q must be a full identity:// URI", *signer)
		}
		priv, lpErr := identity.LoadPrivate(identDir, signerName)
		if lpErr != nil {
			return fmt.Errorf("load signer key: %w", lpErr)
		}
		signed, sErr := spore.Sign(source, priv, *signer)
		if sErr != nil {
			return fmt.Errorf("sign: %w", sErr)
		}
		filePath, receipt, err = spore.SubmitBytes(signed, spaceRoot)
		if err != nil {
			return fmt.Errorf("submit signed: %w", err)
		}
	} else {
		filePath, receipt, err = spore.Submit(sp, spaceRoot)
		if err != nil {
			return fmt.Errorf("submit: %w", err)
		}
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

	// Mirror the spore + receipt into the per-space CRDT shadow.
	sp.FilePath = filePath
	sp.ContentHash = receipt.ContentHash
	crdtshadow.MirrorSpore(root, sp)
	crdtshadow.MirrorReceipt(root, receipt)

	payload := map[string]any{
		"receipt":   receipt,
		"file_path": filePath,
		"signed":    *sign,
	}
	return emit("spore submit", payload, *format, func(w io.Writer, _ any) error {
		fmt.Fprintf(w, "Submitted: %s\n", filePath)
		fmt.Fprintf(w, "  Signed:   %t\n", *sign)
		fmt.Fprintf(w, "  Receipt:  %s\n", receipt.ID)
		return nil
	})
}

// identityNameFromURI extracts the bare name from "identity://<authority>/<name>".
// Returns empty string if uri doesn't fit that shape.
func identityNameFromURI(uri string) string {
	rest, ok := strings.CutPrefix(uri, "identity://")
	if !ok {
		return ""
	}
	slash := strings.Index(rest, "/")
	if slash < 0 || slash == len(rest)-1 {
		return ""
	}
	return rest[slash+1:]
}

// identityResolver returns a spore.IdentityResolver backed by files under
// <root>/.catalog/identities/.
func identityResolver(root string) spore.IdentityResolver {
	dir := filepath.Join(root, ".catalog", "identities")
	return func(uri string) (identity.Identity, error) {
		name := identityNameFromURI(uri)
		if name == "" {
			return identity.Identity{}, fmt.Errorf("not a recognized identity URI: %q", uri)
		}
		return identity.Load(dir, name)
	}
}

// --- spore list -------------------------------------------------------------

func cmdSporeList(args []string) error {
	fs := flag.NewFlagSet("spore list", flag.ContinueOnError)
	spaceFilter := fs.String("space", "", "filter by space URI (default: all installed spaces)")
	statusFilter := fs.String("status", "", "filter by status (unreviewed, accepted, partial, rejected, ...)")
	sinceStr := fs.String("since", "", "only spores submitted within this duration (e.g. 24h, 7d)")
	limit := fs.Int("limit", 50, "max results")
	format := formatFlag(fs)
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}

	var sinceCutoff time.Time
	if *sinceStr != "" {
		d, derr := parseFlexDuration(*sinceStr)
		if derr != nil {
			return fmt.Errorf("--since %q: %w", *sinceStr, derr)
		}
		sinceCutoff = time.Now().UTC().Add(-d)
	}

	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	spaces, err := listSpaces(root)
	if err != nil {
		return err
	}

	type sporeRow struct {
		ID          string    `json:"id"`
		Space       string    `json:"space"`
		Status      string    `json:"status"`
		Path        string    `json:"path"`
		SubmittedAt time.Time `json:"submitted_at"`
	}
	var out []sporeRow

	for _, sp := range spaces {
		if *spaceFilter != "" && !spaceMatches(sp, *spaceFilter) {
			continue
		}
		inboxDir := filepath.Join(sp.Path, "inbox", "agents")
		entries, _ := os.ReadDir(inboxDir)
		for _, ent := range entries {
			if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".md") {
				continue
			}
			absPath := filepath.Join(inboxDir, ent.Name())
			content, rerr := os.ReadFile(absPath)
			if rerr != nil {
				continue
			}
			s, _ := spore.Parse(content)
			if s.ID == "" {
				continue
			}
			if *statusFilter != "" && s.Status != *statusFilter {
				continue
			}
			if !sinceCutoff.IsZero() && s.SubmittedAt.Before(sinceCutoff) {
				continue
			}
			out = append(out, sporeRow{
				ID:          s.ID,
				Space:       s.SpaceID,
				Status:      s.Status,
				Path:        absPath,
				SubmittedAt: s.SubmittedAt,
			})
		}
	}

	// Newest first.
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].SubmittedAt.After(out[j].SubmittedAt)
	})
	if len(out) > *limit {
		out = out[:*limit]
	}

	return emit("spore list", out, *format, func(w io.Writer, data any) error {
		rows, ok := data.([]sporeRow)
		if !ok {
			return fmt.Errorf("spore list: text renderer got %T", data)
		}
		if len(rows) == 0 {
			fmt.Fprintln(w, "(no spores)")
			return nil
		}
		for _, r := range rows {
			fmt.Fprintf(w, "%s  %-12s  %s\n      %s\n",
				r.SubmittedAt.Format("2006-01-02 15:04"),
				nonEmpty(r.Status, "?"),
				r.ID,
				r.Path,
			)
		}
		fmt.Fprintf(os.Stderr, "\n(%d spores)\n", len(rows))
		return nil
	})
}

// spaceMatches reports whether the filter (a hypha:// URI or bare authority/name)
// matches a space entry.
func spaceMatches(sp spaceEntry, filter string) bool {
	f := strings.TrimPrefix(filter, "hypha://")
	f = strings.TrimRight(f, "/")
	return sp.URI == f || sp.URI == filter
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
	format := formatFlag(fs)
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
	crdtshadow.MirrorReceipt(root, rcpt)

	payload := map[string]any{
		"token":      cap.ID,
		"capability": cap,
	}
	return emit("cap issue", payload, *format, func(w io.Writer, _ any) error {
		fmt.Fprintf(w, "Capability: %s\n", cap.ID)
		fmt.Fprintf(w, "  Subject:   %s\n", cap.Subject)
		fmt.Fprintf(w, "  Space:     %s\n", cap.SpaceID)
		fmt.Fprintf(w, "  Expires:   %s\n", cap.ExpiresAt.Format(time.RFC3339))
		return nil
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
	format := formatFlag(fs)
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

	payload := map[string]any{
		"identity":      id,
		"identity_file": mdPath,
		"private_key":   keyPath + " (mode 0600)",
	}
	return emit("identity init", payload, *format, func(w io.Writer, _ any) error {
		fmt.Fprintf(w, "Identity: %s\n", id.ID)
		fmt.Fprintf(w, "  File: %s\n", mdPath)
		fmt.Fprintf(w, "  Key:  %s (mode 0600)\n", keyPath)
		return nil
	})
}

func cmdIdentityList(args []string) error {
	fs := flag.NewFlagSet("identity list", flag.ContinueOnError)
	format := formatFlag(fs)
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}
	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	dir := filepath.Join(root, ".catalog", "identities")

	list, err := identity.List(dir)
	if err != nil {
		return err
	}
	return emit("identity list", list, *format, func(w io.Writer, data any) error {
		ids, ok := data.([]identity.Identity)
		if !ok {
			return fmt.Errorf("identity list: text renderer got %T", data)
		}
		if len(ids) == 0 {
			fmt.Fprintf(os.Stderr, "no identities found at %s\n", dir)
			return nil
		}
		for _, i := range ids {
			fmt.Fprintf(w, "  %s\n", i.ID)
		}
		return nil
	})
}

// --- graft -----------------------------------------------------------------

func cmdGraft(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: hypha graft <spore-id> --as <identity-uri> [--space <hypha-uri>] [--verify]")
	}
	fs := flag.NewFlagSet("graft", flag.ContinueOnError)
	grafter := fs.String("as", "", "grafter identity URI (recorded in the receipt)")
	spaceURI := fs.String("space", "", "space URI override (auto-detected from inbox if omitted)")
	verify := fs.Bool("verify", false, "verify Ed25519 signature on the spore before applying")
	noFmt := fs.Bool("no-fmt", false, "skip the mdpp.fmt pass on touched canonical files (formatting on by default)")
	dryRun := fs.Bool("dry-run", false, "plan the graft without persisting any file, spore-status, or edge changes")
	showDiff := fs.Bool("diff", false, "render a unified diff per touched file (implies --dry-run unless --apply also set)")
	apply := fs.Bool("apply", false, "with --diff: persist the graft after printing the diff (default is preview-only)")
	format := formatFlag(fs)
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: hypha graft <spore-id> --as <identity-uri> [--space <hypha-uri>] [--verify]")
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

	// Optional pre-graft signature verification.
	if *verify {
		sporePath, err := findSporeFilePath(spaceRoot, sporeID)
		if err != nil {
			return fmt.Errorf("verify: %w", err)
		}
		sporeBytes, err := os.ReadFile(sporePath)
		if err != nil {
			return fmt.Errorf("verify read: %w", err)
		}
		if err := spore.Verify(sporeBytes, identityResolver(root)); err != nil {
			return fmt.Errorf("verify failed (refusing graft): %w", err)
		}
		fmt.Fprintln(os.Stderr, "verified spore signature")
	}

	// --diff defaults to preview-only; opt back in with --apply to persist.
	effectiveDryRun := *dryRun || (*showDiff && !*apply)

	result, err := graft.ApplyWithOpts(conn, root, spaceRoot, sporeID, *grafter, graft.ApplyOpts{
		DryRun: effectiveDryRun,
	})
	if err != nil {
		return fmt.Errorf("graft: %w", err)
	}

	// mdpp.fmt pass on touched files: normalize canonical state so the
	// post-graft tree stays canonical. Best-effort — failures are logged
	// but don't unwind the graft (the apply already persisted). Skip in dry-run.
	if !*noFmt && !effectiveDryRun {
		for _, p := range result.TouchedFiles {
			if changed, ferr := formatMdppFile(p); ferr != nil {
				fmt.Fprintf(os.Stderr, "warn: mdpp.fmt %s: %v\n", p, ferr)
			} else if changed {
				fmt.Fprintf(os.Stderr, "fmt: %s\n", p)
			}
		}
	}

	// Persist the graft receipt to the audit log (skipped in dry-run).
	if !effectiveDryRun {
		if wErr := receipts.Write(conn, result.Receipt); wErr != nil && !errors.Is(wErr, receipts.ErrAlreadyExists) {
			fmt.Fprintf(os.Stderr, "warn: failed to persist graft receipt: %v\n", wErr)
		}

		// Mirror canonical writes + receipt + applied edges into the per-space CRDT shadow.
		crdtshadow.MirrorReceipt(root, result.Receipt)
		crdtshadow.MirrorCanonical(root, result.Receipt.SpaceID, result.TouchedFiles)
		for _, e := range result.AppliedEdges {
			crdtshadow.MirrorEdge(root, result.Receipt.SpaceID, e)
		}
		// Spore status flipped (accepted/partial) — mirror that too.
		crdtshadow.MirrorSporeStatus(root, result.Receipt.SpaceID, result.SporeID, result.NewSporeStatus)
	}

	return emit("graft", result, *format, func(w io.Writer, data any) error {
		r, ok := data.(graft.Result)
		if !ok {
			return fmt.Errorf("graft: text renderer got %T", data)
		}
		verb := "Grafted"
		if r.DryRun {
			verb = "DRY-RUN — would graft"
		}
		fmt.Fprintf(w, "%s %s → status: %s (applied %d, skipped %d)\n",
			verb, r.SporeID, r.NewSporeStatus, len(r.AppliedWrites), len(r.SkippedWrites))
		if !r.DryRun {
			fmt.Fprintf(w, "  Receipt: %s\n", r.Receipt.ID)
		}
		if len(r.TouchedFiles) > 0 {
			fmt.Fprintln(w, "  Touched:")
			for _, p := range r.TouchedFiles {
				fmt.Fprintf(w, "    - %s\n", p)
			}
		}
		if *showDiff {
			fmt.Fprintln(w)
			for _, d := range r.Deltas {
				fmt.Fprint(w, graft.RenderDelta(d))
				fmt.Fprintln(w)
			}
		}
		return nil
	})
}

// formatMdppFile runs mdpp.fmt on a single file and rewrites it if the
// output differs. Returns (changed, error). Safe to call on any .md file —
// mdpp.fmt is idempotent and preserves protected fences.
func formatMdppFile(path string) (bool, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	out, err := mdppfmt.Format(src)
	if err != nil {
		return false, err
	}
	if bytes.Equal(src, out) {
		return false, nil
	}
	if err := atomicfs.WriteFile(path, out, 0o644); err != nil {
		return false, err
	}
	return true, nil
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

// findSporeFilePath returns the on-disk path of the spore matching sporeID
// inside spaceRoot/inbox/agents/.
func findSporeFilePath(spaceRoot, sporeID string) (string, error) {
	inbox := filepath.Join(spaceRoot, "inbox", "agents")
	entries, err := os.ReadDir(inbox)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		p := filepath.Join(inbox, e.Name())
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if bytesContainsID(data, sporeID) {
			return p, nil
		}
	}
	return "", fmt.Errorf("spore %q not found under %s", sporeID, inbox)
}

// --- show ------------------------------------------------------------------

func cmdShow(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: hypha show <id-or-hypha-uri> [--path] [--json] [--frontmatter] [--body]")
	}
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	pathOnly := fs.Bool("path", false, "print only the resolved absolute file path")
	jsonOut := fs.Bool("json", false, "print object metadata as JSON (id, type, space, path, title, status, tags, updated_at)")
	frontOnly := fs.Bool("frontmatter", false, "print only the YAML frontmatter block")
	bodyOnly := fs.Bool("body", false, "print only the markdown body (everything after the frontmatter)")
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: hypha show <id-or-hypha-uri> [--path] [--json] [--frontmatter] [--body]")
	}
	id := normalizeShowID(fs.Arg(0))

	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	conn, err := openIndex(root)
	if err != nil {
		return err
	}
	defer conn.Close()

	var fileID, spaceID, typeStr, status, title, tagsJSON, summary, updatedAt string
	err = conn.QueryRow(
		`SELECT file_id, space_id, type, COALESCE(status, ''), COALESCE(title, ''),
		        COALESCE(tags_json, '[]'), COALESCE(summary, ''), updated_at
		 FROM objects WHERE id = ?`,
		id,
	).Scan(&fileID, &spaceID, &typeStr, &status, &title, &tagsJSON, &summary, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("show: no object with id %q (run `hypha index rebuild`?)", id)
	}
	if err != nil {
		return fmt.Errorf("show: query: %w", err)
	}

	absPath, err := resolveObjectPath(root, spaceID, fileID)
	if err != nil {
		return fmt.Errorf("show: resolve path: %w", err)
	}

	if *pathOnly {
		fmt.Println(absPath)
		return nil
	}

	if *jsonOut {
		var tags []string
		_ = json.Unmarshal([]byte(tagsJSON), &tags)
		out := map[string]any{
			"id":         id,
			"type":       typeStr,
			"space_id":   spaceID,
			"file_path":  absPath,
			"status":     status,
			"title":      title,
			"tags":       tags,
			"summary":    summary,
			"updated_at": updatedAt,
		}
		// --json selects the metadata slice. The envelope wraps it so the
		// shape matches the rest of the agent surface.
		return emit("show", out, "", nil)
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("show: read %s: %w", absPath, err)
	}
	front, body := splitFrontmatter(content)

	switch {
	case *frontOnly:
		os.Stdout.Write(front)
	case *bodyOnly:
		os.Stdout.Write(body)
	default:
		os.Stdout.Write(content)
	}
	return nil
}

// normalizeShowID accepts either a bare object id ("concept.spore"), a
// "hypha://<space>/object/<id>" recall URI, or a legacy "hypha://<space>/<id>"
// path-style URI, and returns the bare id suitable for an objects lookup.
func normalizeShowID(in string) string {
	id := strings.TrimSpace(in)
	if !strings.HasPrefix(id, "hypha://") {
		return id
	}
	rest := strings.TrimPrefix(id, "hypha://")
	if idx := strings.LastIndex(rest, "#"); idx >= 0 {
		rest = rest[:idx]
	}
	parts := strings.Split(rest, "/")
	// Find the last segment after an "object" marker, else just the last segment.
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == "object" && i+1 < len(parts) {
			return strings.Join(parts[i+1:], "/")
		}
	}
	return parts[len(parts)-1]
}

// resolveObjectPath joins installRoot/spaces/<authority>-<name>/<file_id>.
// spaceID may be either a full "hypha://<authority>/<name>" URI or the bare
// "<authority>/<name>" form (the indexer stores the bare form in objects.space_id).
// file_id is the path relative to the space directory.
func resolveObjectPath(installRoot, spaceID, fileID string) (string, error) {
	rest := strings.TrimPrefix(spaceID, "hypha://")
	rest = strings.TrimRight(rest, "/")
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 {
		return "", fmt.Errorf("space id missing authority/name: %q", spaceID)
	}
	dir := fmt.Sprintf("%s-%s", parts[0], parts[1])
	return filepath.Join(installRoot, "spaces", dir, fileID), nil
}

// splitFrontmatter separates the leading YAML frontmatter (delimited by ---)
// from the markdown body. If the document has no frontmatter, returns
// (nil, content). Preserves trailing newlines on both sides.
func splitFrontmatter(content []byte) (frontmatter, body []byte) {
	if !bytes.HasPrefix(content, []byte("---\n")) && !bytes.HasPrefix(content, []byte("---\r\n")) {
		return nil, content
	}
	// Find the closing "---" on its own line.
	rest := content[4:]
	idx := bytes.Index(rest, []byte("\n---\n"))
	if idx < 0 {
		idx = bytes.Index(rest, []byte("\n---\r\n"))
	}
	if idx < 0 {
		return nil, content
	}
	end := 4 + idx + len("\n---\n")
	return content[:end], content[end:]
}

// --- spaces ----------------------------------------------------------------

func cmdSpaces(args []string) error {
	if len(args) == 0 || args[0] != "list" {
		return errors.New("usage: hypha spaces list [--format text|json|compact]")
	}
	fs := flag.NewFlagSet("spaces list", flag.ContinueOnError)
	format := formatFlag(fs)
	if err := fs.Parse(reorderFlagsFirst(args[1:])); err != nil {
		return err
	}
	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	spaces, err := listSpaces(root)
	if err != nil {
		return err
	}
	return emit("spaces list", spaces, *format, func(w io.Writer, data any) error {
		sps, ok := data.([]spaceEntry)
		if !ok {
			return fmt.Errorf("spaces list: text renderer got %T", data)
		}
		if len(sps) == 0 {
			fmt.Fprintln(w, "(no spaces installed)")
			return nil
		}
		for _, sp := range sps {
			fmt.Fprintf(w, "  hypha://%s\n      %s\n", sp.URI, sp.Path)
		}
		fmt.Fprintf(os.Stderr, "\n(%d space(s))\n", len(sps))
		return nil
	})
}

// --- trace -----------------------------------------------------------------

func cmdTrace(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: hypha trace start|tick|done|list|history|tail|reap [...]")
	}
	switch args[0] {
	case "start":
		return cmdTraceStart(args[1:])
	case "tick":
		return cmdTraceTick(args[1:])
	case "done":
		return cmdTraceDone(args[1:])
	case "list":
		return cmdTraceList(args[1:])
	case "history":
		return cmdTraceHistory(args[1:])
	case "tail":
		return cmdTraceTail(args[1:])
	case "reap":
		return cmdTraceReap(args[1:])
	default:
		return fmt.Errorf("unknown trace subcommand %q (try `start`, `tick`, `done`, `list`, `history`, `tail`, `reap`)", args[0])
	}
}

// resolveSpaceForTrace picks the on-disk space root for trace I/O. If --space
// is provided it must match an installed space; otherwise the user must have
// exactly one installed space (or we error with a friendly message).
func resolveSpaceForTrace(root, spaceFlag string) (spaceRoot, spaceURI string, err error) {
	spaces, lerr := listSpaces(root)
	if lerr != nil {
		return "", "", lerr
	}
	if spaceFlag != "" {
		want := strings.TrimPrefix(spaceFlag, "hypha://")
		want = strings.TrimRight(want, "/")
		for _, s := range spaces {
			if s.URI == want {
				return s.Path, "hypha://" + s.URI, nil
			}
		}
		return "", "", fmt.Errorf("space %q not installed under %s/spaces", spaceFlag, root)
	}
	if len(spaces) == 0 {
		return "", "", errors.New("no spaces installed; pass --space <uri>")
	}
	if len(spaces) > 1 {
		return "", "", errors.New("multiple spaces installed; pass --space <uri>")
	}
	return spaces[0].Path, "hypha://" + spaces[0].URI, nil
}

func cmdTraceStart(args []string) error {
	fs := flag.NewFlagSet("trace start", flag.ContinueOnError)
	spaceFlag := fs.String("space", "", "space URI (required when multiple spaces are installed)")
	agent := fs.String("agent", "", "agent URI emitting the trace (required)")
	parent := fs.String("parent", "", "parent agent URI, if dispatched")
	session := fs.String("session", "", "session identifier, if applicable")
	taskRef := fs.String("task", "", "task identifier this trace covers")
	phase := fs.String("phase", "", "short phase label")
	format := formatFlag(fs)
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}
	if strings.TrimSpace(*agent) == "" {
		return errors.New("--agent <uri> is required")
	}

	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	spaceRoot, spaceURI, err := resolveSpaceForTrace(root, *spaceFlag)
	if err != nil {
		return err
	}

	tr, err := trace.Start(trace.StartOpts{
		SpaceRoot:    spaceRoot,
		SpaceID:      spaceURI,
		AgentID:      *agent,
		AgentParent:  *parent,
		AgentSession: *session,
		TaskRef:      *taskRef,
		Phase:        *phase,
	})
	if err != nil {
		return err
	}
	crdtshadow.MirrorTrace(root, tr)

	return emit("trace start", tr, *format, func(w io.Writer, data any) error {
		t, ok := data.(types.Trace)
		if !ok {
			return fmt.Errorf("trace start: text renderer got %T", data)
		}
		fmt.Fprintln(w, t.ID)
		fmt.Fprintf(os.Stderr, "  status:   %s\n  agent:    %s\n  started:  %s\n  file:     %s\n",
			t.Status, t.AgentID, t.Started.Format(time.RFC3339), t.FilePath)
		return nil
	})
}

func cmdTraceTick(args []string) error {
	fs := flag.NewFlagSet("trace tick", flag.ContinueOnError)
	spaceFlag := fs.String("space", "", "space URI (default: only installed space)")
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}
	if fs.NArg() < 2 {
		return errors.New("usage: hypha trace tick <trace-id> \"<checkpoint message>\"")
	}
	traceID := fs.Arg(0)
	msg := strings.Join(fs.Args()[1:], " ")

	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	spaceRoot, spaceURI, err := resolveSpaceForTrace(root, *spaceFlag)
	if err != nil {
		return err
	}
	if err := trace.Tick(spaceRoot, traceID, msg); err != nil {
		return err
	}
	// Best-effort mirror: re-load the trace to capture the new tick state.
	if updated, lerr := trace.LoadByID(spaceRoot, traceID); lerr == nil {
		if updated.SpaceID == "" {
			updated.SpaceID = spaceURI
		}
		crdtshadow.MirrorTrace(root, updated)
	}
	fmt.Fprintf(os.Stderr, "tick: %s  %s\n", traceID, msg)
	return nil
}

func cmdTraceDone(args []string) error {
	fs := flag.NewFlagSet("trace done", flag.ContinueOnError)
	status := fs.String("status", "succeeded", "succeeded | failed | killed | superseded")
	linked := fs.String("link-spore", "", "spore id to attribute the work log to (optional)")
	spaceFlag := fs.String("space", "", "space URI (default: only installed space)")
	format := formatFlag(fs)
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: hypha trace done <trace-id> [--status ...] [--link-spore <id>]")
	}
	traceID := fs.Arg(0)

	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	spaceRoot, _, err := resolveSpaceForTrace(root, *spaceFlag)
	if err != nil {
		return err
	}
	tr, err := trace.Done(spaceRoot, traceID, *status, *linked)
	if err != nil {
		return err
	}
	crdtshadow.MirrorTrace(root, tr)

	return emit("trace done", tr, *format, func(w io.Writer, data any) error {
		t, ok := data.(types.Trace)
		if !ok {
			return fmt.Errorf("trace done: text renderer got %T", data)
		}
		fmt.Fprintf(os.Stderr, "trace: %s\n  status:        %s\n  ticks:         %d\n  linked_spore:  %s\n  file:          %s\n",
			t.ID, t.Status, len(t.Ticks), nonEmpty(t.LinkedSpore, "(none)"), t.FilePath)
		return nil
	})
}

func cmdTraceList(args []string) error {
	fs := flag.NewFlagSet("trace list", flag.ContinueOnError)
	activeOnly := fs.Bool("active", false, "only currently-open traces")
	agent := fs.String("agent", "", "exact agent URI match")
	spaceFlag := fs.String("space", "", "space URI (default: all installed spaces)")
	format := formatFlag(fs)
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}

	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	spaces, err := listSpaces(root)
	if err != nil {
		return err
	}

	var out []traceListRow

	for _, sp := range spaces {
		if *spaceFlag != "" && !spaceMatches(sp, *spaceFlag) {
			continue
		}
		traces, terr := trace.List(sp.Path, trace.ListFilter{ActiveOnly: *activeOnly, Agent: *agent})
		if terr != nil {
			fmt.Fprintf(os.Stderr, "warn: list %s: %v\n", sp.URI, terr)
			continue
		}
		for _, t := range traces {
			out = append(out, traceListRow{
				ID:       t.ID,
				Space:    t.SpaceID,
				Agent:    t.AgentID,
				Status:   t.Status,
				Started:  t.Started,
				LastTick: t.LastTick,
				Ticks:    len(t.Ticks),
				Phase:    t.Phase,
				Path:     t.FilePath,
			})
		}
	}

	return emit("trace list", out, *format, func(w io.Writer, data any) error {
		rows, ok := data.([]traceListRow)
		if !ok {
			return fmt.Errorf("trace list: text renderer got %T", data)
		}
		if len(rows) == 0 {
			fmt.Fprintln(w, "(no traces)")
			return nil
		}
		for _, r := range rows {
			phase := ""
			if r.Phase != "" {
				phase = "  phase=" + r.Phase
			}
			fmt.Fprintf(w, "%s  %-10s  ticks=%-2d  %s\n      agent=%s%s\n",
				r.LastTick.Format("2006-01-02 15:04"), r.Status, r.Ticks, r.ID, r.Agent, phase)
		}
		fmt.Fprintf(os.Stderr, "\n(%d traces)\n", len(rows))
		return nil
	})
}

func cmdTraceReap(args []string) error {
	fs := flag.NewFlagSet("trace reap", flag.ContinueOnError)
	olderStr := fs.String("older-than", "1h", "max time since last_tick before an open trace is considered stale")
	spaceFlag := fs.String("space", "", "space URI (default: all installed spaces)")
	format := formatFlag(fs)
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}
	older, err := parseFlexDuration(*olderStr)
	if err != nil {
		return fmt.Errorf("--older-than %q: %w", *olderStr, err)
	}

	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	spaces, err := listSpaces(root)
	if err != nil {
		return err
	}

	type spaceReap struct {
		Space  string           `json:"space"`
		Report trace.ReapReport `json:"report"`
	}
	var all []spaceReap
	totalReaped := 0
	for _, sp := range spaces {
		if *spaceFlag != "" && !spaceMatches(sp, *spaceFlag) {
			continue
		}
		rep, rerr := trace.Reap(sp.Path, older)
		if rerr != nil {
			fmt.Fprintf(os.Stderr, "warn: reap %s: %v\n", sp.URI, rerr)
			continue
		}
		all = append(all, spaceReap{Space: "hypha://" + sp.URI, Report: rep})
		totalReaped += len(rep.Reaped)
		// Mirror the reaped (now-killed) traces.
		for _, reaped := range rep.Reaped {
			if updated, lerr := trace.LoadByID(sp.Path, reaped.ID); lerr == nil {
				if updated.SpaceID == "" {
					updated.SpaceID = "hypha://" + sp.URI
				}
				crdtshadow.MirrorTrace(root, updated)
			}
		}
	}

	payload := map[string]any{
		"older_than":   older.String(),
		"spaces":       all,
		"total_reaped": totalReaped,
	}
	return emit("trace reap", payload, *format, func(w io.Writer, _ any) error {
		if totalReaped == 0 {
			fmt.Fprintf(w, "no stale open traces (threshold %s)\n", older)
			return nil
		}
		fmt.Fprintf(w, "reaped %d stale open trace(s) (threshold %s)\n", totalReaped, older)
		for _, sr := range all {
			for _, r := range sr.Report.Reaped {
				fmt.Fprintf(w, "  %s  stale=%s  %s\n", r.ID, r.StaleFor, r.AgentID)
			}
		}
		return nil
	})
}

type traceListRow struct {
	ID       string    `json:"id"`
	Space    string    `json:"space"`
	Agent    string    `json:"agent"`
	Status   string    `json:"status"`
	Started  time.Time `json:"started"`
	LastTick time.Time `json:"last_tick"`
	Ticks    int       `json:"ticks"`
	Phase    string    `json:"phase,omitempty"`
	Path     string    `json:"path"`
}

// cmdTraceHistory queries the FTS5 index for closed traces matching --similar
// (free-text), optionally filtered by --task or --agent. Methodology recall:
// "how was a similar problem approached before."
func cmdTraceHistory(args []string) error {
	fs := flag.NewFlagSet("trace history", flag.ContinueOnError)
	similar := fs.String("similar", "", "free-text query against trace bodies")
	taskRef := fs.String("task", "", "filter to traces with this task_ref")
	agent := fs.String("agent", "", "filter to traces from this agent URI")
	includeOpen := fs.Bool("include-open", false, "include currently-open traces (default: closed only)")
	limit := fs.Int("limit", 10, "max results")
	spaceFlag := fs.String("space", "", "scope FTS to one space URI")
	format := formatFlag(fs)
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}
	if *similar == "" && *taskRef == "" && *agent == "" {
		return errors.New("usage: hypha trace history [--similar <q>] [--task <id>] [--agent <uri>]")
	}

	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	spaces, err := listSpaces(root)
	if err != nil {
		return err
	}

	var out []traceHistoryRow

	var fts5Matches map[string]float64 // id → BM25 rank (lower is better)
	if *similar != "" {
		conn, derr := openIndex(root)
		if derr != nil {
			return derr
		}
		defer conn.Close()

		sanitized := sanitizeTraceQuery(*similar)
		if sanitized != "" {
			ftsSQL := `
SELECT f.id, bm25(objects_fts, 3.0, 2.0, 2.0, 1.0) AS rank
FROM objects_fts f
WHERE objects_fts MATCH ?
  AND f.type = 'trace'
ORDER BY rank
LIMIT ?`
			rows, qerr := conn.Query(ftsSQL, sanitized, *limit*5)
			if qerr != nil {
				return fmt.Errorf("trace history: fts: %w", qerr)
			}
			defer rows.Close()
			fts5Matches = make(map[string]float64)
			for rows.Next() {
				var id string
				var rank float64
				if err := rows.Scan(&id, &rank); err != nil {
					return err
				}
				fts5Matches[id] = rank
			}
		}
	}

	for _, sp := range spaces {
		if *spaceFlag != "" && !spaceMatches(sp, *spaceFlag) {
			continue
		}
		traces, lerr := trace.List(sp.Path, trace.ListFilter{Agent: *agent})
		if lerr != nil {
			continue
		}
		for _, t := range traces {
			if !*includeOpen && t.Status == types.TraceStatusOpen {
				continue
			}
			if *taskRef != "" && t.TaskRef != *taskRef {
				continue
			}
			if *similar != "" {
				if _, ok := fts5Matches[t.ID]; !ok {
					continue
				}
			}
			out = append(out, traceHistoryRow{
				ID:       t.ID,
				Space:    t.SpaceID,
				Agent:    t.AgentID,
				Status:   t.Status,
				TaskRef:  t.TaskRef,
				Phase:    t.Phase,
				Ticks:    len(t.Ticks),
				LastTick: t.LastTick,
				Path:     t.FilePath,
			})
		}
	}

	// Order: if similar was given, by FTS rank (lower = better); else by recency.
	if *similar != "" && fts5Matches != nil {
		sort.SliceStable(out, func(i, j int) bool {
			return fts5Matches[out[i].ID] < fts5Matches[out[j].ID]
		})
	} else {
		sort.SliceStable(out, func(i, j int) bool { return out[i].LastTick.After(out[j].LastTick) })
	}
	if len(out) > *limit {
		out = out[:*limit]
	}

	return emit("trace history", out, *format, func(w io.Writer, data any) error {
		rows, ok := data.([]traceHistoryRow)
		if !ok {
			return fmt.Errorf("trace history: text renderer got %T", data)
		}
		if len(rows) == 0 {
			fmt.Fprintln(w, "(no matching traces)")
			return nil
		}
		for _, r := range rows {
			task := ""
			if r.TaskRef != "" {
				task = "  task=" + r.TaskRef
			}
			phase := ""
			if r.Phase != "" {
				phase = "  phase=" + r.Phase
			}
			fmt.Fprintf(w, "%s  %-10s  ticks=%-2d  %s\n      agent=%s%s%s\n",
				r.LastTick.Format("2006-01-02 15:04"), r.Status, r.Ticks, r.ID, r.Agent, task, phase)
		}
		fmt.Fprintf(os.Stderr, "\n(%d traces)\n", len(rows))
		return nil
	})
}

type traceHistoryRow struct {
	ID       string    `json:"id"`
	Space    string    `json:"space"`
	Agent    string    `json:"agent"`
	Status   string    `json:"status"`
	TaskRef  string    `json:"task_ref,omitempty"`
	Phase    string    `json:"phase,omitempty"`
	Ticks    int       `json:"ticks"`
	LastTick time.Time `json:"last_tick"`
	Path     string    `json:"path"`
	Snippet  string    `json:"snippet,omitempty"`
}

// sanitizeTraceQuery mirrors recall's strip-to-alphanum approach so FTS5 won't
// choke on punctuation in user queries.
func sanitizeTraceQuery(q string) string {
	var b strings.Builder
	b.Grow(len(q))
	for _, r := range q {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// cmdTraceTail polls trace files and prints new ticks as they arrive. No
// fsnotify dependency — just compare ticks-len at a polling interval until
// timeout / signal / max-ticks reached.
func cmdTraceTail(args []string) error {
	fs := flag.NewFlagSet("trace tail", flag.ContinueOnError)
	traceID := fs.String("id", "", "specific trace id to tail (default: all open traces)")
	agent := fs.String("agent", "", "tail only traces from this agent URI")
	spaceFlag := fs.String("space", "", "scope to one space URI")
	interval := fs.Duration("interval", time.Second, "polling interval")
	timeout := fs.Duration("timeout", 5*time.Minute, "stop after this duration (0 = forever)")
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}

	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	spaces, err := listSpaces(root)
	if err != nil {
		return err
	}

	// Find traces to tail.
	type tailTarget struct {
		spaceRoot string
		id        string
		seen      int
	}
	var targets []tailTarget
	collect := func() error {
		targets = targets[:0]
		for _, sp := range spaces {
			if *spaceFlag != "" && !spaceMatches(sp, *spaceFlag) {
				continue
			}
			traces, lerr := trace.List(sp.Path, trace.ListFilter{ActiveOnly: true, Agent: *agent})
			if lerr != nil {
				continue
			}
			for _, t := range traces {
				if *traceID != "" && t.ID != *traceID {
					continue
				}
				targets = append(targets, tailTarget{spaceRoot: sp.Path, id: t.ID, seen: len(t.Ticks)})
			}
		}
		return nil
	}
	if err := collect(); err != nil {
		return err
	}
	if len(targets) == 0 {
		fmt.Println("(no open traces to tail)")
		return nil
	}
	for _, t := range targets {
		fmt.Fprintf(os.Stderr, "tailing %s (initial ticks=%d)\n", t.id, t.seen)
	}

	deadline := time.Time{}
	if *timeout > 0 {
		deadline = time.Now().Add(*timeout)
	}

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for {
		if !deadline.IsZero() && time.Now().After(deadline) {
			fmt.Fprintln(os.Stderr, "(timeout reached)")
			return nil
		}
		for i, t := range targets {
			cur, err := trace.LoadByID(t.spaceRoot, t.id)
			if err != nil {
				continue
			}
			if len(cur.Ticks) > t.seen {
				for _, tk := range cur.Ticks[t.seen:] {
					fmt.Printf("%s  %s  %s\n", tk.At.Format(time.RFC3339), t.id, tk.Message)
				}
				targets[i].seen = len(cur.Ticks)
			}
			if cur.Status != types.TraceStatusOpen {
				fmt.Fprintf(os.Stderr, "(%s closed with status=%s)\n", t.id, cur.Status)
				// Drop this target.
				targets = append(targets[:i], targets[i+1:]...)
				if len(targets) == 0 {
					return nil
				}
				break
			}
		}
		<-ticker.C
	}
}

// --- analyze ---------------------------------------------------------------

func cmdAnalyze(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: hypha analyze <kind> [target] | list | refresh <id>")
	}
	switch args[0] {
	case "list":
		return cmdAnalyzeList(args[1:])
	case "refresh":
		return cmdAnalyzeRefresh(args[1:])
	case "impact", "callgraph", "refs", "hotspot", "dead", "review":
		return cmdAnalyzeRun(args[0], args[1:])
	default:
		return fmt.Errorf("unknown analyze kind/subcommand %q (try impact|callgraph|refs|hotspot|dead|review|list|refresh)", args[0])
	}
}

// resolveSourceForSpace picks the source repo path for an installed space.
// Convention: `~/work/<basename-of-space-uri>`. Override with --source.
func resolveSourceForSpace(spaceURI, sourceFlag string) (string, error) {
	if strings.TrimSpace(sourceFlag) != "" {
		return sourceFlag, nil
	}
	rest := strings.TrimPrefix(spaceURI, "hypha://")
	rest = strings.TrimRight(rest, "/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) < 2 {
		return "", fmt.Errorf("malformed space uri %q", spaceURI)
	}
	home, _ := os.UserHomeDir()
	candidate := filepath.Join(home, "work", parts[1])
	if _, err := os.Stat(filepath.Join(candidate, ".git")); err != nil {
		return "", fmt.Errorf("no git repo at %s (pass --source <path>)", candidate)
	}
	return candidate, nil
}

func cmdAnalyzeRun(kind string, args []string) error {
	fs := flag.NewFlagSet("analyze "+kind, flag.ContinueOnError)
	spaceFlag := fs.String("space", "", "space URI (default: only installed space)")
	source := fs.String("source", "", "source repo path (default: ~/work/<space-basename>)")
	diffRef := fs.String("diff-ref", "", "git ref for kinds that diff (impact, review)")
	maxDepth := fs.Int("max-depth", 0, "max reverse-call depth (impact, callgraph)")
	refresh := fs.Bool("refresh", false, "ignore cached analysis and re-run canopy")
	format := formatFlag(fs)
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}
	target := ""
	if fs.NArg() > 0 {
		target = fs.Arg(0)
	}

	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	spaceRoot, spaceURI, err := resolveSpaceForTrace(root, *spaceFlag)
	if err != nil {
		return err
	}
	sourcePath, err := resolveSourceForSpace(spaceURI, *source)
	if err != nil {
		return err
	}

	// Cache check (skip when --refresh).
	if !*refresh {
		existing, _ := analyze.List(spaceRoot, analyze.ListFilter{
			Kind:       kind,
			TargetFile: target,
		})
		if len(existing) > 0 {
			// Pick newest matching target.
			for _, a := range existing {
				if a.Target == target || (target == "" && a.Target == "repo") {
					checkAndAnnotateFreshness(&a, sourcePath)
					return emitAnalysis(a, *format)
				}
			}
		}
	}

	a, err := analyze.Run(analyze.RunOpts{
		Kind:       kind,
		Target:     target,
		SourcePath: sourcePath,
		SpaceRoot:  spaceRoot,
		SpaceID:    spaceURI,
		MaxDepth:   *maxDepth,
		DiffRef:    *diffRef,
	})
	if err != nil {
		return err
	}
	return emitAnalysis(a, *format)
}

func cmdAnalyzeList(args []string) error {
	fs := flag.NewFlagSet("analyze list", flag.ContinueOnError)
	kindFilter := fs.String("kind", "", "filter by kind (impact|callgraph|refs|hotspot|dead|review)")
	targetFile := fs.String("target-file", "", "match analyses whose target_files include this path")
	spaceFlag := fs.String("space", "", "space URI (default: all installed spaces)")
	format := formatFlag(fs)
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}

	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	spaces, err := listSpaces(root)
	if err != nil {
		return err
	}

	var out []analyzeListRow
	for _, sp := range spaces {
		if *spaceFlag != "" && !spaceMatches(sp, *spaceFlag) {
			continue
		}
		list, lerr := analyze.List(sp.Path, analyze.ListFilter{Kind: *kindFilter, TargetFile: *targetFile})
		if lerr != nil {
			fmt.Fprintf(os.Stderr, "warn: list %s: %v\n", sp.URI, lerr)
			continue
		}
		sourcePath, _ := resolveSourceForSpace("hypha://"+sp.URI, "")
		for _, a := range list {
			checkAndAnnotateFreshness(&a, sourcePath)
			out = append(out, analyzeListRow{
				ID:            a.ID,
				Kind:          a.Kind,
				Target:        a.Target,
				Commit:        a.Commit,
				ComputedAt:    a.ComputedAt,
				Stale:         a.Stale,
				TotalAffected: a.TotalAffected,
				Path:          a.FilePath,
			})
		}
	}

	return emit("analyze list", out, *format, func(w io.Writer, data any) error {
		rows, ok := data.([]analyzeListRow)
		if !ok {
			return fmt.Errorf("analyze list: text renderer got %T", data)
		}
		if len(rows) == 0 {
			fmt.Fprintln(w, "(no analyses)")
			return nil
		}
		for _, r := range rows {
			staleTag := ""
			if r.Stale {
				staleTag = "  STALE"
			}
			fmt.Fprintf(w, "%s  %-10s  %s  @%s%s\n      %s\n",
				r.ComputedAt.Format("2006-01-02 15:04"), r.Kind, r.Target, r.Commit, staleTag, r.ID)
		}
		fmt.Fprintf(os.Stderr, "\n(%d analyses)\n", len(rows))
		return nil
	})
}

type analyzeListRow struct {
	ID            string    `json:"id"`
	Kind          string    `json:"kind"`
	Target        string    `json:"target"`
	Commit        string    `json:"commit"`
	ComputedAt    time.Time `json:"computed_at"`
	Stale         bool      `json:"stale"`
	TotalAffected int       `json:"total_affected,omitempty"`
	Path          string    `json:"path"`
}

func cmdAnalyzeRefresh(args []string) error {
	fs := flag.NewFlagSet("analyze refresh", flag.ContinueOnError)
	spaceFlag := fs.String("space", "", "space URI (default: only installed space)")
	source := fs.String("source", "", "source repo path (default: ~/work/<space-basename>)")
	format := formatFlag(fs)
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: hypha analyze refresh <analysis-id>")
	}
	id := fs.Arg(0)

	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	spaces, err := listSpaces(root)
	if err != nil {
		return err
	}
	// Find the analysis across spaces.
	var match types.Analysis
	var spaceRoot, spaceURI string
	for _, sp := range spaces {
		if *spaceFlag != "" && !spaceMatches(sp, *spaceFlag) {
			continue
		}
		list, _ := analyze.List(sp.Path, analyze.ListFilter{})
		for _, a := range list {
			if a.ID == id {
				match = a
				spaceRoot = sp.Path
				spaceURI = "hypha://" + sp.URI
				break
			}
		}
		if match.ID != "" {
			break
		}
	}
	if match.ID == "" {
		return fmt.Errorf("analyze refresh: no analysis with id %q", id)
	}
	sourcePath, err := resolveSourceForSpace(spaceURI, *source)
	if err != nil {
		return err
	}
	a, err := analyze.Run(analyze.RunOpts{
		Kind:       match.Kind,
		Target:     match.Target,
		SourcePath: sourcePath,
		SpaceRoot:  spaceRoot,
		SpaceID:    spaceURI,
	})
	if err != nil {
		return err
	}
	return emitAnalysis(a, *format)
}

// checkAndAnnotateFreshness updates a.Stale based on source-repo state.
// Silently no-ops if the source path is missing or git isn't available.
func checkAndAnnotateFreshness(a *types.Analysis, sourcePath string) {
	if sourcePath == "" {
		return
	}
	currentCommit, _ := func() (string, error) {
		cmd := exec.Command("git", "rev-parse", "HEAD")
		cmd.Dir = sourcePath
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}()
	mtimes := make(map[string]time.Time, len(a.TargetFiles))
	for _, f := range a.TargetFiles {
		if info, err := os.Stat(filepath.Join(sourcePath, f)); err == nil {
			mtimes[f] = info.ModTime().UTC()
		}
	}
	_ = analyze.CheckFreshness(a, analyze.FreshnessInputs{
		CurrentCommit: currentCommit,
		TargetMtimes:  mtimes,
	})
}

func emitAnalysis(a types.Analysis, format string) error {
	return emit("analyze", a, format, func(w io.Writer, data any) error {
		an, ok := data.(types.Analysis)
		if !ok {
			return fmt.Errorf("analyze: text renderer got %T", data)
		}
		staleTag := ""
		if an.Stale {
			staleTag = "  [STALE — re-run with `hypha analyze refresh " + an.ID + "`]"
		}
		fmt.Fprintf(w, "Analysis:    %s%s\n", an.ID, staleTag)
		fmt.Fprintf(w, "Kind:        %s\n", an.Kind)
		fmt.Fprintf(w, "Target:      %s\n", an.Target)
		if an.Commit != "" {
			fmt.Fprintf(w, "Commit:      %s\n", an.Commit)
		}
		fmt.Fprintf(w, "Computed at: %s\n", an.ComputedAt.Format(time.RFC3339))
		if an.TotalAffected > 0 {
			fmt.Fprintf(w, "Affected:    %d symbols across %d files\n", an.TotalAffected, len(an.TopFiles))
		}
		if len(an.TopFiles) > 0 {
			fmt.Fprintln(w, "\nTop files:")
			for _, f := range an.TopFiles {
				fmt.Fprintf(w, "  - %s\n", f)
			}
		}
		if len(an.TopSymbols) > 0 {
			fmt.Fprintln(w, "\nTop symbols:")
			for _, s := range an.TopSymbols {
				fmt.Fprintf(w, "  - %s\n", s)
			}
		}
		fmt.Fprintf(w, "\nFile: %s\n", an.FilePath)
		return nil
	})
}

// --- graph -----------------------------------------------------------------

func cmdGraph(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: hypha graph backlinks|related|trace <object-id> [flags]")
	}
	switch args[0] {
	case "backlinks":
		return cmdGraphLinks(args[1:], graph.Backlinks)
	case "related":
		return cmdGraphLinks(args[1:], graph.Related)
	case "trace":
		return cmdGraphTrace(args[1:])
	default:
		return fmt.Errorf("unknown graph subcommand %q", args[0])
	}
}

type linksFunc func(*sql.DB, string, []types.EdgeKind, int) ([]graph.Neighbor, error)

func cmdGraphLinks(args []string, fn linksFunc) error {
	fs := flag.NewFlagSet("graph links", flag.ContinueOnError)
	kindStr := fs.String("kind", "", "comma-separated edge kinds to filter (default: all)")
	limit := fs.Int("limit", 50, "max results")
	format := formatFlag(fs)
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("missing <object-id>")
	}
	objectID := fs.Arg(0)

	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	conn, err := openIndex(root)
	if err != nil {
		return err
	}
	defer conn.Close()

	out, err := fn(conn, objectID, parseEdgeKinds(*kindStr), *limit)
	if err != nil {
		return err
	}
	return emit("graph links", out, *format, func(w io.Writer, data any) error {
		neighbors, ok := data.([]graph.Neighbor)
		if !ok {
			return fmt.Errorf("graph links: text renderer got %T", data)
		}
		if len(neighbors) == 0 {
			fmt.Fprintln(w, "(no neighbors)")
			return nil
		}
		for _, n := range neighbors {
			fmt.Fprintf(w, "  %-16s  %s\n", n.Edge.Kind, n.Endpoint)
		}
		fmt.Fprintf(os.Stderr, "\n(%d neighbors)\n", len(neighbors))
		return nil
	})
}

func cmdGraphTrace(args []string) error {
	fs := flag.NewFlagSet("graph trace", flag.ContinueOnError)
	kindStr := fs.String("kind", "derived_from,cites,source_ref", "comma-separated edge kinds to follow")
	maxDepth := fs.Int("max-depth", 4, "max BFS depth")
	format := formatFlag(fs)
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: hypha graph trace <object-id> [--kind k1,k2] [--max-depth N]")
	}
	objectID := fs.Arg(0)

	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	conn, err := openIndex(root)
	if err != nil {
		return err
	}
	defer conn.Close()

	steps, err := graph.Trace(conn, objectID, parseEdgeKinds(*kindStr), *maxDepth)
	if err != nil {
		return err
	}
	return emit("graph trace", steps, *format, func(w io.Writer, data any) error {
		ss, ok := data.([]graph.TraceStep)
		if !ok {
			return fmt.Errorf("graph trace: text renderer got %T", data)
		}
		if len(ss) == 0 {
			fmt.Fprintln(w, "(no path)")
			return nil
		}
		for _, s := range ss {
			fmt.Fprintf(w, "  depth=%d  %-16s  %s → %s\n", s.HopDepth, s.Edge.Kind, s.From, s.To)
		}
		fmt.Fprintf(os.Stderr, "\n(%d steps)\n", len(ss))
		return nil
	})
}

func parseEdgeKinds(s string) []types.EdgeKind {
	if s == "" {
		return nil
	}
	parts := splitCSV(s)
	out := make([]types.EdgeKind, 0, len(parts))
	for _, p := range parts {
		out = append(out, types.EdgeKind(p))
	}
	return out
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
	format := formatFlag(fs)
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

	return emit("receipts list", out, *format, func(w io.Writer, data any) error {
		rs, ok := data.([]types.Receipt)
		if !ok {
			return fmt.Errorf("receipts list: text renderer got %T", data)
		}
		if len(rs) == 0 {
			fmt.Fprintln(w, "(no receipts)")
			return nil
		}
		for _, r := range rs {
			fmt.Fprintf(w, "%s  %-16s  %s\n      %s\n",
				r.CreatedAt.Format("2006-01-02 15:04"), r.Action, r.SubjectID, r.ID)
		}
		fmt.Fprintf(os.Stderr, "\n(%d receipts)\n", len(rs))
		return nil
	})
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
