// Package analyze materializes canopy outputs as hyphae `analysis` objects
// under `<space-root>/.analyses/<kind>/<slug>@<commit>.md`. Local-by-default;
// never federates (directory is gitignored). Freshness is stale-tolerant:
// recall/show return cached results with `Stale: true` when commit ≠ HEAD
// or any target file's mtime is newer than ComputedAt.
//
// Canopy is invoked as a subprocess via the `canopy` CLI; hyphae parses the
// JSON output and renders a markdown body that's both human-readable and
// FTS5-indexable.
package analyze

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"m31labs.dev/hyphae/internal/types"
)

// FreshnessInputs configures the staleness check. Any non-empty CurrentCommit
// is compared to the analysis Commit; any non-zero mtime in TargetMtimes is
// compared to ComputedAt.
type FreshnessInputs struct {
	CurrentCommit string
	TargetMtimes  map[string]time.Time // path → mtime
}

// FreshnessResult is the read-time staleness verdict. Reason is empty when
// Stale is false; otherwise it names the trigger.
type FreshnessResult struct {
	Stale  bool
	Reason string
}

const (
	StaleReasonCommit = "commit-drift"
	StaleReasonMtime  = "mtime-drift"
)

// ListFilter narrows List results. Zero value returns everything.
type ListFilter struct {
	Kind       string
	TargetFile string // matches Target or any entry in TargetFiles
}

// Write serializes an Analysis to its canonical on-disk path under
// `<spaceRoot>/.analyses/<kind>/<slug>@<short-commit>.md` and sets
// `a.FilePath` + `a.ComputedAt` if unset.
func Write(spaceRoot string, a *types.Analysis) error {
	if a.Kind == "" {
		return errors.New("analyze: Kind required")
	}
	if a.Target == "" {
		return errors.New("analyze: Target required")
	}
	if a.ComputedAt.IsZero() {
		a.ComputedAt = time.Now().UTC().Truncate(time.Second)
	}
	if a.Tool == "" {
		a.Tool = "canopy"
	}
	if a.ID == "" {
		a.ID = idFor(a.Kind, a.Target, a.Commit)
	}
	dir := filepath.Join(spaceRoot, ".analyses", a.Kind)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("analyze: mkdir %s: %w", dir, err)
	}
	a.FilePath = filepath.Join(dir, fileSlug(a.Target, a.Commit)+".md")
	return os.WriteFile(a.FilePath, []byte(serialize(*a)), 0o644)
}

// Load reads and parses an analysis file from disk.
func Load(path string) (types.Analysis, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return types.Analysis{}, fmt.Errorf("analyze: read %s: %w", path, err)
	}
	a, err := parse(raw)
	if err != nil {
		return types.Analysis{}, fmt.Errorf("analyze: parse %s: %w", path, err)
	}
	a.FilePath = path
	return a, nil
}

// List enumerates analyses under spaceRoot/.analyses, filtered by kind and
// target-file overlap. Returns newest-computed first.
func List(spaceRoot string, filter ListFilter) ([]types.Analysis, error) {
	root := filepath.Join(spaceRoot, ".analyses")
	var out []types.Analysis
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	for _, kind := range entries {
		if !kind.IsDir() {
			continue
		}
		if filter.Kind != "" && kind.Name() != filter.Kind {
			continue
		}
		kindDir := filepath.Join(root, kind.Name())
		files, _ := os.ReadDir(kindDir)
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".md") {
				continue
			}
			a, err := Load(filepath.Join(kindDir, f.Name()))
			if err != nil {
				continue
			}
			if filter.TargetFile != "" && !matchesTarget(a, filter.TargetFile) {
				continue
			}
			out = append(out, a)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ComputedAt.After(out[j].ComputedAt) })
	return out, nil
}

// CheckFreshness computes the staleness verdict against current repo state.
// Mutates a.Stale as a side-effect for convenience.
func CheckFreshness(a *types.Analysis, in FreshnessInputs) FreshnessResult {
	if in.CurrentCommit != "" && a.Commit != "" {
		if !strings.HasPrefix(in.CurrentCommit, a.Commit) &&
			!strings.HasPrefix(a.Commit, in.CurrentCommit) {
			a.Stale = true
			return FreshnessResult{Stale: true, Reason: StaleReasonCommit}
		}
	}
	for path, mt := range in.TargetMtimes {
		if mt.IsZero() {
			continue
		}
		if mt.After(a.ComputedAt) && stringSliceContains(a.TargetFiles, path) {
			a.Stale = true
			return FreshnessResult{Stale: true, Reason: StaleReasonMtime}
		}
	}
	a.Stale = false
	return FreshnessResult{Stale: false}
}

// ─── canopy parsers ──────────────────────────────────────────────────────────

