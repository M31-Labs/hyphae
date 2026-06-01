// Package doctor inspects a Hyphae install without mutating it.
package doctor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"m31labs.dev/hyphae/internal/db"
	"m31labs.dev/hyphae/internal/parser"
)

const (
	StatusOK      = "ok"
	StatusWarning = "warning"
	StatusError   = "error"
)

// Options controls a doctor run.
type Options struct {
	Root         string
	CheckCanopy  bool
	IncludeInbox bool
	ToolTimeout  time.Duration
}

// Report is the machine-readable diagnostic payload for `hypha doctor`.
type Report struct {
	Status          string        `json:"status"`
	Root            string        `json:"root"`
	Checks          []Check       `json:"checks"`
	Spaces          []SpaceReport `json:"spaces"`
	Index           IndexReport   `json:"index"`
	Tools           []ToolReport  `json:"tools,omitempty"`
	Recommendations []string      `json:"recommendations,omitempty"`
}

// Check is one top-level diagnostic result.
type Check struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// SpaceReport summarizes one installed space directory.
type SpaceReport struct {
	URI           string       `json:"uri"`
	Path          string       `json:"path"`
	Status        string       `json:"status"`
	ManifestPath  string       `json:"manifest_path,omitempty"`
	ManifestOK    bool         `json:"manifest_ok"`
	MarkdownFiles int          `json:"markdown_files"`
	Objects       int          `json:"objects"`
	Anchors       int          `json:"anchors"`
	Edges         int          `json:"edges"`
	ParseErrors   []ParseError `json:"parse_errors,omitempty"`
}

// ParseError records a file skipped by the mdpp/parser path.
type ParseError struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// IndexReport summarizes the rebuildable SQLite index.
type IndexReport struct {
	Path              string         `json:"path"`
	Status            string         `json:"status"`
	Exists            bool           `json:"exists"`
	Objects           int            `json:"objects"`
	ObjectsFTS        int            `json:"objects_fts"`
	Anchors           int            `json:"anchors"`
	Edges             int            `json:"edges"`
	Receipts          int            `json:"receipts"`
	Capabilities      int            `json:"capabilities"`
	ObjectsBySpace    map[string]int `json:"objects_by_space,omitempty"`
	Error             string         `json:"error,omitempty"`
	ParsedObjectTotal int            `json:"parsed_object_total"`
}

