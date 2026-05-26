// Package trace implements the v0.1.4 trace primitive — the in-flight,
// checkpoint-emitted record of how a piece of work happened. See
// ~/.hyphae/spaces/m31labs-hyphae/concepts/trace.md and decision 0009.
//
// Traces live at <space-root>/.trace/<YYYY-MM-DD>/<trace-id>.md. They are
// gitignored by default and never federate without an explicit per-space
// opt-in (out of scope for v0.1.4).
package trace

import (
	"crypto/rand"
	"encoding/hex"
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

// ErrNotFound is returned when a trace id can't be resolved within a space.
var ErrNotFound = errors.New("trace: not found")

// ErrClosed is returned by Tick when the trace is no longer open.
var ErrClosed = errors.New("trace: already closed (status != open)")

// StartOpts is the input to Start. SpaceRoot + SpaceID + AgentID are required.
type StartOpts struct {
	SpaceRoot    string // absolute path to the space directory
	SpaceID      string // hypha:// URI
	AgentID      string // agent URI emitting the trace
	AgentParent  string // optional
	AgentSession string // optional
	TaskRef      string // optional task identifier
	Phase        string // optional phase label
}

// ListFilter narrows List results. Zero value returns everything.
type ListFilter struct {
	ActiveOnly bool   // status == "open"
	Agent      string // exact match on agent.id
}

// Start creates a new open trace and writes its file to disk.
func Start(opts StartOpts) (types.Trace, error) {
	if strings.TrimSpace(opts.SpaceRoot) == "" {
		return types.Trace{}, errors.New("trace: space_root is required")
	}
	if strings.TrimSpace(opts.SpaceID) == "" {
		return types.Trace{}, errors.New("trace: space is required")
	}
	if strings.TrimSpace(opts.AgentID) == "" {
		return types.Trace{}, errors.New("trace: agent.id is required")
	}

	now := time.Now().UTC().Truncate(time.Second)
	id, err := newID(now, opts.AgentID)
	if err != nil {
		return types.Trace{}, err
	}

	tr := types.Trace{
		ID:           id,
		SpaceID:      opts.SpaceID,
		AgentID:      opts.AgentID,
		AgentParent:  opts.AgentParent,
		AgentSession: opts.AgentSession,
		TaskRef:      opts.TaskRef,
		Phase:        opts.Phase,
		Status:       types.TraceStatusOpen,
		Started:      now,
		LastTick:     now,
		Ticks:        nil,
	}

	if err := write(opts.SpaceRoot, &tr); err != nil {
		return types.Trace{}, err
	}
	return tr, nil
}

// Tick appends a checkpoint to an open trace and updates last_tick.
func Tick(spaceRoot, traceID, message string) error {
	tr, err := LoadByID(spaceRoot, traceID)
	if err != nil {
		return err
	}
	if tr.Status != types.TraceStatusOpen {
		return ErrClosed
	}
	now := time.Now().UTC().Truncate(time.Second)
	tr.Ticks = append(tr.Ticks, types.Tick{At: now, Message: strings.TrimSpace(message)})
	tr.LastTick = now
	return write(spaceRoot, &tr)
}

// Done closes a trace, sets its terminal status, and compacts the checkpoint
// list into a "Work log" section in the body. If linkedSpore is set, the
// trace's `linked_spore` frontmatter field is populated; when the spore file
// can be located on disk, the compacted work log is also appended to the
// spore body (idempotent — no double-append if the section already exists).
func Done(spaceRoot, traceID, status, linkedSpore string) (types.Trace, error) {
	switch status {
	case types.TraceStatusSucceeded, types.TraceStatusFailed,
		types.TraceStatusKilled, types.TraceStatusSuperseded:
		// ok
	default:
		return types.Trace{}, fmt.Errorf("trace: invalid status %q (want succeeded|failed|killed|superseded)", status)
	}
	tr, err := LoadByID(spaceRoot, traceID)
	if err != nil {
		return types.Trace{}, err
	}
	tr.Status = status
	tr.LastTick = time.Now().UTC().Truncate(time.Second)
	if linkedSpore != "" {
		tr.LinkedSpore = linkedSpore
	}
	if err := write(spaceRoot, &tr); err != nil {
		return types.Trace{}, err
	}
	if linkedSpore != "" {
		// Best-effort: locate the spore file under <space>/inbox/agents/ and
		// append a Work log section to its body. Failures don't unwind the
		// done() call — the trace itself is already persisted.
		if sporePath, ok := findSporeFile(spaceRoot, linkedSpore); ok {
			_ = appendWorkLogToSpore(sporePath, tr)
		}
	}
	return tr, nil
}

// findSporeFile locates a spore file under spaceRoot/inbox/agents/ whose
// frontmatter id matches sporeID. Returns (path, true) on hit.
func findSporeFile(spaceRoot, sporeID string) (string, bool) {
	dir := filepath.Join(spaceRoot, "inbox", "agents")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	needle := []byte("id: " + sporeID)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		// Only look in the frontmatter region (between first two ---).
		head := data
		if len(head) > 4096 {
			head = head[:4096]
		}
		if bytesContains(head, needle) {
			return p, true
		}
	}
	return "", false
}

