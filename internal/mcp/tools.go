package mcp

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"m31labs.dev/hyphae/internal/analyze"
	"m31labs.dev/hyphae/internal/assess"
	"m31labs.dev/hyphae/internal/graph"
	"m31labs.dev/hyphae/internal/identity"
	"m31labs.dev/hyphae/internal/pulse"
	"m31labs.dev/hyphae/internal/recall"
	"m31labs.dev/hyphae/internal/receipts"
	"m31labs.dev/hyphae/internal/trace"
	"m31labs.dev/hyphae/internal/types"
)

// toolSpec defines one MCP tool: its surface name, its description, the
// JSON Schema for its arguments, and the handler that turns those
// arguments into a serializable result.
//
// DefaultMaxTokens is the soft response-size cap when the caller doesn't
// pass `max_tokens`. List-shaped responses honor it by dropping trailing
// rows and emitting a TRUNCATED warning.
type toolSpec struct {
	Name             string
	Description      string
	InputSchema      map[string]any
	DefaultMaxTokens int
	Handler          func(args map[string]any) (any, error)
}

func buildTools(s *Server) []toolSpec {
	var tools []toolSpec
	tools = append(tools, readTools(s)...)
	tools = append(tools, writeTools(s)...)
	return tools
}

func readTools(s *Server) []toolSpec {
	// budgetProps is the common token-discipline arg set: format selects
	// the wire shape; max_tokens caps response size with truncation; fields
	// projects each list row down to a whitelist.
	budgetProps := map[string]any{
		"format":     enumProp([]string{"jsonline", "json", "compact"}, "wire shape (default jsonline)"),
		"max_tokens": numberProp("soft response budget; list rows are trimmed when over"),
		"fields":     arrayOfStrings("whitelist of top-level fields per list row"),
	}

	return []toolSpec{
		{
			Name:             "hypha_recall",
			Description:      "FTS5 search across spaces; ranked hits + body snippets + per-snippet citations.",
			DefaultMaxTokens: 800,
			InputSchema: schema(merge(map[string]any{
				"query":      stringProp("query terms"),
				"limit":      numberProp("max hits to consider (default 12)"),
				"shape":      enumProp([]string{"headline", "summary+anchors", "count_only"}, "recall response shape"),
			}, budgetProps), []string{"query"}),
			Handler: func(args map[string]any) (any, error) {
				q, _ := args["query"].(string)
				if strings.TrimSpace(q) == "" {
					return nil, errors.New("recall: query required")
				}
				shape, _ := args["shape"].(string)
				if shape == "" {
					shape = "summary+anchors"
				}
				return recall.Recall(s.conn, q, intArg(args, "limit", 12), types.Budget{
					MaxResponseTokens: intArg(args, "max_tokens", 800),
					Shape:             types.ResponseShape(shape),
				})
			},
		},
		{
			Name:             "hypha_show",
			Description:      "Fetch one object's metadata or body by id/URI; slice picks how much to return.",
			DefaultMaxTokens: 1200,
			InputSchema: schema(merge(map[string]any{
				"id":    stringProp("hypha:// URI or bare object id"),
				"slice": enumProp([]string{"metadata", "frontmatter", "body", "full", "path"}, "what to return (default metadata)"),
			}, budgetProps), []string{"id"}),
			Handler: func(args map[string]any) (any, error) {
				id, _ := args["id"].(string)
				if id == "" {
					return nil, errors.New("show: id required")
				}
				slice, _ := args["slice"].(string)
				if slice == "" {
					slice = "metadata"
				}
				return showObject(s.conn, s.installRoot, id, slice)
			},
		},
		{
			Name:             "hypha_pulse",
			Description:      "Time-windowed signal: top initiatives, hot zones, recent pressure, activity.",
			DefaultMaxTokens: 800,
			InputSchema: schema(merge(map[string]any{
				"space":  stringProp("filter to a single space URI"),
				"window": stringProp("Go duration; supports Nd (e.g. 7d, 30d). Default 30d."),
			}, budgetProps), nil),
			Handler: func(args map[string]any) (any, error) {
				windowStr, _ := args["window"].(string)
				if windowStr == "" {
					windowStr = "30d"
				}
				w, err := parseFlexDuration(windowStr)
				if err != nil {
					return nil, fmt.Errorf("window %q: %w", windowStr, err)
				}
				return pulse.Compute(s.conn, stringArg(args, "space"), w)
			},
		},
		{
			Name:             "hypha_assess_task",
			Description:      "Alignment scoring for a task vs active initiatives.",
			DefaultMaxTokens: 600,
			InputSchema: schema(merge(map[string]any{
				"task":  stringProp("task description"),
				"space": stringProp("filter scoring to one space URI"),
			}, budgetProps), []string{"task"}),
			Handler: func(args map[string]any) (any, error) {
				task, _ := args["task"].(string)
				if strings.TrimSpace(task) == "" {
					return nil, errors.New("assess_task: task required")
				}
				return assess.Change(s.conn, assess.ChangeRequest{
					Task:   task,
					Space:  stringArg(args, "space"),
					Window: 30 * 24 * time.Hour,
					Budget: types.Budget{MaxResponseTokens: intArg(args, "max_tokens", 600), Shape: types.ShapeCitedSpans},
				})
			},
		},
		{
			Name:             "hypha_assess_change",
			Description:      "Alignment scoring with task + changed files + diff summary.",
			DefaultMaxTokens: 800,
			InputSchema: schema(merge(map[string]any{
				"task":         stringProp("change description"),
				"files":        arrayOfStrings("changed file paths"),
				"diff_summary": stringProp("one-line diff summary"),
				"space":        stringProp("filter scoring to one space URI"),
			}, budgetProps), []string{"task"}),
			Handler: func(args map[string]any) (any, error) {
				task, _ := args["task"].(string)
				if strings.TrimSpace(task) == "" {
					return nil, errors.New("assess_change: task required")
				}
				return assess.Change(s.conn, assess.ChangeRequest{
					Task:         task,
					ChangedFiles: stringSliceArg(args, "files"),
					DiffSummary:  stringArg(args, "diff_summary"),
					Space:        stringArg(args, "space"),
					Window:       30 * 24 * time.Hour,
					Budget:       types.Budget{MaxResponseTokens: intArg(args, "max_tokens", 800), Shape: types.ShapeCitedSpans},
				})
			},
		},
		{
			Name:             "hypha_assess_pr",
			Description:      "Alignment scoring for a PR: derives files + diff-summary from git base...HEAD.",
			DefaultMaxTokens: 800,
			InputSchema: schema(merge(map[string]any{
				"task":   stringProp("PR description"),
				"base":   stringProp("git base ref (default origin/main)"),
				"source": stringProp("source repo path (default cwd)"),
				"space":  stringProp("filter scoring to one space URI"),
			}, budgetProps), []string{"task"}),
			Handler: func(args map[string]any) (any, error) {
				task, _ := args["task"].(string)
				if strings.TrimSpace(task) == "" {
					return nil, errors.New("assess_pr: task required")
				}
				base := stringArg(args, "base")
				if base == "" {
					base = "origin/main"
				}
				return assessPR(s.conn, s.installRoot, task, base, stringArg(args, "source"), stringArg(args, "space"), intArg(args, "max_tokens", 800))
			},
		},
		{
			Name:             "hypha_spaces_list",
			Description:      "List installed spaces under $HYPHAE_HOME/spaces.",
			DefaultMaxTokens: 600,
			InputSchema:      schema(budgetProps, nil),
			Handler: func(_ map[string]any) (any, error) {
				return listSpaces(s.installRoot)
			},
		},
		{
			Name:             "hypha_graph_backlinks",
			Description:      "Edges pointing AT object_id.",
			DefaultMaxTokens: 800,
			InputSchema: schema(merge(map[string]any{
				"object_id": stringProp("hypha:// URI or bare id"),
				"kind":      stringProp("CSV of edge kinds"),
				"limit":     numberProp("max results (default 50)"),
			}, budgetProps), []string{"object_id"}),
			Handler: func(args map[string]any) (any, error) {
				id, _ := args["object_id"].(string)
				if id == "" {
					return nil, errors.New("graph_backlinks: object_id required")
				}
				return graph.Backlinks(s.conn, id, parseEdgeKinds(stringArg(args, "kind")), intArg(args, "limit", 50))
			},
		},
		{
			Name:             "hypha_graph_related",
			Description:      "Edges incident on object_id (in or out).",
			DefaultMaxTokens: 800,
			InputSchema: schema(merge(map[string]any{
				"object_id": stringProp("hypha:// URI or bare id"),
				"kind":      stringProp("CSV of edge kinds"),
				"limit":     numberProp("max results (default 50)"),
			}, budgetProps), []string{"object_id"}),
			Handler: func(args map[string]any) (any, error) {
				id, _ := args["object_id"].(string)
				if id == "" {
					return nil, errors.New("graph_related: object_id required")
				}
				return graph.Related(s.conn, id, parseEdgeKinds(stringArg(args, "kind")), intArg(args, "limit", 50))
			},
		},
		{
			Name:             "hypha_graph_trace",
			Description:      "BFS the derivation/citation chain from object_id.",
			DefaultMaxTokens: 800,
			InputSchema: schema(merge(map[string]any{
				"object_id": stringProp("hypha:// URI or bare id"),
				"kind":      stringProp("CSV of edge kinds (default derived_from,cites,source_ref)"),
				"max_depth": numberProp("max BFS depth (default 4)"),
			}, budgetProps), []string{"object_id"}),
			Handler: func(args map[string]any) (any, error) {
				id, _ := args["object_id"].(string)
				if id == "" {
					return nil, errors.New("graph_trace: object_id required")
				}
				k := stringArg(args, "kind")
				if k == "" {
					k = "derived_from,cites,source_ref"
				}
				return graph.Trace(s.conn, id, parseEdgeKinds(k), intArg(args, "max_depth", 4))
			},
		},
		{
			Name:             "hypha_identity_list",
			Description:      "List local identities under $HYPHAE_HOME/.catalog/identities.",
			DefaultMaxTokens: 400,
			InputSchema:      schema(budgetProps, nil),
			Handler: func(_ map[string]any) (any, error) {
				return identity.List(filepath.Join(s.installRoot, ".catalog", "identities"))
			},
		},
		{
			Name:             "hypha_spore_list",
			Description:      "List inbox spores across spaces.",
			DefaultMaxTokens: 800,
			InputSchema: schema(merge(map[string]any{
				"space":  stringProp("filter by space URI"),
				"status": stringProp("filter by status"),
				"limit":  numberProp("max results (default 50)"),
			}, budgetProps), nil),
			Handler: func(args map[string]any) (any, error) {
				return sporeList(s.installRoot, stringArg(args, "space"), stringArg(args, "status"), intArg(args, "limit", 50))
			},
		},
		{
			Name:             "hypha_trace_list",
			Description:      "List traces; active=true narrows to open.",
			DefaultMaxTokens: 800,
			InputSchema: schema(merge(map[string]any{
				"space":  stringProp("filter by space URI"),
				"agent":  stringProp("exact agent URI match"),
				"active": boolProp("only open traces"),
			}, budgetProps), nil),
			Handler: func(args map[string]any) (any, error) {
				active, _ := args["active"].(bool)
				return traceList(s.installRoot, stringArg(args, "space"), stringArg(args, "agent"), active)
			},
		},
		{
			Name:             "hypha_trace_history",
			Description:      "FTS5 search of closed traces (methodology recall).",
			DefaultMaxTokens: 800,
			InputSchema: schema(merge(map[string]any{
				"similar":      stringProp("free-text query against trace bodies"),
				"task":         stringProp("filter to traces with this task_ref"),
				"agent":        stringProp("filter to this agent URI"),
				"include_open": boolProp("include currently-open traces"),
				"limit":        numberProp("max results (default 10)"),
				"space":        stringProp("filter by space URI"),
			}, budgetProps), nil),
			Handler: func(args map[string]any) (any, error) {
				includeOpen, _ := args["include_open"].(bool)
				return traceHistory(s.conn, s.installRoot, traceHistoryArgs{
					Similar:     stringArg(args, "similar"),
					Task:        stringArg(args, "task"),
					Agent:       stringArg(args, "agent"),
					Space:       stringArg(args, "space"),
					IncludeOpen: includeOpen,
					Limit:       intArg(args, "limit", 10),
				})
			},
		},
		{
			Name:             "hypha_receipts_list",
			Description:      "Local audit log query.",
			DefaultMaxTokens: 800,
			InputSchema: schema(merge(map[string]any{
				"space":   stringProp("filter by space URI"),
				"subject": stringProp("filter by subject id"),
				"action":  stringProp("filter by action"),
				"since":   stringProp("Go duration; receipts within last N units"),
				"limit":   numberProp("max results (default 50)"),
			}, budgetProps), nil),
			Handler: func(args map[string]any) (any, error) {
				var sinceT time.Time
				if since := stringArg(args, "since"); since != "" {
					d, err := parseFlexDuration(since)
					if err != nil {
						return nil, fmt.Errorf("since %q: %w", since, err)
					}
					sinceT = time.Now().UTC().Add(-d)
				}
				return receipts.List(s.conn, receipts.ListFilter{
					SpaceID:   stringArg(args, "space"),
					SubjectID: stringArg(args, "subject"),
					Action:    stringArg(args, "action"),
					Since:     sinceT,
					Limit:     intArg(args, "limit", 50),
				})
			},
		},
		{
			Name:             "hypha_analyze_list",
			Description:      "List cached canopy analyses across spaces.",
			DefaultMaxTokens: 800,
			InputSchema: schema(merge(map[string]any{
				"kind":        stringProp("filter by kind (impact|callgraph|refs|hotspot|dead|review)"),
				"target_file": stringProp("filter analyses whose target_files include this path"),
				"space":       stringProp("filter by space URI"),
			}, budgetProps), nil),
			Handler: func(args map[string]any) (any, error) {
				return analyzeList(s.installRoot, stringArg(args, "kind"), stringArg(args, "target_file"), stringArg(args, "space"))
			},
		},
	}
}