// impactJSON mirrors canopy graph impact --json output.
type impactJSON struct {
	Changed       []string `json:"changed"`
	Affected      []struct {
		Name      string  `json:"name"`
		File      string  `json:"file"`
		Kind      string  `json:"kind"`
		StartLine int     `json:"start_line"`
		EndLine   int     `json:"end_line"`
		Distance  int     `json:"distance"`
		Risk      float64 `json:"risk"`
	} `json:"affected"`
	AffectedFiles []string `json:"affected_files"`
	TotalAffected int      `json:"total_affected"`
}

// ParseImpact converts canopy graph impact --json output into a partial
// Analysis (Kind, RawJSON, TotalAffected, TopFiles, TopSymbols populated;
// caller fills Target, Commit, etc. before Write).
func ParseImpact(raw []byte) (types.Analysis, error) {
	var ij impactJSON
	if err := json.Unmarshal(raw, &ij); err != nil {
		return types.Analysis{}, fmt.Errorf("impact json: %w", err)
	}

	// Count file frequencies for top-N ranking.
	fileCounts := make(map[string]int)
	for _, a := range ij.Affected {
		fileCounts[a.File]++
	}
	type fileCount struct {
		File  string
		Count int
	}
	fc := make([]fileCount, 0, len(fileCounts))
	for f, c := range fileCounts {
		fc = append(fc, fileCount{f, c})
	}
	sort.SliceStable(fc, func(i, j int) bool {
		if fc[i].Count != fc[j].Count {
			return fc[i].Count > fc[j].Count
		}
		return fc[i].File < fc[j].File
	})
	topFiles := make([]string, 0, 5)
	for i, e := range fc {
		if i >= 5 {
			break
		}
		topFiles = append(topFiles, e.File)
	}

	// Top symbols: lowest distance first, then alphabetical.
	type aff struct {
		Name     string
		Distance int
	}
	syms := make([]aff, 0, len(ij.Affected))
	for _, a := range ij.Affected {
		syms = append(syms, aff{a.Name, a.Distance})
	}
	sort.SliceStable(syms, func(i, j int) bool {
		if syms[i].Distance != syms[j].Distance {
			return syms[i].Distance < syms[j].Distance
		}
		return syms[i].Name < syms[j].Name
	})
	topSyms := make([]string, 0, 5)
	for i, s := range syms {
		if i >= 5 {
			break
		}
		topSyms = append(topSyms, s.Name)
	}

	return types.Analysis{
		Kind:          types.AnalysisKindImpact,
		TotalAffected: ij.TotalAffected,
		TopFiles:      topFiles,
		TopSymbols:    topSyms,
		RawJSON:       string(raw),
	}, nil
}

// ─── id + path helpers ───────────────────────────────────────────────────────

var slugRe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// fileSlug derives the on-disk filename body (without extension) for an
// analysis. "<slug>@<short-commit>" — e.g. "cmd-hypha-main@a6f1fc0".
func fileSlug(target, commit string) string {
	t := strings.ReplaceAll(target, "/", "-")
	t = strings.TrimPrefix(t, "-")
	t = strings.TrimSuffix(t, "-")
	t = slugRe.ReplaceAllString(t, "-")
	t = strings.Trim(t, "-")
	if t == "" {
		t = "target"
	}
	if commit == "" {
		return t
	}
	return t + "@" + shortCommit(commit)
}

// idFor constructs the canonical analysis id.
func idFor(kind, target, commit string) string {
	return "analysis." + kind + "." + fileSlug(target, "") + "." + shortCommit(commit)
}

func shortCommit(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}

func matchesTarget(a types.Analysis, file string) bool {
	if a.Target == file {
		return true
	}
	return stringSliceContains(a.TargetFiles, file)
}

func stringSliceContains(s []string, v string) bool {
	for _, e := range s {
		if e == v {
			return true
		}
	}
	return false
}

// ─── serialize / parse ───────────────────────────────────────────────────────

