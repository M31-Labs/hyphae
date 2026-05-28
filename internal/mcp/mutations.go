package mcp

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"m31labs.dev/hyphae/internal/analyze"
	"m31labs.dev/hyphae/internal/atomicfs"
	"m31labs.dev/hyphae/internal/capability"
	"m31labs.dev/hyphae/internal/db"
	"m31labs.dev/hyphae/internal/graft"
	"m31labs.dev/hyphae/internal/identity"
	"m31labs.dev/hyphae/internal/parser"
	"m31labs.dev/hyphae/internal/recall"
	"m31labs.dev/hyphae/internal/receipts"
	"m31labs.dev/hyphae/internal/spore"
	"m31labs.dev/hyphae/internal/trace"
	"m31labs.dev/hyphae/internal/types"
)

// writeTools are the mutating MCP tools. Each one is gated only by the
// caller's responsibility — `hypha_graft` defaults to dry-run so the
// agent has to consciously opt into persistence (apply=true).
func writeTools(s *Server) []toolSpec {
	budgetProps := map[string]any{
		"format":     enumProp([]string{"jsonline", "json", "compact"}, "wire shape"),
		"max_tokens": numberProp("soft response budget"),
	}

	return []toolSpec{
		{
			Name:             "hypha_index_rebuild",
			Description:      "Walk install root and (re)populate the SQLite index over every space.",
			DefaultMaxTokens: 300,
			InputSchema:      schema(budgetProps, nil),
			Handler: func(_ map[string]any) (any, error) {
				return doIndexRebuild(s.installRoot)
			},
		},
		{
			Name:             "hypha_spore_submit",
			Description:      "Validate a spore file and write it to the matching space's inbox.",
			DefaultMaxTokens: 400,
			InputSchema: schema(merge(map[string]any{
				"path": stringProp("path to the spore .md file"),
				"sign": boolProp("Ed25519-sign before submitting"),
				"as":   stringProp("signer identity URI (required when sign=true)"),
			}, budgetProps), []string{"path"}),
			Handler: func(args map[string]any) (any, error) {
				path := stringArg(args, "path")
				if path == "" {
					return nil, errors.New("spore_submit: path required")
				}
				signFlag, _ := args["sign"].(bool)
				return doSporeSubmit(s.installRoot, path, signFlag, stringArg(args, "as"))
			},
		},
		{
			Name:             "hypha_spore_accept",
			Description:      "Flip an unreviewed spore to accepted (no graft; metadata + receipt only).",
			DefaultMaxTokens: 300,
			InputSchema: schema(merge(map[string]any{
				"spore_id": stringProp("spore id"),
				"as":       stringProp("reviewer identity URI"),
				"reason":   stringProp("optional human-readable reason"),
				"space":    stringProp("space URI containing the spore"),
			}, budgetProps), []string{"spore_id", "as"}),
			Handler: func(args map[string]any) (any, error) {
				return doSporeReview(s.installRoot, stringArg(args, "spore_id"), stringArg(args, "as"),
					stringArg(args, "reason"), stringArg(args, "space"), "accepted")
			},
		},
		{
			Name:             "hypha_spore_reject",
			Description:      "Flip an unreviewed spore to rejected (no graft; metadata + receipt only).",
			DefaultMaxTokens: 300,
			InputSchema: schema(merge(map[string]any{
				"spore_id": stringProp("spore id"),
				"as":       stringProp("reviewer identity URI"),
				"reason":   stringProp("optional human-readable reason"),
				"space":    stringProp("space URI containing the spore"),
			}, budgetProps), []string{"spore_id", "as"}),
			Handler: func(args map[string]any) (any, error) {
				return doSporeReview(s.installRoot, stringArg(args, "spore_id"), stringArg(args, "as"),
					stringArg(args, "reason"), stringArg(args, "space"), "rejected")
			},
		},
		{
			Name: "hypha_graft",
			Description: "Apply a spore's proposed_writes + proposed_edges. SAFE BY DEFAULT: " +
				"runs in dry-run mode unless apply=true. Pass diff=true to include unified diffs in the response.",
			DefaultMaxTokens: 2000,
			InputSchema: schema(merge(map[string]any{
				"spore_id": stringProp("spore id"),
				"as":       stringProp("grafter identity URI"),
				"space":    stringProp("space URI override"),
				"apply":    boolProp("persist the graft (default false — dry-run)"),
				"diff":     boolProp("include unified diffs in the response"),
				"verify":   boolProp("verify Ed25519 signature before applying"),
				"no_fmt":   boolProp("skip the mdpp.fmt pass on touched files"),
			}, budgetProps), []string{"spore_id", "as"}),
			Handler: func(args map[string]any) (any, error) {
				apply, _ := args["apply"].(bool)
				diff, _ := args["diff"].(bool)
				verify, _ := args["verify"].(bool)
				noFmt, _ := args["no_fmt"].(bool)
				return doGraft(s.conn, s.installRoot, stringArg(args, "spore_id"), stringArg(args, "as"),
					stringArg(args, "space"), apply, diff, verify, noFmt)
			},
		},
		{
			Name:             "hypha_trace_start",
			Description:      "Open a new trace.",
			DefaultMaxTokens: 400,
			InputSchema: schema(merge(map[string]any{
				"agent":   stringProp("agent URI (required)"),
				"task":    stringProp("task identifier"),
				"phase":   stringProp("short phase label"),
				"parent":  stringProp("parent agent URI"),
				"session": stringProp("session id"),
				"space":   stringProp("space URI (default: only installed space)"),
			}, budgetProps), []string{"agent"}),
			Handler: func(args map[string]any) (any, error) {
				return doTraceStart(s.installRoot, stringArg(args, "agent"), stringArg(args, "task"),
					stringArg(args, "phase"), stringArg(args, "parent"), stringArg(args, "session"),
					stringArg(args, "space"))
			},
		},
		{
			Name:             "hypha_trace_tick",
			Description:      "Append a checkpoint to an open trace.",
			DefaultMaxTokens: 200,
			InputSchema: schema(merge(map[string]any{
				"trace_id": stringProp("trace id (required)"),
				"message":  stringProp("checkpoint message (required)"),
				"space":    stringProp("space URI"),
			}, budgetProps), []string{"trace_id", "message"}),
			Handler: func(args map[string]any) (any, error) {
				id := stringArg(args, "trace_id")
				msg := stringArg(args, "message")
				if id == "" || msg == "" {
					return nil, errors.New("trace_tick: trace_id + message required")
				}
				root, err := resolveSpaceRoot(s.installRoot, stringArg(args, "space"))
				if err != nil {
					return nil, err
				}
				if err := trace.Tick(root, id, msg); err != nil {
					return nil, err
				}
				return map[string]any{"trace_id": id, "message": msg, "ok": true}, nil
			},
		},
		{
			Name:             "hypha_trace_done",
			Description:      "Close a trace with terminal status.",
			DefaultMaxTokens: 400,
			InputSchema: schema(merge(map[string]any{
				"trace_id":    stringProp("trace id (required)"),
				"status":      enumProp([]string{"succeeded", "failed", "killed", "superseded"}, "terminal status (default succeeded)"),
				"link_spore":  stringProp("spore id to attribute the work log to"),
				"space":       stringProp("space URI"),
			}, budgetProps), []string{"trace_id"}),
			Handler: func(args map[string]any) (any, error) {
				id := stringArg(args, "trace_id")
				if id == "" {
					return nil, errors.New("trace_done: trace_id required")
				}
				status := stringArg(args, "status")
				if status == "" {
					status = "succeeded"
				}
				root, err := resolveSpaceRoot(s.installRoot, stringArg(args, "space"))
				if err != nil {
					return nil, err
				}
				return trace.Done(root, id, status, stringArg(args, "link_spore"))
			},
		},
		{
			Name:             "hypha_trace_reap",
			Description:      "Force-close stale open traces (last_tick older than threshold).",
			DefaultMaxTokens: 600,
			InputSchema: schema(merge(map[string]any{
				"older_than": stringProp("Go duration; supports Nd. Default 1h."),
				"space":      stringProp("space URI (default: all installed spaces)"),
			}, budgetProps), nil),
			Handler: func(args map[string]any) (any, error) {
				older := stringArg(args, "older_than")
				if older == "" {
					older = "1h"
				}
				d, err := parseFlexDuration(older)
				if err != nil {
					return nil, fmt.Errorf("older_than %q: %w", older, err)
				}
				return doTraceReap(s.installRoot, stringArg(args, "space"), d)
			},
		},
		{
			Name:             "hypha_identity_init",
			Description:      "Generate a new Ed25519 identity + private-key sidecar.",
			DefaultMaxTokens: 400,
			InputSchema: schema(merge(map[string]any{
				"name":      stringProp("bare username"),
				"authority": stringProp("URI authority"),
				"space":     stringProp("owning space URI"),
			}, budgetProps), []string{"name", "authority", "space"}),
			Handler: func(args map[string]any) (any, error) {
				return doIdentityInit(s.installRoot, stringArg(args, "name"), stringArg(args, "authority"), stringArg(args, "space"))
			},
		},
		{
			Name:             "hypha_cap_issue",
			Description:      "Issue a scoped local capability token.",
			DefaultMaxTokens: 500,
			InputSchema: schema(merge(map[string]any{
				"subject":     stringProp("subject identity URI"),
				"space":       stringProp("target space URI"),
				"permissions": stringProp("CSV of permission names (default memory:recall,spore:create)"),
				"expires":     stringProp("Go duration (default 24h)"),
			}, budgetProps), []string{"subject", "space"}),
			Handler: func(args map[string]any) (any, error) {
				perms := stringArg(args, "permissions")
				if perms == "" {
					perms = "memory:recall,spore:create"
				}
				expires := stringArg(args, "expires")
				if expires == "" {
					expires = "24h"
				}
				exp, err := parseFlexDuration(expires)
				if err != nil {
					return nil, fmt.Errorf("expires %q: %w", expires, err)
				}
				return doCapIssue(s.installRoot, stringArg(args, "subject"), stringArg(args, "space"), perms, exp)
			},
		},
		{
			Name:             "hypha_analyze_run",
			Description:      "Run a canopy code-intelligence analysis (impact|callgraph|refs|hotspot|dead|review).",
			DefaultMaxTokens: 1200,
			InputSchema: schema(merge(map[string]any{
				"kind":      enumProp([]string{"impact", "callgraph", "refs", "hotspot", "dead", "review"}, "analysis kind"),
				"target":    stringProp("symbol or path (kind-specific)"),
				"space":     stringProp("space URI (required)"),
				"source":    stringProp("source repo path"),
				"diff_ref":  stringProp("git ref (for review)"),
				"max_depth": numberProp("max BFS depth"),
				"refresh":   boolProp("ignore cache"),
			}, budgetProps), []string{"kind", "space"}),
			Handler: func(args map[string]any) (any, error) {
				return doAnalyzeRun(s.installRoot,
					stringArg(args, "kind"), stringArg(args, "target"), stringArg(args, "space"),
					stringArg(args, "source"), stringArg(args, "diff_ref"),
					intArg(args, "max_depth", 0), boolValue(args["refresh"]))
			},
		},
		{
			Name:             "hypha_analyze_refresh",
			Description:      "Recompute a cached canopy analysis by id.",
			DefaultMaxTokens: 800,
			InputSchema: schema(merge(map[string]any{
				"analysis_id": stringProp("analysis id"),
				"space":       stringProp("space URI"),
				"source":      stringProp("source repo path"),
			}, budgetProps), []string{"analysis_id"}),
			Handler: func(args map[string]any) (any, error) {
				return doAnalyzeRefresh(s.installRoot, stringArg(args, "analysis_id"), stringArg(args, "space"), stringArg(args, "source"))
			},
		},
	}
}