// ─── small helpers for argument unpacking and schema building ────────────────

func schema(props map[string]any, required []string) map[string]any {
	out := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func merge(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func stringProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func numberProp(desc string) map[string]any {
	return map[string]any{"type": "number", "description": desc}
}

func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

func arrayOfStrings(desc string) map[string]any {
	return map[string]any{
		"type":        "array",
		"description": desc,
		"items":       map[string]any{"type": "string"},
	}
}

func enumProp(values []string, desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc, "enum": values}
}

func intArg(args map[string]any, key string, def int) int {
	v, ok := args[key]
	if !ok || v == nil {
		return def
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return def
}

func stringArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}

func stringSliceArg(args map[string]any, key string) []string {
	raw, ok := args[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// parseFlexDuration accepts Go durations plus "Nd" → N*24h.
func parseFlexDuration(s string) (time.Duration, error) {
	if len(s) > 1 && s[len(s)-1] == 'd' {
		if d, err := time.ParseDuration(s[:len(s)-1] + "h"); err == nil {
			return d * 24, nil
		}
	}
	return time.ParseDuration(s)
}

func parseEdgeKinds(s string) []types.EdgeKind {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]types.EdgeKind, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, types.EdgeKind(p))
		}
	}
	return out
}

// ─── space/spore/trace/analyze list helpers (duplicate small bits of cmd/hypha to keep the MCP package self-contained) ───