func serialize(a types.Analysis) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("mdpp: \"0.1\"\n")
	fmt.Fprintf(&b, "id: %s\n", a.ID)
	b.WriteString("type: analysis\n")
	fmt.Fprintf(&b, "kind: %s\n", a.Kind)
	if a.SpaceID != "" {
		fmt.Fprintf(&b, "space: %s\n", a.SpaceID)
	}
	fmt.Fprintf(&b, "target: %s\n", a.Target)
	if len(a.TargetFiles) > 0 {
		b.WriteString("target_files:\n")
		for _, f := range a.TargetFiles {
			fmt.Fprintf(&b, "  - %s\n", f)
		}
	}
	if a.Commit != "" {
		fmt.Fprintf(&b, "commit: %s\n", a.Commit)
	}
	fmt.Fprintf(&b, "computed_at: %s\n", a.ComputedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "tool: %s\n", a.Tool)
	if a.ToolVersion != "" {
		fmt.Fprintf(&b, "tool_version: \"%s\"\n", a.ToolVersion)
	}
	if a.TotalAffected > 0 {
		fmt.Fprintf(&b, "total_affected: %d\n", a.TotalAffected)
	}
	if len(a.TopFiles) > 0 {
		b.WriteString("top_files:\n")
		for _, f := range a.TopFiles {
			fmt.Fprintf(&b, "  - %s\n", f)
		}
	}
	if len(a.TopSymbols) > 0 {
		b.WriteString("top_symbols:\n")
		for _, s := range a.TopSymbols {
			fmt.Fprintf(&b, "  - %s\n", s)
		}
	}
	b.WriteString("---\n\n")

	// Body.
	fmt.Fprintf(&b, "# %s: %s", titleForKind(a.Kind), a.Target)
	if a.Commit != "" {
		fmt.Fprintf(&b, " (%s)", shortCommit(a.Commit))
	}
	b.WriteString("\n\n## Summary\n\n")
	fmt.Fprintf(&b, "Tool: %s%s. ", a.Tool, optVersion(a.ToolVersion))
	if a.TotalAffected > 0 {
		fmt.Fprintf(&b, "%d affected symbols across %d files.\n", a.TotalAffected, len(a.TopFiles))
	} else {
		b.WriteString("No affected symbols recorded.\n")
	}

	if len(a.TopFiles) > 0 {
		b.WriteString("\n## Top affected files\n\n")
		for _, f := range a.TopFiles {
			fmt.Fprintf(&b, "- %s\n", f)
		}
	}
	if len(a.TopSymbols) > 0 {
		b.WriteString("\n## Top affected symbols\n\n")
		for _, s := range a.TopSymbols {
			fmt.Fprintf(&b, "- %s\n", s)
		}
	}

	if a.RawJSON != "" {
		b.WriteString("\n## Raw\n\n```json\n")
		b.WriteString(a.RawJSON)
		if !strings.HasSuffix(a.RawJSON, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("```\n")
	}

	return b.String()
}

func titleForKind(kind string) string {
	switch kind {
	case types.AnalysisKindImpact:
		return "Impact"
	case types.AnalysisKindCallgraph:
		return "Callgraph"
	case types.AnalysisKindRefs:
		return "Refs"
	case types.AnalysisKindHotspot:
		return "Hotspot"
	case types.AnalysisKindDead:
		return "Dead"
	case types.AnalysisKindReview:
		return "Review"
	default:
		return "Analysis"
	}
}

func optVersion(v string) string {
	if v == "" {
		return ""
	}
	return " " + v
}

func parse(raw []byte) (types.Analysis, error) {
	s := string(raw)
	if !strings.HasPrefix(s, "---\n") {
		return types.Analysis{}, errors.New("missing leading frontmatter delimiter")
	}
	rest := s[len("---\n"):]
	idx := strings.Index(rest, "\n---\n")
	if idx < 0 {
		return types.Analysis{}, errors.New("missing trailing frontmatter delimiter")
	}
	fmBlock := rest[:idx]
	body := rest[idx+len("\n---\n"):]

	a := types.Analysis{}
	var listKey string // "target_files" | "top_files" | "top_symbols"
	for _, line := range strings.Split(fmBlock, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			listKey = ""
			continue
		}
		// List continuation: indented `- value`.
		if (strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t")) &&
			strings.HasPrefix(trimmed, "- ") {
			v := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			v = strings.Trim(v, `"`)
			switch listKey {
			case "target_files":
				a.TargetFiles = append(a.TargetFiles, v)
			case "top_files":
				a.TopFiles = append(a.TopFiles, v)
			case "top_symbols":
				a.TopSymbols = append(a.TopSymbols, v)
			}
			continue
		}
		listKey = ""

		k, v, ok := splitKV(trimmed)
		if !ok {
			continue
		}
		switch k {
		case "id":
			a.ID = v
		case "kind":
			a.Kind = v
		case "space":
			a.SpaceID = v
		case "target":
			a.Target = v
		case "target_files":
			listKey = "target_files"
		case "top_files":
			listKey = "top_files"
		case "top_symbols":
			listKey = "top_symbols"
		case "commit":
			a.Commit = v
		case "computed_at":
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				a.ComputedAt = t.UTC()
			}
		case "tool":
			a.Tool = v
		case "tool_version":
			a.ToolVersion = v
		case "total_affected":
			fmt.Sscanf(v, "%d", &a.TotalAffected)
		}
	}

	a.Body = strings.TrimSpace(body)
	a.RawJSON = extractRawJSON(body)
	return a, nil
}

// extractRawJSON pulls the JSON out of a fenced ```json ... ``` block.
func extractRawJSON(body string) string {
	const marker = "```json\n"
	start := strings.Index(body, marker)
	if start < 0 {
		return ""
	}
	rest := body[start+len(marker):]
	end := strings.Index(rest, "```")
	if end < 0 {
		return ""
	}
	return strings.TrimRight(rest[:end], "\n")
}

func splitKV(line string) (string, string, bool) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return "", "", false
	}
	k := strings.TrimSpace(line[:idx])
	v := strings.TrimSpace(line[idx+1:])
	v = strings.Trim(v, `"`)
	return k, v, k != ""
}