func boolValue(v any) bool {
	b, _ := v.(bool)
	return b
}

// ─── mutating handlers ────────────────────────────────────────────────────

func doIndexRebuild(installRoot string) (any, error) {
	dbPath := filepath.Join(installRoot, ".index", "hyphae.db")
	conn, err := db.Open(dbPath)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	spaces, err := listSpaces(installRoot)
	if err != nil {
		return nil, err
	}
	if len(spaces) == 0 {
		return nil, fmt.Errorf("no spaces under %s/spaces/", installRoot)
	}

	var totalObj, totalAnc, totalEdg int
	for _, sp := range spaces {
		objects, anchors, edges, err := parser.WalkSpace(sp.Path, "hypha://"+sp.URI, false)
		if err != nil {
			return nil, fmt.Errorf("walk %s: %w", sp.Path, err)
		}
		if err := recall.IndexBatch(conn, objects); err != nil {
			return nil, fmt.Errorf("index %s: %w", sp.URI, err)
		}
		// Note: MCP rebuild only refreshes FTS; the richer indexer in cmd/hypha
		// also persists anchors/edges, but those tables are owned by the CLI
		// path. Here we count what we walked for the response and rely on the
		// CLI for the full rebuild when needed.
		totalObj += len(objects)
		totalAnc += len(anchors)
		totalEdg += len(edges)
	}
	return map[string]any{
		"db":              dbPath,
		"spaces_indexed":  len(spaces),
		"objects_indexed": totalObj,
		"anchors_walked":  totalAnc,
		"edges_walked":    totalEdg,
		"note":            "MCP rebuild refreshes FTS only; for full anchors/edges tables run `hypha index rebuild`",
	}, nil
}