type spaceEntry struct {
	URI  string `json:"uri"`
	Path string `json:"path"`
	Name string `json:"name"`
}

func listSpaces(installRoot string) ([]spaceEntry, error) {
	dir := filepath.Join(installRoot, "spaces")
	entries, err := osReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []spaceEntry
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		uri := strings.Replace(name, "-", "/", 1) // m31labs-hyphae → m31labs/hyphae
		out = append(out, spaceEntry{
			URI:  uri,
			Path: filepath.Join(dir, name),
			Name: name,
		})
	}
	return out, nil
}

type sporeRow struct {
	ID          string    `json:"id"`
	Space       string    `json:"space"`
	Status      string    `json:"status"`
	Path        string    `json:"path"`
	SubmittedAt time.Time `json:"submitted_at"`
}

type traceRow struct {
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

type analyzeRow struct {
	ID            string    `json:"id"`
	Kind          string    `json:"kind"`
	Target        string    `json:"target"`
	Commit        string    `json:"commit"`
	ComputedAt    time.Time `json:"computed_at"`
	Stale         bool      `json:"stale"`
	TotalAffected int       `json:"total_affected,omitempty"`
	Path          string    `json:"path"`
}

func traceList(installRoot, spaceFilter, agent string, active bool) ([]traceRow, error) {
	spaces, err := listSpaces(installRoot)
	if err != nil {
		return nil, err
	}
	var out []traceRow
	for _, sp := range spaces {
		if spaceFilter != "" && !spaceMatches(sp, spaceFilter) {
			continue
		}
		traces, terr := trace.List(sp.Path, trace.ListFilter{ActiveOnly: active, Agent: agent})
		if terr != nil {
			continue
		}
		for _, t := range traces {
			out = append(out, traceRow{
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
	return out, nil
}

func analyzeList(installRoot, kindFilter, targetFile, spaceFilter string) ([]analyzeRow, error) {
	spaces, err := listSpaces(installRoot)
	if err != nil {
		return nil, err
	}
	var out []analyzeRow
	for _, sp := range spaces {
		if spaceFilter != "" && !spaceMatches(sp, spaceFilter) {
			continue
		}
		list, lerr := analyze.List(sp.Path, analyze.ListFilter{Kind: kindFilter, TargetFile: targetFile})
		if lerr != nil {
			continue
		}
		for _, a := range list {
			out = append(out, analyzeRow{
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
	return out, nil
}

func spaceMatches(sp spaceEntry, filter string) bool {
	f := strings.TrimPrefix(filter, "hypha://")
	f = strings.TrimRight(f, "/")
	return sp.URI == f || sp.URI == filter
}