// appendWorkLogToSpore appends a "## Work log (trace …)" section to a spore
// file's body. Idempotent: if a section with the same trace id already
// exists, returns nil without modifying.
func appendWorkLogToSpore(sporePath string, tr types.Trace) error {
	data, err := os.ReadFile(sporePath)
	if err != nil {
		return err
	}
	heading := fmt.Sprintf("## Work log (%s)", tr.ID)
	if bytesContains(data, []byte(heading)) {
		return nil
	}
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(heading)
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "_Compacted from trace `%s` (%s, ticks=%d, started=%s, last_tick=%s)._\n\n",
		tr.ID, tr.Status, len(tr.Ticks),
		tr.Started.UTC().Format(time.RFC3339),
		tr.LastTick.UTC().Format(time.RFC3339),
	)
	for _, t := range tr.Ticks {
		fmt.Fprintf(&b, "- %s  %s\n", t.At.UTC().Format(time.RFC3339), t.Message)
	}
	// Ensure trailing newline + appended block.
	if len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	data = append(data, []byte(b.String())...)
	return os.WriteFile(sporePath, data, 0o644)
}

func bytesContains(haystack, needle []byte) bool {
	return strings.Contains(string(haystack), string(needle))
}

// LoadByID resolves a trace id to a file path under spaceRoot/.trace and
// returns the parsed Trace.
func LoadByID(spaceRoot, traceID string) (types.Trace, error) {
	// Resolve by walking .trace/<date>/<id>.md — date is embedded in the id.
	parts := strings.Split(traceID, ".")
	if len(parts) < 3 || parts[0] != "trace" {
		return types.Trace{}, fmt.Errorf("trace: malformed id %q (want trace.<date>.<agent>.<short>)", traceID)
	}
	date := parts[1]
	path := filepath.Join(spaceRoot, ".trace", date, traceID+".md")
	if _, err := os.Stat(path); err != nil {
		// Fall back: full walk in case the date-segment was inferred wrong.
		if errors.Is(err, os.ErrNotExist) {
			if found, ok := scanForID(spaceRoot, traceID); ok {
				return load(found)
			}
		}
		return types.Trace{}, fmt.Errorf("%w: %s", ErrNotFound, traceID)
	}
	return load(path)
}

// List enumerates traces under spaceRoot/.trace, newest-first by LastTick.
func List(spaceRoot string, filter ListFilter) ([]types.Trace, error) {
	root := filepath.Join(spaceRoot, ".trace")
	var out []types.Trace
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	for _, day := range entries {
		if !day.IsDir() {
			continue
		}
		dayDir := filepath.Join(root, day.Name())
		files, _ := os.ReadDir(dayDir)
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".md") {
				continue
			}
			tr, err := load(filepath.Join(dayDir, f.Name()))
			if err != nil {
				continue
			}
			if filter.ActiveOnly && tr.Status != types.TraceStatusOpen {
				continue
			}
			if filter.Agent != "" && tr.AgentID != filter.Agent {
				continue
			}
			out = append(out, tr)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastTick.After(out[j].LastTick) })
	return out, nil
}

