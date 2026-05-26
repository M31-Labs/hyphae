package analyze

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"m31labs.dev/hyphae/internal/types"
)

// RunOpts is the input to Run. SourcePath is the source repo root (where
// canopy will run); SpaceRoot is the hyphae space dir (where .analyses/
// will be written).
type RunOpts struct {
	Kind       string
	Target     string // file path, symbol, or "repo" for repo-wide kinds
	SourcePath string
	SpaceRoot  string
	SpaceID    string
	MaxDepth   int    // optional, kind-specific (impact, callgraph)
	DiffRef    string // optional, for impact/review
}

// Run invokes canopy for the requested kind, parses the JSON output, writes
// an Analysis file under SpaceRoot/.analyses/, and returns the persisted
// Analysis. Commit is auto-discovered from `git rev-parse HEAD` in SourcePath.
func Run(opts RunOpts) (types.Analysis, error) {
	if opts.SourcePath == "" {
		return types.Analysis{}, fmt.Errorf("analyze: SourcePath required")
	}
	if opts.SpaceRoot == "" {
		return types.Analysis{}, fmt.Errorf("analyze: SpaceRoot required")
	}
	if opts.Kind == "" {
		return types.Analysis{}, fmt.Errorf("analyze: Kind required")
	}

	commit, _ := gitHead(opts.SourcePath)
	toolVer, _ := canopyVersion()

	args, err := canopyArgsFor(opts)
	if err != nil {
		return types.Analysis{}, err
	}
	rawJSON, err := runCanopy(opts.SourcePath, args)
	if err != nil {
		return types.Analysis{}, err
	}

	var a types.Analysis
	switch opts.Kind {
	case types.AnalysisKindImpact:
		a, err = ParseImpact(rawJSON)
	default:
		// Other kinds: store raw JSON; structured fields stay zero.
		a = types.Analysis{Kind: opts.Kind, RawJSON: string(rawJSON)}
	}
	if err != nil {
		return types.Analysis{}, fmt.Errorf("analyze parse %s: %w", opts.Kind, err)
	}

	a.Target = opts.Target
	if a.Target == "" {
		a.Target = "repo"
	}
	a.SpaceID = opts.SpaceID
	a.Commit = commit
	a.Tool = "canopy"
	a.ToolVersion = toolVer
	a.ComputedAt = time.Now().UTC().Truncate(time.Second)
	a.TargetFiles = targetFilesFor(opts, a)

	if err := Write(opts.SpaceRoot, &a); err != nil {
		return types.Analysis{}, err
	}
	return a, nil
}

// canopyArgsFor builds the canopy command for a given kind. Each branch is
// kept narrow on purpose so adding a new kind is a one-line case.
func canopyArgsFor(opts RunOpts) ([]string, error) {
	switch opts.Kind {
	case types.AnalysisKindImpact:
		args := []string{"graph", "impact", "--json"}
		if opts.MaxDepth > 0 {
			args = append(args, "--max-depth", fmt.Sprintf("%d", opts.MaxDepth))
		}
		if opts.DiffRef != "" {
			args = append(args, "--diff", opts.DiffRef)
		}
		if opts.Target != "" && opts.Target != "repo" {
			args = append(args, opts.Target)
		}
		return args, nil
	case types.AnalysisKindCallgraph:
		args := []string{"graph", "calls", "--json"}
		if opts.MaxDepth > 0 {
			args = append(args, "--max-depth", fmt.Sprintf("%d", opts.MaxDepth))
		}
		if opts.Target != "" && opts.Target != "repo" {
			args = append(args, opts.Target)
		}
		return args, nil
	case types.AnalysisKindRefs:
		if opts.Target == "" || opts.Target == "repo" {
			return nil, fmt.Errorf("analyze: refs requires a symbol target")
		}
		return []string{"search", "refs", "--json", opts.Target}, nil
	case types.AnalysisKindHotspot:
		return []string{"analyze", "hotspot", "--json"}, nil
	case types.AnalysisKindDead:
		return []string{"graph", "dead", "--json"}, nil
	case types.AnalysisKindReview:
		args := []string{"analyze", "review", "--json"}
		if opts.DiffRef != "" {
			args = append(args, "--base", opts.DiffRef)
		}
		return args, nil
	default:
		return nil, fmt.Errorf("analyze: unknown kind %q", opts.Kind)
	}
}

// targetFilesFor derives the list of files this analysis depends on for
// staleness. For impact: the affected file list plus the target file.
// For others: the target if it's a file path; otherwise empty (repo-wide).
func targetFilesFor(opts RunOpts, a types.Analysis) []string {
	switch opts.Kind {
	case types.AnalysisKindImpact:
		// TopFiles is already populated by ParseImpact; ensure target itself
		// is included if it's a file path.
		set := make(map[string]struct{}, len(a.TopFiles)+1)
		for _, f := range a.TopFiles {
			set[f] = struct{}{}
		}
		if isFilePath(opts.Target) {
			set[opts.Target] = struct{}{}
		}
		out := make([]string, 0, len(set))
		for f := range set {
			out = append(out, f)
		}
		return out
	default:
		if isFilePath(opts.Target) {
			return []string{opts.Target}
		}
		return nil
	}
}

func isFilePath(s string) bool {
	if s == "" || s == "repo" {
		return false
	}
	return strings.Contains(s, "/") || strings.Contains(s, ".")
}

// runCanopy invokes the canopy binary in workdir with the given args and
// returns the raw JSON output. Canopy may emit log lines to stdout before
// the JSON; the JSON starts at the first '{' or '['.
func runCanopy(workdir string, args []string) ([]byte, error) {
	cmd := exec.Command("canopy", args...)
	cmd.Dir = workdir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("canopy %s: %w (stderr: %s)",
			strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	out := stdout.Bytes()
	// Strip leading non-JSON prefix (canopy emits an "index: using cached..."
	// status line before the actual JSON).
	for i, c := range out {
		if c == '{' || c == '[' {
			return out[i:], nil
		}
	}
	return nil, fmt.Errorf("canopy %s: no JSON in output", strings.Join(args, " "))
}

// gitHead returns the current HEAD short SHA in workdir, or empty if git is
// unavailable or the directory isn't a repo.
func gitHead(workdir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = workdir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// canopyVersion returns the canopy CLI version string, or empty on failure.
func canopyVersion() (string, error) {
	cmd := exec.Command("canopy", "--version")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	// canopy --version emits one line like "canopy v0.x.y" — take everything
	// after the first space.
	line := strings.TrimSpace(string(out))
	if idx := strings.Index(line, " "); idx > 0 {
		return strings.TrimSpace(line[idx+1:]), nil
	}
	return line, nil
}
