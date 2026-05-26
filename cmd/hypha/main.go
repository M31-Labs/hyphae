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
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/odvcencio/hyphae/internal/assess"
	"github.com/odvcencio/hyphae/internal/capability"
	"github.com/odvcencio/hyphae/internal/db"
	"github.com/odvcencio/hyphae/internal/graft"
	"github.com/odvcencio/hyphae/internal/graph"
	"github.com/odvcencio/hyphae/internal/identity"
	"github.com/odvcencio/hyphae/internal/parser"
	"github.com/odvcencio/hyphae/internal/pulse"
	"github.com/odvcencio/hyphae/internal/recall"
	"github.com/odvcencio/hyphae/internal/receipts"
	"github.com/odvcencio/hyphae/internal/spore"
	"github.com/odvcencio/hyphae/internal/types"
)

const usage = `hypha — Hyphae v0.1.3 CLI

Usage:
  hypha index    rebuild [--root <path>]
  hypha recall   <query> [--limit N] [--max-tokens N] [--shape headline|summary+anchors] [--format json|text]
  hypha spore    submit <file> [--sign --as <identity-uri>]
  hypha cap      issue --subject <uri> --space <uri> [--permissions p1,p2] [--expires 24h]
  hypha identity init --name <name> --authority <auth> --space <uri> [--expires 1y]
  hypha identity list
  hypha graft    <spore-id> --as <identity-uri> [--space <hypha-uri>] [--verify]
  hypha graph    backlinks <object-id> [--kind k1,k2] [--limit N]
  hypha graph    related   <object-id> [--kind k1,k2] [--limit N]
  hypha graph    trace     <object-id> [--kind derived_from,cites] [--max-depth 4]
  hypha pulse    [--space <uri>] [--window 30d] [--ttl 5m] [--format json|text]
  hypha assess   change --task <text> [--files p1,p2] [--diff-summary <text>] [--space <uri>] [--window 30d] [--format json|text]
  hypha show     <id-or-hypha-uri> [--path] [--json] [--frontmatter] [--body]
  hypha receipts list [--space <uri>] [--subject <uri>] [--action <name>] [--since 24h] [--limit N]

Separate binary for the browser visualization (GoSX-based):
  hypha-viz       [--addr 127.0.0.1:7777] [--root <hyphae-home>]

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
	case "graph":
		return cmdGraph(rest)
	case "pulse":
		return cmdPulse(rest)
	case "assess":
		return cmdAssess(rest)
	case "show":
		return cmdShow(rest)
	default:
		return fmt.Errorf("unknown command %q (try `hypha help`)", group)
	}
}

// --- pulse -----------------------------------------------------------------

func cmdPulse(args []string) error {
	fs := flag.NewFlagSet("pulse", flag.ContinueOnError)
	spaceURI := fs.String("space", "", "filter by space URI (default: all spaces)")
	windowStr := fs.String("window", "30d", "Go duration window (e.g. 7d, 30d, q2 → 90d)")
	ttlStr := fs.String("ttl", "5m", "cache TTL; pass 0 to force recompute")
	format := fs.String("format", "json", "json | text")
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

	switch *format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(p)
	case "text":
		printPulseText(os.Stdout, p)
		return nil
	default:
		return fmt.Errorf("unknown --format %q", *format)
	}
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
		return errors.New("usage: hypha assess change --task <text> [--files p1,p2] [--diff-summary <text>] [...]")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "change":
		return cmdAssessChange(rest)
	default:
		return fmt.Errorf("unknown assess subcommand %q (try `change`)", sub)
	}
}

func cmdAssessChange(args []string) error {
	fs := flag.NewFlagSet("assess change", flag.ContinueOnError)
	task := fs.String("task", "", "natural-language description of the proposed change")
	filesCSV := fs.String("files", "", "comma-separated list of changed file paths")
	diffSummary := fs.String("diff-summary", "", "short summary of the diff")
	spaceURI := fs.String("space", "", "filter scoring to one space URI (default: all spaces)")
	windowStr := fs.String("window", "30d", "Go duration window for recent-pressure aggregation")
	budgetTokens := fs.Int("budget-tokens", 1200, "soft response token budget (advisory)")
	format := fs.String("format", "json", "json | text")
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

	res, err := assess.Change(conn, req)
	if err != nil {
		return err
	}

	switch *format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	case "text":
		printAssessText(os.Stdout, res)
		return nil
	default:
		return fmt.Errorf("unknown --format %q", *format)
	}
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
		fmt.Fprintf(w, "\nHot zone: %s  (grafts/14d=%d, incidents/14d=%d)\n",
			r.HotZone.Path, r.HotZone.Commits14d, r.HotZone.Incidents14d)
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

	fmt.Fprintf(os.Stderr, "indexed %d objects, %d anchors, %d edges across %d space(s)\n",
		totalObj, totalAnc, totalEdg, len(spaces))
	fmt.Fprintf(os.Stderr, "db: %s\n", dbPath)
	return nil
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
		return errors.New("usage: hypha spore submit <file> [--sign --as <identity-uri>]")
	}
	rest := args[1:]
	fs := flag.NewFlagSet("spore submit", flag.ContinueOnError)
	sign := fs.Bool("sign", false, "Ed25519-sign the spore before submission")
	signer := fs.String("as", "", "signer identity URI (required with --sign)")
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

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(map[string]any{
		"receipt":  receipt,
		"filePath": filePath,
		"signed":   *sign,
	}); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\nReported back to Hyphae: %s\n", filePath)
	return nil
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
		return errors.New("usage: hypha graft <spore-id> --as <identity-uri> [--space <hypha-uri>] [--verify]")
	}
	fs := flag.NewFlagSet("graft", flag.ContinueOnError)
	grafter := fs.String("as", "", "grafter identity URI (recorded in the receipt)")
	spaceURI := fs.String("space", "", "space URI override (auto-detected from inbox if omitted)")
	verify := fs.Bool("verify", false, "verify Ed25519 signature on the spore before applying")
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
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
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
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func cmdGraphTrace(args []string) error {
	fs := flag.NewFlagSet("graph trace", flag.ContinueOnError)
	kindStr := fs.String("kind", "derived_from,cites,source_ref", "comma-separated edge kinds to follow")
	maxDepth := fs.Int("max-depth", 4, "max BFS depth")
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
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(steps)
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