// ─── internals ───────────────────────────────────────────────────────────────

// newID generates trace.<YYYY-MM-DD>.<agent-short>.<hex4>.
func newID(now time.Time, agentID string) (string, error) {
	short := agentShort(agentID)
	if short == "" {
		short = "agent"
	}
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("trace: rand: %w", err)
	}
	return fmt.Sprintf("trace.%s.%s.%s", now.Format("2006-01-02"), short, hex.EncodeToString(b[:])), nil
}

// agentShort returns the last path segment of an agent URI, sanitized for ids.
//
//	agent://claude-code/walnut → walnut
//	identity://m31labs/odvcencio → odvcencio
func agentShort(uri string) string {
	for _, p := range []string{"agent://", "identity://", "service://"} {
		if strings.HasPrefix(uri, p) {
			uri = strings.TrimPrefix(uri, p)
			break
		}
	}
	parts := strings.Split(uri, "/")
	last := parts[len(parts)-1]
	last = regexp.MustCompile(`[^a-zA-Z0-9-]+`).ReplaceAllString(last, "-")
	return strings.Trim(last, "-")
}

// filePathFor returns the canonical on-disk path for a trace by id.
func filePathFor(spaceRoot, traceID string, startedDate string) string {
	return filepath.Join(spaceRoot, ".trace", startedDate, traceID+".md")
}

// scanForID walks spaceRoot/.trace looking for a file named <id>.md. Used as
// a fallback when the date segment in the id doesn't match the directory.
func scanForID(spaceRoot, traceID string) (string, bool) {
	root := filepath.Join(spaceRoot, ".trace")
	target := traceID + ".md"
	var found string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if !info.IsDir() && filepath.Base(p) == target {
			found = p
			return filepath.SkipAll
		}
		return nil
	})
	return found, found != ""
}

// write serializes a Trace to its canonical on-disk path. Idempotent: creates
// the date directory if needed and overwrites the file.
func write(spaceRoot string, tr *types.Trace) error {
	date := tr.Started.UTC().Format("2006-01-02")
	path := filePathFor(spaceRoot, tr.ID, date)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("trace: mkdir: %w", err)
	}
	tr.FilePath = path
	bytes := serialize(*tr)
	if err := os.WriteFile(path, bytes, 0o644); err != nil {
		return fmt.Errorf("trace: write: %w", err)
	}
	return nil
}

// serialize emits a Trace as a YAML-frontmatter + markdown-body file.
//
// The body holds the checkpoint list under "## Checkpoints" while open and
// under "## Work log" after Done (compacted form).
func serialize(tr types.Trace) []byte {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("mdpp: \"0.1\"\n")
	fmt.Fprintf(&b, "id: %s\n", tr.ID)
	b.WriteString("type: trace\n")
	fmt.Fprintf(&b, "space: %s\n", tr.SpaceID)
	b.WriteString("agent:\n")
	fmt.Fprintf(&b, "  id: %s\n", tr.AgentID)
	if tr.AgentParent != "" {
		fmt.Fprintf(&b, "  parent: %s\n", tr.AgentParent)
	}
	if tr.AgentSession != "" {
		fmt.Fprintf(&b, "  session: %s\n", tr.AgentSession)
	}
	if tr.TaskRef != "" {
		fmt.Fprintf(&b, "task_ref: %s\n", tr.TaskRef)
	}
	if tr.Phase != "" {
		fmt.Fprintf(&b, "phase: %s\n", tr.Phase)
	}
	fmt.Fprintf(&b, "status: %s\n", tr.Status)
	fmt.Fprintf(&b, "started: %s\n", tr.Started.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "last_tick: %s\n", tr.LastTick.UTC().Format(time.RFC3339))
	if tr.LinkedSpore != "" {
		fmt.Fprintf(&b, "linked_spore: %s\n", tr.LinkedSpore)
	}
	b.WriteString("---\n\n")

	title := tr.Phase
	if title == "" {
		title = tr.AgentID
	}
	fmt.Fprintf(&b, "# Trace: %s\n\n", title)

	heading := "Checkpoints"
	if tr.Status != types.TraceStatusOpen {
		heading = "Work log"
	}
	fmt.Fprintf(&b, "## %s\n\n", heading)

	for _, t := range tr.Ticks {
		fmt.Fprintf(&b, "- %s  %s\n", t.At.UTC().Format(time.RFC3339), t.Message)
	}

	return []byte(b.String())
}