// ToolReport summarizes an optional external tool Hyphae can use.
type ToolReport struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Found   bool   `json:"found"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Run inspects root and returns a diagnostic report. Missing spaces or index
// files are represented in the report instead of returned as Go errors.
func Run(ctx context.Context, opts Options) (Report, error) {
	root := opts.Root
	if root == "" {
		resolved, err := db.Root()
		if err != nil {
			return Report{}, err
		}
		root = resolved
	}

	r := Report{
		Status: StatusOK,
		Root:   root,
		Index: IndexReport{
			Path:           filepath.Join(root, ".index", "hyphae.db"),
			Status:         StatusWarning,
			ObjectsBySpace: map[string]int{},
		},
	}

	if st, err := os.Stat(root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			r.addCheck("install_root", StatusError, "install root does not exist", "create it or set HYPHAE_HOME to an existing Hyphae install")
			r.addRecommendation("Create the install root or point HYPHAE_HOME at the right directory.")
		} else {
			r.addCheck("install_root", StatusError, "install root is not readable", err.Error())
		}
	} else if !st.IsDir() {
		r.addCheck("install_root", StatusError, "install root is not a directory", root)
	} else {
		r.addCheck("install_root", StatusOK, "install root exists", "")
	}

	r.Spaces = inspectSpaces(root, opts.IncludeInbox)
	if r.Spaces == nil {
		r.Spaces = []SpaceReport{}
	}
	if len(r.Spaces) == 0 {
		r.addCheck("spaces", StatusWarning, "no installed spaces found", "copy examples/seed-space under <root>/spaces/<authority>-<name>")
		r.addRecommendation("Install at least one space, then run `hypha index rebuild`.")
	} else {
		r.addCheck("spaces", StatusOK, fmt.Sprintf("%d installed space(s)", len(r.Spaces)), "")
	}

	r.Index = inspectIndex(root, parsedObjectTotal(r.Spaces))
	if r.Index.Status == StatusWarning && !r.Index.Exists {
		r.addRecommendation("Run `hypha index rebuild` after spaces are installed.")
	}
	if r.Index.Exists && r.Index.ParsedObjectTotal != r.Index.Objects {
		r.addRecommendation("Run `hypha index rebuild`; parsed object count differs from indexed object count.")
	}
	for _, sp := range r.Spaces {
		if len(sp.ParseErrors) > 0 {
			r.addRecommendation("Fix space parse errors; skipped markdown files will not be indexed or recalled.")
			break
		}
	}

	if opts.CheckCanopy {
		r.Tools = append(r.Tools, inspectCanopy(ctx, opts.ToolTimeout))
	}

	r.finalize()
	return r, nil
}

func inspectSpaces(root string, includeInbox bool) []SpaceReport {
	dir := filepath.Join(root, "spaces")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]SpaceReport, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		spaceID := spaceIDFromDirName(name)
		out = append(out, inspectSpace(filepath.Join(dir, name), spaceID, includeInbox))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].URI < out[j].URI })
	return out
}

func inspectSpace(spaceRoot, spaceID string, includeInbox bool) SpaceReport {
	sp := SpaceReport{
		URI:    "hypha://" + spaceID,
		Path:   spaceRoot,
		Status: StatusOK,
	}
	manifest := filepath.Join(spaceRoot, "SPACE.md")
	if st, err := os.Stat(manifest); err == nil && !st.IsDir() {
		sp.ManifestPath = manifest
		sp.ManifestOK = true
	} else {
		sp.Status = StatusWarning
		sp.ParseErrors = append(sp.ParseErrors, ParseError{
			Path:  "SPACE.md",
			Error: "missing space manifest",
		})
	}

	opts := parser.DefaultWalkOptions()
	opts.IncludeInbox = includeInbox
	stats, err := parser.WalkSpaceWithOptions(spaceRoot, spaceID, opts, nil)
	sp.MarkdownFiles = stats.MarkdownSeen
	sp.Objects = stats.Objects
	sp.Anchors = stats.Anchors
	sp.Edges = stats.Edges
	for _, skip := range stats.Skips {
		sp.ParseErrors = append(sp.ParseErrors, ParseError{Path: rel(spaceRoot, skip.Path), Error: skip.Reason})
	}
	if stats.SkipsTruncated {
		sp.ParseErrors = append(sp.ParseErrors, ParseError{Path: ".", Error: "additional skipped files omitted from doctor output"})
	}
	if err != nil {
		sp.Status = StatusError
		sp.ParseErrors = append(sp.ParseErrors, ParseError{Path: ".", Error: err.Error()})
	} else if len(sp.ParseErrors) > 0 && sp.Status == StatusOK {
		sp.Status = StatusWarning
	}
	return sp
}

func inspectIndex(root string, parsedObjects int) IndexReport {
	idx := IndexReport{
		Path:              filepath.Join(root, ".index", "hyphae.db"),
		Status:            StatusWarning,
		ObjectsBySpace:    map[string]int{},
		ParsedObjectTotal: parsedObjects,
	}
	st, err := os.Stat(idx.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			idx.Error = "index database does not exist"
			return idx
		}
		idx.Status = StatusError
		idx.Error = err.Error()
		return idx
	}
	if st.IsDir() {
		idx.Status = StatusError
		idx.Exists = true
		idx.Error = "index path is a directory"
		return idx
	}
	idx.Exists = true

	conn, err := openReadOnlyIndex(idx.Path)
	if err != nil {
		idx.Status = StatusError
		idx.Error = err.Error()
		return idx
	}
	defer conn.Close()

	if idx.Objects, err = countTable(conn, "objects"); err != nil {
		idx.Status = StatusError
		idx.Error = err.Error()
		return idx
	}
	idx.ObjectsFTS, _ = countTable(conn, "objects_fts")
	idx.Anchors, _ = countTable(conn, "anchors")
	idx.Edges, _ = countTable(conn, "edges")
	idx.Receipts, _ = countTable(conn, "receipts")
	idx.Capabilities, _ = countTable(conn, "capabilities")
	idx.ObjectsBySpace, _ = countObjectsBySpace(conn)

	idx.Status = StatusOK
	if parsedObjects != idx.Objects {
		idx.Status = StatusWarning
	}
	return idx
}

func openReadOnlyIndex(path string) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("mode", "ro")
	q.Set("_pragma", "foreign_keys(1)")
	u.RawQuery = q.Encode()
	return sql.Open("sqlite", u.String())
}

func inspectCanopy(ctx context.Context, timeout time.Duration) ToolReport {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "canopy", "--version")
	out, err := cmd.CombinedOutput()
	version := strings.TrimSpace(string(out))
	if err == nil {
		if !toolVersionAtLeast(version, 0, 16, 0) {
			return ToolReport{Name: "canopy", Status: StatusWarning, Found: true, Version: version, Error: "canopy older than v0.16.0"}
		}
		return ToolReport{Name: "canopy", Status: StatusOK, Found: true, Version: version}
	}
	if errors.Is(err, exec.ErrNotFound) {
		return ToolReport{Name: "canopy", Status: StatusWarning, Found: false, Error: "canopy not found on PATH"}
	}
	if ctx.Err() != nil {
		return ToolReport{Name: "canopy", Status: StatusWarning, Found: true, Version: version, Error: ctx.Err().Error()}
	}
	return ToolReport{Name: "canopy", Status: StatusWarning, Found: true, Version: version, Error: err.Error()}
}

func toolVersionAtLeast(s string, wantMajor, wantMinor, wantPatch int) bool {
	for _, field := range strings.Fields(s) {
		field = strings.TrimPrefix(field, "v")
		parts := strings.Split(field, ".")
		if len(parts) < 2 {
			continue
		}
		major, err1 := strconv.Atoi(parts[0])
		minor, err2 := strconv.Atoi(parts[1])
		patch := 0
		err3 := error(nil)
		if len(parts) > 2 {
			patchPart := strings.TrimRightFunc(parts[2], func(r rune) bool {
				return r < '0' || r > '9'
			})
			patch, err3 = strconv.Atoi(patchPart)
		}
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		if major != wantMajor {
			return major > wantMajor
		}
		if minor != wantMinor {
			return minor > wantMinor
		}
		return patch >= wantPatch
	}
	return true
}

func countTable(conn *sql.DB, table string) (int, error) {
	var n int
	if err := conn.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func countObjectsBySpace(conn *sql.DB) (map[string]int, error) {
	rows, err := conn.Query(`SELECT space_id, COUNT(*) FROM objects GROUP BY space_id ORDER BY space_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var spaceID string
		var count int
		if err := rows.Scan(&spaceID, &count); err != nil {
			return nil, err
		}
		out[spaceID] = count
	}
	return out, rows.Err()
}

