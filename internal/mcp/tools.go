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
	"m31labs.dev/hyphae/internal/pulse"
	"m31labs.dev/hyphae/internal/recall"
	"m31labs.dev/hyphae/internal/receipts"
	"m31labs.dev/hyphae/internal/trace"
	"m31labs.dev/hyphae/internal/types"
)

// toolSpec defines one MCP tool: its surface name, its description, the
// JSON Schema for its arguments, and the handler that turns those
// arguments into a serializable result.
type toolSpec struct {
	Name        string
	Description string
	InputSchema map[string]any
	Handler     func(args map[string]any) (any, error)
}

func buildTools(s *Server) []toolSpec {
	return []toolSpec{
		{
			Name: "hypha_recall",
			Description: "Full-text search across all installed Hyphae spaces. " +
				"Returns ranked hits with short body snippets and per-snippet citations (anchor URI + line range). " +
				"Token-budgeted; designed for direct LLM consumption.",
			InputSchema: schema(map[string]any{
				"query":      stringProp("query terms"),
				"limit":      numberProp("max hits to consider (default 12)"),
				"max_tokens": numberProp("token budget for the response (default 800)"),
				"shape":      enumProp([]string{"headline", "summary+anchors", "count_only"}, "response shape"),
			}, []string{"query"}),
			Handler: func(args map[string]any) (any, error) {
				query, _ := args["query"].(string)
				if strings.TrimSpace(query) == "" {
					return nil, errors.New("recall: query is required")
				}
				limit := intArg(args, "limit", 12)
				maxTokens := intArg(args, "max_tokens", 800)
				shape, _ := args["shape"].(string)
				if shape == "" {
					shape = "summary+anchors"
				}
				return recall.Recall(s.conn, query, limit, types.Budget{
					MaxResponseTokens: maxTokens,
					Shape:             types.ResponseShape(shape),
				})
			},
		},
		{
			Name: "hypha_pulse",
			Description: "Time-windowed signal aggregation: top initiatives, hot zones, recent pressure, " +
				"edge-kind distribution, activity counts.",
			InputSchema: schema(map[string]any{
				"space":  stringProp("filter to a single space URI (default: all spaces)"),
				"window": stringProp("Go duration; supports Nd (e.g. 7d, 30d). Default 30d."),
			}, nil),
			Handler: func(args map[string]any) (any, error) {
				space, _ := args["space"].(string)
				windowStr, _ := args["window"].(string)
				if windowStr == "" {
					windowStr = "30d"
				}
				window, err := parseFlexDuration(windowStr)
				if err != nil {
					return nil, fmt.Errorf("window %q: %w", windowStr, err)
				}
				return pulse.Compute(s.conn, space, window)
			},
		},
		{
			Name: "hypha_assess_task",
			Description: "Alignment scoring for a proposed task against active initiatives in a space. " +
				"Returns alignment category, matched initiatives, recommendation.",
			InputSchema: schema(map[string]any{
				"task":  stringProp("natural-language description of the task"),
				"space": stringProp("filter scoring to one space URI"),
			}, []string{"task"}),
			Handler: func(args map[string]any) (any, error) {
				task, _ := args["task"].(string)
				if strings.TrimSpace(task) == "" {
					return nil, errors.New("assess_task: task is required")
				}
				space, _ := args["space"].(string)
				return assess.Change(s.conn, assess.ChangeRequest{
					Task:   task,
					Space:  space,
					Window: 30 * 24 * time.Hour,
					Budget: types.Budget{MaxResponseTokens: 1200, Shape: types.ShapeCitedSpans},
				})
			},
		},
		{
			Name: "hypha_assess_change",
			Description: "Alignment scoring for a change (task + changed files + diff summary). " +
				"Same scorer as assess_task with richer input.",
			InputSchema: schema(map[string]any{
				"task":         stringProp("natural-language description of the change"),
				"files":        arrayOfStrings("changed file paths"),
				"diff_summary": stringProp("one-line diff summary"),
				"space":        stringProp("filter scoring to one space URI"),
			}, []string{"task"}),
			Handler: func(args map[string]any) (any, error) {
				task, _ := args["task"].(string)
				if strings.TrimSpace(task) == "" {
					return nil, errors.New("assess_change: task is required")
				}
				files := stringSliceArg(args, "files")
				diff, _ := args["diff_summary"].(string)
				space, _ := args["space"].(string)
				return assess.Change(s.conn, assess.ChangeRequest{
					Task:         task,
					ChangedFiles: files,
					DiffSummary:  diff,
					Space:        space,
					Window:       30 * 24 * time.Hour,
					Budget:       types.Budget{MaxResponseTokens: 1200, Shape: types.ShapeCitedSpans},
				})
			},
		},
		{
			Name:        "hypha_spaces_list",
			Description: "List installed spaces under $HYPHAE_HOME/spaces.",
			InputSchema: schema(map[string]any{}, nil),
			Handler: func(_ map[string]any) (any, error) {
				return listSpaces(s.installRoot)
			},
		},
		{
			Name: "hypha_graph_backlinks",
			Description: "Edges pointing AT this object. Walks the typed graph table.",
			InputSchema: schema(map[string]any{
				"object_id": stringProp("hypha:// URI or bare object id"),
				"kind":      stringProp("comma-separated edge kinds to filter"),
				"limit":     numberProp("max results (default 50)"),
			}, []string{"object_id"}),
			Handler: func(args map[string]any) (any, error) {
				id, _ := args["object_id"].(string)
				if id == "" {
					return nil, errors.New("graph_backlinks: object_id required")
				}
				return graph.Backlinks(s.conn, id, parseEdgeKinds(stringArg(args, "kind")), intArg(args, "limit", 50))
			},
		},
		{
			Name: "hypha_graph_related",
			Description: "Edges incident on this object (in or out).",
			InputSchema: schema(map[string]any{
				"object_id": stringProp("hypha:// URI or bare object id"),
				"kind":      stringProp("comma-separated edge kinds to filter"),
				"limit":     numberProp("max results (default 50)"),
			}, []string{"object_id"}),
			Handler: func(args map[string]any) (any, error) {
				id, _ := args["object_id"].(string)
				if id == "" {
					return nil, errors.New("graph_related: object_id required")
				}
				return graph.Related(s.conn, id, parseEdgeKinds(stringArg(args, "kind")), intArg(args, "limit", 50))
			},
		},
		{
			Name: "hypha_graph_trace",
			Description: "BFS the derivation/citation chain from an object.",
			InputSchema: schema(map[string]any{
				"object_id": stringProp("hypha:// URI or bare object id"),
				"kind":      stringProp("comma-separated edge kinds to follow (default derived_from,cites,source_ref)"),
				"max_depth": numberProp("max BFS depth (default 4)"),
			}, []string{"object_id"}),
			Handler: func(args map[string]any) (any, error) {
				id, _ := args["object_id"].(string)
				if id == "" {
					return nil, errors.New("graph_trace: object_id required")
				}
				kind := stringArg(args, "kind")
				if kind == "" {
					kind = "derived_from,cites,source_ref"
				}
				return graph.Trace(s.conn, id, parseEdgeKinds(kind), intArg(args, "max_depth", 4))
			},
		},
		{
			Name: "hypha_spore_list",
			Description: "List inbox spores across installed spaces.",
			InputSchema: schema(map[string]any{
				"space":  stringProp("filter by space URI"),
				"status": stringProp("filter by status (unreviewed, accepted, rejected, …)"),
				"limit":  numberProp("max results (default 50)"),
			}, nil),
			Handler: func(args map[string]any) (any, error) {
				return sporeList(s.installRoot, stringArg(args, "space"), stringArg(args, "status"), intArg(args, "limit", 50))
			},
		},
		{
			Name: "hypha_trace_list",
			Description: "List in-flight or recently-closed traces.",
			InputSchema: schema(map[string]any{
				"space":  stringProp("filter by space URI"),
				"agent":  stringProp("exact agent URI match"),
				"active": boolProp("only currently-open traces"),
			}, nil),
			Handler: func(args map[string]any) (any, error) {
				space := stringArg(args, "space")
				agent := stringArg(args, "agent")
				active, _ := args["active"].(bool)
				return traceList(s.installRoot, space, agent, active)
			},
		},
		{
			Name: "hypha_receipts_list",
			Description: "Query the local audit log.",
			InputSchema: schema(map[string]any{
				"space":   stringProp("filter by space URI"),
				"subject": stringProp("filter by subject id"),
				"action":  stringProp("filter by action (e.g. spore:create, graft, cap:issue)"),
				"since":   stringProp("Go duration; receipts within the last N units"),
				"limit":   numberProp("max results (default 50)"),
			}, nil),
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
			Name: "hypha_analyze_list",
			Description: "List cached canopy code-intelligence analyses across spaces.",
			InputSchema: schema(map[string]any{
				"kind":        stringProp("filter by kind (impact|callgraph|refs|hotspot|dead|review)"),
				"target_file": stringProp("filter analyses whose target_files include this path"),
				"space":       stringProp("filter by space URI"),
			}, nil),
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