// load reads and parses a trace file from disk.
func load(path string) (types.Trace, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return types.Trace{}, fmt.Errorf("trace: read %s: %w", path, err)
	}
	tr, err := parse(raw)
	if err != nil {
		return types.Trace{}, fmt.Errorf("trace: parse %s: %w", path, err)
	}
	tr.FilePath = path
	return tr, nil
}

// parse extracts a Trace from serialized bytes. Hand-rolled — the trace
// frontmatter is small and stable enough that a YAML library would be more
// machinery than the format needs.
func parse(raw []byte) (types.Trace, error) {
	s := string(raw)
	if !strings.HasPrefix(s, "---\n") {
		return types.Trace{}, errors.New("missing leading frontmatter delimiter")
	}
	rest := s[len("---\n"):]
	idx := strings.Index(rest, "\n---\n")
	if idx < 0 {
		return types.Trace{}, errors.New("missing trailing frontmatter delimiter")
	}
	fmBlock := rest[:idx]
	body := strings.TrimSpace(rest[idx+len("\n---\n"):])

	tr := types.Trace{}
	inAgent := false
	for _, line := range strings.Split(fmBlock, "\n") {
		raw := line
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// agent block: lines indented under "agent:"
		if strings.HasPrefix(raw, "agent:") {
			inAgent = true
			continue
		}
		if inAgent && (strings.HasPrefix(raw, "  ") || strings.HasPrefix(raw, "\t")) {
			k, v, ok := splitKV(trimmed)
			if !ok {
				continue
			}
			switch k {
			case "id":
				tr.AgentID = v
			case "parent":
				tr.AgentParent = v
			case "session":
				tr.AgentSession = v
			}
			continue
		}
		inAgent = false

		k, v, ok := splitKV(trimmed)
		if !ok {
			continue
		}
		switch k {
		case "id":
			tr.ID = v
		case "space":
			tr.SpaceID = v
		case "task_ref":
			tr.TaskRef = v
		case "phase":
			tr.Phase = v
		case "status":
			tr.Status = v
		case "started":
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				tr.Started = t.UTC()
			}
		case "last_tick":
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				tr.LastTick = t.UTC()
			}
		case "linked_spore":
			tr.LinkedSpore = v
		}
	}

	// Parse checkpoint lines from the body.
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		entry := strings.TrimSpace(line[2:])
		// "<RFC3339>  <message>" — split on the first run of whitespace ≥ 2.
		ts, msg, ok := splitTickLine(entry)
		if !ok {
			continue
		}
		at, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			continue
		}
		tr.Ticks = append(tr.Ticks, types.Tick{At: at.UTC(), Message: msg})
	}
	tr.Body = body
	return tr, nil
}

// splitKV returns key, value for "key: value" lines, trimming surrounding quotes.
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

// splitTickLine splits "RFC3339-timestamp  message" using the first 2+ spaces.
func splitTickLine(s string) (string, string, bool) {
	re := regexp.MustCompile(`\s{2,}`)
	idx := re.FindStringIndex(s)
	if idx == nil {
		// Fallback: split on the first single space.
		if sp := strings.Index(s, " "); sp > 0 {
			return s[:sp], strings.TrimSpace(s[sp+1:]), true
		}
		return "", "", false
	}
	return s[:idx[0]], strings.TrimSpace(s[idx[1]:]), true
}