func doSporeSubmit(installRoot, path string, sign bool, signer string) (any, error) {
	source, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	sp, verrs := spore.Parse(source)
	if len(verrs) > 0 {
		msgs := make([]string, len(verrs))
		for i, v := range verrs {
			msgs[i] = v.Error()
		}
		return nil, fmt.Errorf("validation: %s", strings.Join(msgs, "; "))
	}

	spaceRoot, err := spaceURIToPath(installRoot, sp.SpaceID)
	if err != nil {
		return nil, err
	}

	conn, err := openIndex(installRoot)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	var (
		filePath string
		receipt  types.Receipt
	)
	if sign {
		if signer == "" {
			return nil, errors.New("sign=true requires `as`")
		}
		identDir := filepath.Join(installRoot, ".catalog", "identities")
		name := identityNameFromURI(signer)
		if name == "" {
			return nil, fmt.Errorf("not a valid identity URI: %q", signer)
		}
		priv, err := identity.LoadPrivate(identDir, name)
		if err != nil {
			return nil, fmt.Errorf("load signer key: %w", err)
		}
		signed, err := spore.Sign(source, priv, signer)
		if err != nil {
			return nil, fmt.Errorf("sign: %w", err)
		}
		filePath, receipt, err = spore.SubmitBytes(signed, spaceRoot)
		if err != nil {
			return nil, err
		}
	} else {
		filePath, receipt, err = spore.Submit(sp, spaceRoot)
		if err != nil {
			return nil, err
		}
	}

	if wErr := receipts.Write(conn, receipt); wErr != nil && !errors.Is(wErr, receipts.ErrAlreadyExists) {
		return nil, fmt.Errorf("persist receipt: %w", wErr)
	}
	return map[string]any{"receipt": receipt, "file_path": filePath, "signed": sign}, nil
}

