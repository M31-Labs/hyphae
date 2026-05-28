package mcp

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"m31labs.dev/hyphae/internal/assess"
	"m31labs.dev/hyphae/internal/trace"
	"m31labs.dev/hyphae/internal/types"
)

// showObject mirrors `hypha show` with the same slice semantics. Returns
// a small map suitable for envelope wrapping. Slices are: metadata
// (default; frontmatter-derived fields), frontmatter (raw YAML block),
// body (markdown after frontmatter), full (whole file), path (resolved
// file path only).
func showObject(conn *sql.DB, installRoot, idArg, slice string) (any, error) {
	id := normalizeShowID(idArg)
	var fileID, spaceID, typeStr, status, title, tagsJSON, summary, updatedAt string
	err := conn.QueryRow(
		`SELECT file_id, space_id, type, COALESCE(status, ''), COALESCE(title, ''),
		        COALESCE(tags_json, '[]'), COALESCE(summary, ''), updated_at
		 FROM objects WHERE id = ?`,
		id,
	).Scan(&fileID, &spaceID, &typeStr, &status, &title, &tagsJSON, &summary, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("show: no object with id %q (run hypha index rebuild?)", id)
	}
	if err != nil {
		return nil, fmt.Errorf("show: query: %w", err)
	}

	absPath, err := resolveObjectPath(installRoot, spaceID, fileID)
	if err != nil {
		return nil, fmt.Errorf("show: resolve path: %w", err)
	}

	switch slice {
	case "path":
		return map[string]any{"id": id, "path": absPath}, nil
	case "metadata", "":
		var tags []string
		_ = json.Unmarshal([]byte(tagsJSON), &tags)
		return map[string]any{
			"id":         id,
			"type":       typeStr,
			"space_id":   spaceID,
			"file_path":  absPath,
			"status":     status,
			"title":      title,
			"tags":       tags,
			"summary":    summary,
			"updated_at": updatedAt,
		}, nil
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("show: read %s: %w", absPath, err)
	}
	front, body := splitFrontmatter(content)
	switch slice {
	case "frontmatter":
		return map[string]any{"id": id, "path": absPath, "frontmatter": string(front)}, nil
	case "body":
		return map[string]any{"id": id, "path": absPath, "body": string(body)}, nil
	case "full":
		return map[string]any{"id": id, "path": absPath, "content": string(content)}, nil
	}
	return nil, fmt.Errorf("show: unknown slice %q", slice)
}

// normalizeShowID accepts either a bare object id ("concept.spore"), a
// "hypha://<space>/object/<id>" recall URI, or a legacy
// "hypha://<space>/<id>" form, and returns the bare id suitable for the
// objects lookup.
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
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == "object" && i+1 < len(parts) {
			return strings.Join(parts[i+1:], "/")
		}
	}
	return parts[len(parts)-1]
}

// resolveObjectPath joins installRoot/spaces/<authority>-<name>/<file_id>.
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