func parsedObjectTotal(spaces []SpaceReport) int {
	total := 0
	for _, sp := range spaces {
		total += sp.Objects
	}
	return total
}

func spaceIDFromDirName(name string) string {
	if i := strings.Index(name, "-"); i > 0 {
		return name[:i] + "/" + name[i+1:]
	}
	return name
}

func rel(root, path string) string {
	if out, err := filepath.Rel(root, path); err == nil {
		return out
	}
	return path
}

func (r *Report) addCheck(name, status, message, hint string) {
	r.Checks = append(r.Checks, Check{Name: name, Status: status, Message: message, Hint: hint})
}

func (r *Report) addRecommendation(s string) {
	for _, existing := range r.Recommendations {
		if existing == s {
			return
		}
	}
	r.Recommendations = append(r.Recommendations, s)
}

func (r *Report) finalize() {
	status := StatusOK
	for _, c := range r.Checks {
		status = mergeStatus(status, c.Status)
	}
	status = mergeStatus(status, r.Index.Status)
	for _, sp := range r.Spaces {
		status = mergeStatus(status, sp.Status)
	}
	for _, t := range r.Tools {
		status = mergeStatus(status, t.Status)
	}
	r.Status = status
}

func mergeStatus(a, b string) string {
	if a == StatusError || b == StatusError {
		return StatusError
	}
	if a == StatusWarning || b == StatusWarning {
		return StatusWarning
	}
	return StatusOK
}

// WriteText renders a compact human report.
func WriteText(w io.Writer, r Report) error {
	fmt.Fprintf(w, "Hyphae doctor: %s\n", r.Status)
	fmt.Fprintf(w, "Root: %s\n\n", r.Root)

	fmt.Fprintln(w, "Checks:")
	for _, c := range r.Checks {
		fmt.Fprintf(w, "  [%s] %s: %s\n", c.Status, c.Name, c.Message)
		if c.Hint != "" {
			fmt.Fprintf(w, "       hint: %s\n", c.Hint)
		}
	}

	fmt.Fprintln(w, "\nSpaces:")
	if len(r.Spaces) == 0 {
		fmt.Fprintln(w, "  (none)")
	} else {
		for _, sp := range r.Spaces {
			fmt.Fprintf(w, "  [%s] %s\n", sp.Status, sp.URI)
			fmt.Fprintf(w, "       path: %s\n", sp.Path)
			fmt.Fprintf(w, "       markdown=%d objects=%d anchors=%d edges=%d\n", sp.MarkdownFiles, sp.Objects, sp.Anchors, sp.Edges)
			for i, pe := range sp.ParseErrors {
				if i >= 5 {
					fmt.Fprintf(w, "       ... %d more parse error(s)\n", len(sp.ParseErrors)-i)
					break
				}
				fmt.Fprintf(w, "       parse: %s: %s\n", pe.Path, pe.Error)
			}
		}
	}

	fmt.Fprintln(w, "\nIndex:")
	fmt.Fprintf(w, "  [%s] %s\n", r.Index.Status, r.Index.Path)
	if r.Index.Exists {
		fmt.Fprintf(w, "       objects=%d fts=%d anchors=%d edges=%d receipts=%d caps=%d\n",
			r.Index.Objects, r.Index.ObjectsFTS, r.Index.Anchors, r.Index.Edges, r.Index.Receipts, r.Index.Capabilities)
		fmt.Fprintf(w, "       parsed_objects=%d\n", r.Index.ParsedObjectTotal)
	} else if r.Index.Error != "" {
		fmt.Fprintf(w, "       %s\n", r.Index.Error)
	}

	if len(r.Tools) > 0 {
		fmt.Fprintln(w, "\nTools:")
		for _, t := range r.Tools {
			fmt.Fprintf(w, "  [%s] %s", t.Status, t.Name)
			if t.Version != "" {
				fmt.Fprintf(w, ": %s", t.Version)
			}
			if t.Error != "" {
				fmt.Fprintf(w, " (%s)", t.Error)
			}
			fmt.Fprintln(w)
		}
	}

	if len(r.Recommendations) > 0 {
		fmt.Fprintln(w, "\nRecommendations:")
		for _, rec := range r.Recommendations {
			fmt.Fprintf(w, "  - %s\n", rec)
		}
	}
	return nil
}