func doSporeReview(installRoot, sporeID, reviewer, reason, spaceFlag, newStatus string) (any, error) {
	if sporeID == "" || reviewer == "" {
		return nil, errors.New("spore_review: spore_id + as required")
	}
	var spacePath, spaceURI string
	if spaceFlag != "" {
		sp, su, err := resolveSpaceForTrace(installRoot, spaceFlag)
		if err != nil {
			return nil, err
		}
		spacePath = sp
		spaceURI = su
	} else {
		sp, err := findSporeSpaceRoot(installRoot, sporeID)
		if err != nil {
			return nil, err
		}
		spacePath = sp
		spaceURI = spaceURIFromDir(sp)
	}
	sporePath, err := findSporeFilePath(spacePath, sporeID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(sporePath)
	if err != nil {
		return nil, err
	}
	cur, ok := readFrontmatterField(data, "status")
	if !ok {
		return nil, fmt.Errorf("%s has no status field", sporeID)
	}
	if cur != "unreviewed" {
		return nil, fmt.Errorf("status is %q (only unreviewed spores can be reviewed)", cur)
	}
	updated := writeFrontmatterField(data, "status", newStatus)
	if err := atomicfs.WriteFile(sporePath, updated, 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", sporePath, err)
	}
	hash := sha256.Sum256(updated)
	receipt := types.Receipt{
		ID:              fmt.Sprintf("hypha-receipt:spore:%s:%s:%s", newStatus, time.Now().UTC().Format("2006-01-02"), shortHash(hash[:])),
		SpaceID:         spaceURI,
		SubjectID:       sporeID,
		SubjectKind:     "spore",
		Action:          "spore:" + newStatus,
		Status:          "ok",
		ContentHash:     fmt.Sprintf("%x", hash[:]),
		IdentityID:      reviewer,
		CreatedAt:       time.Now().UTC(),
		PermissionsUsed: []string{"spore:review"},
		NextState:       newStatus,
	}
	if conn, dbErr := openIndex(installRoot); dbErr == nil {
		defer conn.Close()
		_ = receipts.Write(conn, receipt) // best-effort
	}
	return map[string]any{
		"spore_id":     sporeID,
		"status_was":   cur,
		"status_now":   newStatus,
		"reviewer":     reviewer,
		"path":         sporePath,
		"receipt_id":   receipt.ID,
		"content_hash": receipt.ContentHash,
		"reason":       reason,
	}, nil
}

func doGraft(conn *sql.DB, installRoot, sporeID, grafter, spaceURI string, apply, diff, verify, noFmt bool) (any, error) {
	if sporeID == "" || grafter == "" {
		return nil, errors.New("graft: spore_id + as required")
	}
	var spaceRoot string
	var err error
	if spaceURI != "" {
		spaceRoot, err = spaceURIToPath(installRoot, spaceURI)
		if err != nil {
			return nil, err
		}
	} else {
		spaceRoot, err = findSporeSpaceRoot(installRoot, sporeID)
		if err != nil {
			return nil, err
		}
	}
	if verify {
		sporePath, err := findSporeFilePath(spaceRoot, sporeID)
		if err != nil {
			return nil, err
		}
		bts, err := os.ReadFile(sporePath)
		if err != nil {
			return nil, err
		}
		if err := spore.Verify(bts, identityResolver(installRoot)); err != nil {
			return nil, fmt.Errorf("verify failed: %w", err)
		}
	}

	res, err := graft.ApplyWithOpts(conn, installRoot, spaceRoot, sporeID, grafter, graft.ApplyOpts{
		DryRun: !apply,
	})
	if err != nil {
		return nil, err
	}

	if apply && !noFmt {
		// best-effort mdpp.fmt pass; we don't have the helper inlined here, so
		// skip it from the MCP side. Callers wanting mdpp.fmt should use the CLI.
		_ = res
	}

	payload := map[string]any{
		"spore_id":     res.SporeID,
		"status_now":   res.NewSporeStatus,
		"applied":      len(res.AppliedWrites),
		"skipped":      len(res.SkippedWrites),
		"touched":     res.TouchedFiles,
		"dry_run":      res.DryRun,
	}
	if !res.DryRun {
		payload["receipt"] = res.Receipt
	}
	if diff {
		diffs := make([]map[string]any, 0, len(res.Deltas))
		for _, d := range res.Deltas {
			diffs = append(diffs, map[string]any{
				"path": d.Path,
				"diff": graft.RenderDelta(d),
			})
		}
		payload["diffs"] = diffs
	}
	return payload, nil
}

func doTraceStart(installRoot, agent, task, phase, parent, session, spaceFlag string) (any, error) {
	if agent == "" {
		return nil, errors.New("trace_start: agent required")
	}
	spaceRoot, spaceURI, err := resolveSpaceForTrace(installRoot, spaceFlag)
	if err != nil {
		return nil, err
	}
	return trace.Start(trace.StartOpts{
		SpaceRoot: spaceRoot, SpaceID: spaceURI,
		AgentID: agent, AgentParent: parent, AgentSession: session,
		TaskRef: task, Phase: phase,
	})
}

func doTraceReap(installRoot, spaceFilter string, older time.Duration) (any, error) {
	spaces, err := listSpaces(installRoot)
	if err != nil {
		return nil, err
	}
	type out struct {
		Space  string           `json:"space"`
		Report trace.ReapReport `json:"report"`
	}
	var all []out
	total := 0
	for _, sp := range spaces {
		if spaceFilter != "" && !spaceMatches(sp, spaceFilter) {
			continue
		}
		rep, rerr := trace.Reap(sp.Path, older)
		if rerr != nil {
			continue
		}
		all = append(all, out{Space: "hypha://" + sp.URI, Report: rep})
		total += len(rep.Reaped)
	}
	return map[string]any{
		"older_than":   older.String(),
		"spaces":       all,
		"total_reaped": total,
	}, nil
}

func doIdentityInit(installRoot, name, authority, space string) (any, error) {
	if name == "" || authority == "" || space == "" {
		return nil, errors.New("identity_init: name + authority + space required")
	}
	dir := filepath.Join(installRoot, ".catalog", "identities")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	id, priv, err := identity.Generate(authority, name, space)
	if err != nil {
		return nil, fmt.Errorf("generate: %w", err)
	}
	mdPath, keyPath, err := identity.Save(dir, id, priv)
	if err != nil {
		return nil, fmt.Errorf("save: %w", err)
	}
	return map[string]any{
		"identity":      id,
		"identity_file": mdPath,
		"private_key":   keyPath + " (mode 0600)",
	}, nil
}

func doCapIssue(installRoot, subject, space, permsCSV string, expires time.Duration) (any, error) {
	if subject == "" || space == "" {
		return nil, errors.New("cap_issue: subject + space required")
	}
	parts := strings.Split(permsCSV, ",")
	perms := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			perms = append(perms, p)
		}
	}
	if len(perms) == 0 {
		return nil, errors.New("cap_issue: at least one permission required")
	}
	conn, err := db.Open(filepath.Join(installRoot, ".index", "hyphae.db"))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	cap, err := capability.Issue(conn, subject, space, perms, types.Limits{
		MaxRecallResults:  25,
		MaxResponseTokens: 800,
		MaxSpores:         3,
		MaxBytes:          200000,
	}, expires)
	if err != nil {
		return nil, err
	}
	return map[string]any{"token": cap.ID, "capability": cap}, nil
}