// splitFrontmatter separates the leading YAML frontmatter (delimited by
// ---) from the markdown body. Mirrors cmd/hypha's helper.
func splitFrontmatter(content []byte) (frontmatter, body []byte) {
	if !bytes.HasPrefix(content, []byte("---\n")) && !bytes.HasPrefix(content, []byte("---\r\n")) {
		return nil, content
	}
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

// assessPR derives changed files + diff summary from git, then runs
// assess.Change. Mirrors `hypha assess pr`.
func assessPR(conn *sql.DB, installRoot, task, base, source, space string, maxTokens int) (any, error) {
	srcPath := source
	if srcPath == "" {
		cwd, _ := os.Getwd()
		srcPath = cwd
	}
	files, err := gitChangedFiles(srcPath, base)
	if err != nil {
		return nil, fmt.Errorf("assess_pr: derive files from %s: %w", base, err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("assess_pr: no changed files vs %s", base)
	}
	diffSummary, _ := gitDiffStat(srcPath, base)
	res, err := assess.Change(conn, assess.ChangeRequest{
		Task:         task,
		ChangedFiles: files,
		DiffSummary:  diffSummary,
		Space:        space,
		Window:       30 * 24 * time.Hour,
		Budget:       types.Budget{MaxResponseTokens: maxTokens, Shape: types.ShapeCitedSpans},
		SourcePath:   srcPath,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"task":          task,
		"base_ref":      base,
		"changed_files": files,
		"diff_summary":  diffSummary,
		"result":        res,
	}, nil
}

func gitChangedFiles(workdir, base string) ([]string, error) {
	cmd := exec.Command("git", "diff", "--name-only", base+"...HEAD")
	cmd.Dir = workdir
	out, err := cmd.Output()
	if err != nil {
		cmd2 := exec.Command("git", "diff", "--name-only", base)
		cmd2.Dir = workdir
		out, err = cmd2.Output()
		if err != nil {
			return nil, err
		}
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

func gitDiffStat(workdir, base string) (string, error) {
	cmd := exec.Command("git", "diff", "--shortstat", base+"...HEAD")
	cmd.Dir = workdir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ─── trace_history ────────────────────────────────────────────────────────

type traceHistoryArgs struct {
	Similar     string
	Task        string
	Agent       string
	Space       string
	IncludeOpen bool
	Limit       int
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
}

// traceHistory queries the FTS5 index for closed traces matching --similar
// (free-text), optionally filtered by task or agent.
func traceHistory(conn *sql.DB, installRoot string, args traceHistoryArgs) ([]traceHistoryRow, error) {
	if args.Similar == "" && args.Task == "" && args.Agent == "" {
		return nil, errors.New("trace_history: need at least one of similar/task/agent")
	}
	spaces, err := listSpaces(installRoot)
	if err != nil {
		return nil, err
	}

	var fts5 map[string]float64
	if args.Similar != "" {
		sanitized := sanitizeFTSQuery(args.Similar)
		if sanitized != "" {
			rows, qerr := conn.Query(`
SELECT f.id, bm25(objects_fts, 3.0, 2.0, 2.0, 1.0) AS rank
FROM objects_fts f
WHERE objects_fts MATCH ?
  AND f.type = 'trace'
ORDER BY rank
LIMIT ?`, sanitized, args.Limit*5)
			if qerr != nil {
				return nil, fmt.Errorf("trace_history fts: %w", qerr)
			}
			defer rows.Close()
			fts5 = make(map[string]float64)
			for rows.Next() {
				var id string
				var rank float64
				if err := rows.Scan(&id, &rank); err != nil {
					return nil, err
				}
				fts5[id] = rank
			}
		}
	}

	var out []traceHistoryRow
	for _, sp := range spaces {
		if args.Space != "" && !spaceMatches(sp, args.Space) {
			continue
		}
		traces, lerr := trace.List(sp.Path, trace.ListFilter{Agent: args.Agent})
		if lerr != nil {
			continue
		}
		for _, t := range traces {
			if !args.IncludeOpen && t.Status == types.TraceStatusOpen {
				continue
			}
			if args.Task != "" && t.TaskRef != args.Task {
				continue
			}
			if args.Similar != "" {
				if _, ok := fts5[t.ID]; !ok {
					continue
				}
			}
			out = append(out, traceHistoryRow{
				ID: t.ID, Space: t.SpaceID, Agent: t.AgentID, Status: t.Status,
				TaskRef: t.TaskRef, Phase: t.Phase, Ticks: len(t.Ticks),
				LastTick: t.LastTick, Path: t.FilePath,
			})
		}
	}

	if args.Similar != "" && fts5 != nil {
		sort.SliceStable(out, func(i, j int) bool { return fts5[out[i].ID] < fts5[out[j].ID] })
	} else {
		sort.SliceStable(out, func(i, j int) bool { return out[i].LastTick.After(out[j].LastTick) })
	}
	if args.Limit > 0 && len(out) > args.Limit {
		out = out[:args.Limit]
	}
	return out, nil
}

// sanitizeFTSQuery mirrors recall's strip-to-alphanum approach so FTS5
// won't choke on punctuation.
func sanitizeFTSQuery(q string) string {
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