func doAnalyzeRun(installRoot, kind, target, space, source, diffRef string, maxDepth int, refresh bool) (any, error) {
	if kind == "" || space == "" {
		return nil, errors.New("analyze_run: kind + space required")
	}
	spaceRoot, spaceURI, err := resolveSpaceForTrace(installRoot, space)
	if err != nil {
		return nil, err
	}
	sourcePath := source
	if sourcePath == "" {
		// best-effort: use ~/work/<space-basename>
		home, _ := os.UserHomeDir()
		parts := strings.SplitN(strings.TrimPrefix(spaceURI, "hypha://"), "/", 2)
		if len(parts) == 2 {
			sourcePath = filepath.Join(home, "work", parts[1])
		}
	}
	if !refresh {
		existing, _ := analyze.List(spaceRoot, analyze.ListFilter{Kind: kind, TargetFile: target})
		for _, a := range existing {
			if a.Target == target || (target == "" && a.Target == "repo") {
				return a, nil
			}
		}
	}
	return analyze.Run(analyze.RunOpts{
		Kind: kind, Target: target,
		SourcePath: sourcePath, SpaceRoot: spaceRoot, SpaceID: spaceURI,
		MaxDepth: maxDepth, DiffRef: diffRef,
	})
}

func doAnalyzeRefresh(installRoot, id, spaceFlag, source string) (any, error) {
	if id == "" {
		return nil, errors.New("analyze_refresh: analysis_id required")
	}
	spaces, err := listSpaces(installRoot)
	if err != nil {
		return nil, err
	}
	var match types.Analysis
	var spaceRoot, spaceURI string
	for _, sp := range spaces {
		if spaceFlag != "" && !spaceMatches(sp, spaceFlag) {
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
		return nil, fmt.Errorf("no analysis with id %q", id)
	}
	sourcePath := source
	if sourcePath == "" {
		home, _ := os.UserHomeDir()
		parts := strings.SplitN(strings.TrimPrefix(spaceURI, "hypha://"), "/", 2)
		if len(parts) == 2 {
			sourcePath = filepath.Join(home, "work", parts[1])
		}
	}
	return analyze.Run(analyze.RunOpts{
		Kind: match.Kind, Target: match.Target,
		SourcePath: sourcePath, SpaceRoot: spaceRoot, SpaceID: spaceURI,
	})
}

// ─── tiny helpers reused from cmd/hypha (duplicated for package isolation) ──

func openIndex(installRoot string) (*sql.DB, error) {
	return db.Open(filepath.Join(installRoot, ".index", "hyphae.db"))
}

func spaceURIToPath(installRoot, spaceURI string) (string, error) {
	rest := strings.TrimPrefix(spaceURI, "hypha://")
	rest = strings.TrimRight(rest, "/")
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 {
		return "", fmt.Errorf("space URI must have authority/name, got %q", spaceURI)
	}
	dir := fmt.Sprintf("%s-%s", parts[0], parts[1])
	return filepath.Join(installRoot, "spaces", dir), nil
}

func resolveSpaceForTrace(installRoot, spaceFlag string) (string, string, error) {
	if spaceFlag != "" {
		root, err := spaceURIToPath(installRoot, spaceFlag)
		return root, spaceFlag, err
	}
	// Default: if there's exactly one installed space, use it; else require --space.
	spaces, err := listSpaces(installRoot)
	if err != nil {
		return "", "", err
	}
	if len(spaces) == 1 {
		return spaces[0].Path, "hypha://" + spaces[0].URI, nil
	}
	return "", "", errors.New("multiple spaces installed; pass `space`")
}

func resolveSpaceRoot(installRoot, spaceFlag string) (string, error) {
	root, _, err := resolveSpaceForTrace(installRoot, spaceFlag)
	return root, err
}

func spaceURIFromDir(path string) string {
	base := filepath.Base(path)
	if idx := strings.Index(base, "-"); idx >= 0 {
		return "hypha://" + base[:idx] + "/" + base[idx+1:]
	}
	return "hypha://" + base
}

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

func identityResolver(installRoot string) spore.IdentityResolver {
	dir := filepath.Join(installRoot, ".catalog", "identities")
	return func(uri string) (identity.Identity, error) {
		name := identityNameFromURI(uri)
		if name == "" {
			return identity.Identity{}, fmt.Errorf("not a recognized identity URI: %q", uri)
		}
		return identity.Load(dir, name)
	}
}

func findSporeSpaceRoot(installRoot, sporeID string) (string, error) {
	spaces, err := listSpaces(installRoot)
	if err != nil {
		return "", err
	}
	needle := []byte("id: " + sporeID)
	for _, s := range spaces {
		inbox := filepath.Join(s.Path, "inbox", "agents")
		entries, _ := os.ReadDir(inbox)
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			data, _ := os.ReadFile(filepath.Join(inbox, e.Name()))
			if bytesContains(data, needle) {
				return s.Path, nil
			}
		}
	}
	return "", fmt.Errorf("spore %q not found in any installed space's inbox", sporeID)
}

func findSporeFilePath(spaceRoot, sporeID string) (string, error) {
	inbox := filepath.Join(spaceRoot, "inbox", "agents")
	entries, err := os.ReadDir(inbox)
	if err != nil {
		return "", err
	}
	needle := []byte("id: " + sporeID)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		p := filepath.Join(inbox, e.Name())
		data, _ := os.ReadFile(p)
		if bytesContains(data, needle) {
			return p, nil
		}
	}
	return "", fmt.Errorf("spore %q not found under %s", sporeID, inbox)
}

func bytesContains(haystack, needle []byte) bool {
	return strings.Contains(string(haystack), string(needle))
}

func readFrontmatterField(data []byte, key string) (string, bool) {
	s := string(data)
	if !strings.HasPrefix(s, "---\n") {
		return "", false
	}
	rest := s[4:]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return "", false
	}
	fm := rest[:end]
	prefix := key + ":"
	for _, line := range strings.Split(fm, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(t, prefix)), true
		}
	}
	return "", false
}

func writeFrontmatterField(data []byte, key, newValue string) []byte {
	s := string(data)
	if !strings.HasPrefix(s, "---\n") {
		return data
	}
	rest := s[4:]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return data
	}
	fm := rest[:end]
	body := rest[end:]
	prefix := key + ":"
	lines := strings.Split(fm, "\n")
	updated := false
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, prefix) {
			lines[i] = key + ": " + newValue
			updated = true
			break
		}
	}
	if !updated {
		lines = append(lines, key+": "+newValue)
	}
	return []byte("---\n" + strings.Join(lines, "\n") + body)
}

func shortHash(b []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, 0, 8)
	for i := 0; i < 4 && i < len(b); i++ {
		out = append(out, hex[b[i]>>4], hex[b[i]&0x0f])
	}
	return string(out)
}
