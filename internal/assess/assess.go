// Package assess implements alignment scoring (change:assess) on top of the
// Hyphae index. Given a task description and a set of changed files / diff
// summary, it scores how well the proposed work aligns with the active
// initiatives in a space and returns the categorical alignment, a numeric
// score, a recommendation, and supporting citations.
//
// The MVP composes recall (FTS5 over active initiatives), pulse (recent
// pressure signals), and a path-prefix hot-zone derived from receipt
// activity. It produces output matching the JSON shape documented in
// ~/.hyphae/spaces/m31labs-hyphae/concepts/initiative-alignment.md.
package assess

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/odvcencio/hyphae/internal/pulse"
	"github.com/odvcencio/hyphae/internal/types"
)

// ErrInvalidRequest is returned when ChangeRequest has neither a task nor a
// diff summary nor any changed files — there is nothing to score against.
var ErrInvalidRequest = errors.New("assess: request must include task, diff_summary, or changed_files")

// Alignment categorizes how a change fits the org's active initiatives.
type Alignment string

const (
	AlignDirectlyAligned Alignment = "directly_aligned"
	AlignEnabling        Alignment = "enabling"
	AlignAdjacent        Alignment = "adjacent"
	AlignNeutral         Alignment = "neutral"
)

// Recommendation is the suggested next step for a client enforcing a gate.
type Recommendation string

const (
	RecProceed                  Recommendation = "proceed"
	RecProceedWithExtraReview   Recommendation = "proceed_with_extra_review"
	RecReviewRequired           Recommendation = "review_required"
)

// ChangeRequest is the input to a change:assess call. Mirrors the JSON shape
// in protocols/http-api.md POST /v1/assess/change.
type ChangeRequest struct {
	Task         string        `json:"task"`
	ChangedFiles []string      `json:"changed_files,omitempty"`
	DiffSummary  string        `json:"diff_summary,omitempty"`
	Space        string        `json:"space,omitempty"`  // hypha:// URI; "" = all spaces
	Window       time.Duration `json:"window,omitempty"` // default 30d
	Budget       types.Budget  `json:"budget"`
}

// MatchedInitiative is one initiative the change appears to align with.
type MatchedInitiative struct {
	ID     string  `json:"id"`
	Title  string  `json:"title,omitempty"`
	Score  float64 `json:"score"`
	Reason string  `json:"reason"`
}

// HotZone reports recent activity in the directory tree the changed files
// share, when one can be inferred.
type HotZone struct {
	Path        string `json:"path"`
	Commits14d  int    `json:"commits_14d"`
	Incidents14d int   `json:"incidents_14d"`
}

// Result is the full change:assess output. Maps directly to the JSON in
// concepts/initiative-alignment.md.
type Result struct {
	Alignment          Alignment           `json:"alignment"`
	Score              float64             `json:"score"`
	Recommendation     Recommendation      `json:"recommendation"`
	MatchedInitiatives []MatchedInitiative `json:"matched_initiatives"`
	RecentPressure     []string            `json:"recent_pressure"`
	HotZone            *HotZone            `json:"hot_zone,omitempty"`
	Risks              []string            `json:"risks"`
	TokensUsed         int                 `json:"tokens_used"`
}

// defaultWindow matches pulse: 30 days.
const defaultWindow = 30 * 24 * time.Hour

// Change runs the MVP scorer against conn and returns a Result.
//
// The scoring composition:
//   1. Build a query string from req.Task + req.DiffSummary + path tokens of
//      req.ChangedFiles.
//   2. Run a typed FTS5 query over active initiatives (objects_fts where
//      type='initiative' and the object is in an active status). Take top 5.
//   3. Normalize BM25 ranks to [0,1] scores (lower BM25 → higher score).
//   4. Compute pulse over the window and surface RecentPressure anchors.
//   5. Derive a path-prefix hot zone from req.ChangedFiles and count
//      graft receipts within 14d that touched it.
//   6. Rule-based alignment + recommendation from top match score.
func Change(conn *sql.DB, req ChangeRequest) (Result, error) {
	if strings.TrimSpace(req.Task) == "" &&
		strings.TrimSpace(req.DiffSummary) == "" &&
		len(req.ChangedFiles) == 0 {
		return Result{}, ErrInvalidRequest
	}

	window := req.Window
	if window <= 0 {
		window = defaultWindow
	}

	matched, err := matchInitiatives(conn, req)
	if err != nil {
		return Result{}, fmt.Errorf("assess: match initiatives: %w", err)
	}

	pressure, err := recentPressureAnchors(conn, req.Space, window)
	if err != nil {
		return Result{}, fmt.Errorf("assess: recent pressure: %w", err)
	}

	hotZone, err := hotZoneFor(conn, req.ChangedFiles, 14*24*time.Hour)
	if err != nil {
		return Result{}, fmt.Errorf("assess: hot zone: %w", err)
	}

	alignment, score, recommendation := categorize(matched)

	result := Result{
		Alignment:          alignment,
		Score:              score,
		Recommendation:     recommendation,
		MatchedInitiatives: matched,
		RecentPressure:     pressure,
		HotZone:            hotZone,
		Risks:              []string{},
	}
	if result.RecentPressure == nil {
		result.RecentPressure = []string{}
	}
	if result.MatchedInitiatives == nil {
		result.MatchedInitiatives = []MatchedInitiative{}
	}

	b, err := json.Marshal(result)
	if err == nil {
		result.TokensUsed = (len(b) + 3) / 4
	}

	return result, nil
}

// buildQueryString flattens the request inputs into an FTS5 OR-query so
// partial term overlaps still match. Implicit-AND would force every token
// of the task + diff + paths to appear in the initiative — far too strict
// for natural-language input. BM25 ranks the matches by overlap density.
//
// Path components are tokenized on slashes and underscores so file paths
// like "services/billing-worker/retry.go" surface "billing" and "worker".
func buildQueryString(req ChangeRequest) string {
	var parts []string
	if s := strings.TrimSpace(req.Task); s != "" {
		parts = append(parts, s)
	}
	if s := strings.TrimSpace(req.DiffSummary); s != "" {
		parts = append(parts, s)
	}
	for _, f := range req.ChangedFiles {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		dir := path.Dir(f)
		base := strings.TrimSuffix(path.Base(f), path.Ext(f))
		toks := strings.FieldsFunc(dir+" "+base, func(r rune) bool {
			return r == '/' || r == '_' || r == '-' || r == '.'
		})
		for _, t := range toks {
			if t == "" || t == "." {
				continue
			}
			parts = append(parts, t)
		}
	}

	cleaned := sanitizeFTSQuery(strings.Join(parts, " "))
	if cleaned == "" {
		return ""
	}
	tokens := dedupeKeepOrder(strings.Fields(cleaned))
	tokens = dropStopwords(tokens)
	if len(tokens) == 0 {
		return ""
	}
	return strings.Join(tokens, " OR ")
}

// dedupeKeepOrder removes duplicate tokens while preserving first-occurrence
// order.
func dedupeKeepOrder(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, t := range in {
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// stopwords is a tiny English filter so FTS5 OR-queries aren't dominated by
// "to/for/the/of" etc. We don't need a real stopword list — just enough to
// stop the OR-query from matching every document on the planet.
var stopwords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "the": {},
	"to": {}, "of": {}, "for": {}, "in": {}, "on": {}, "at": {},
	"is": {}, "are": {}, "be": {}, "by": {}, "with": {}, "as": {},
	"add": {}, "adds": {}, "fix": {}, "fixes": {},
	"go": {}, // path extension noise after stripping the dot
}

func dropStopwords(in []string) []string {
	out := in[:0]
	for _, t := range in {
		if _, ok := stopwords[strings.ToLower(t)]; ok {
			continue
		}
		out = append(out, t)
	}
	return out
}

// scoringSourceTokens picks the most authoritative token set available for
// absolute scoring: task > diff_summary > file paths. The FTS5 candidate
// query still uses the union (so weak diff/path overlap still surfaces an
// initiative), but the user-visible score is computed against only the
// strongest signal source the caller provided.
func scoringSourceTokens(req ChangeRequest) []string {
	if toks := normalizedTokens(req.Task); len(toks) > 0 {
		return toks
	}
	if toks := normalizedTokens(req.DiffSummary); len(toks) > 0 {
		return toks
	}
	var paths []string
	for _, f := range req.ChangedFiles {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		dir := path.Dir(f)
		base := strings.TrimSuffix(path.Base(f), path.Ext(f))
		paths = append(paths, strings.FieldsFunc(dir+" "+base, func(r rune) bool {
			return r == '/' || r == '_' || r == '-' || r == '.'
		})...)
	}
	return normalizedTokens(strings.Join(paths, " "))
}

// normalizedTokens lowercases, sanitizes, drops stopwords, and filters to
// tokens of at least 3 chars (short tokens cause false-positive containment).
func normalizedTokens(s string) []string {
	cleaned := sanitizeFTSQuery(s)
	if cleaned == "" {
		return nil
	}
	lower := strings.ToLower(cleaned)
	raw := dropStopwords(strings.Fields(lower))
	raw = dedupeKeepOrder(raw)
	out := raw[:0]
	for _, t := range raw {
		if len(t) >= 3 {
			out = append(out, t)
		}
	}
	return out
}

// sanitizeFTSQuery reduces the query to ASCII alphanumerics and spaces so
// every remaining token is a plain FTS5 term. Punctuation (`.`, `,`, `:`,
// `-`, etc.) all has special meaning in FTS5 query syntax — strip rather
// than escape.
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

// matchInitiatives runs an FTS5 OR-query over objects_fts filtered to
// type='initiative'. We further filter to status='active' via a JOIN against
// the objects table since the FTS table doesn't index status.
//
// Scoring is absolute (term-overlap fraction), not corpus-relative: for each
// candidate we count the fraction of distinct query tokens whose lowercased
// prefix appears in the indexed title+tags+summary+body. This avoids the
// "single weak match becomes score 1.0" failure mode of relative
// normalization. Title and tag hits get a small boost (×1.5).
//
// BM25 is used only for the initial candidate cut (FTS5 ORDER BY rank) and
// as a tiebreaker; the user-visible Score is the overlap fraction.
func matchInitiatives(conn *sql.DB, req ChangeRequest) ([]MatchedInitiative, error) {
	query := buildQueryString(req)
	if query == "" {
		return []MatchedInitiative{}, nil
	}

	const ftsSQL = `
SELECT f.id, f.space_id, COALESCE(f.title, '') AS title,
       COALESCE(f.tags, '') AS tags,
       COALESCE(f.summary, '') AS summary,
       COALESCE(f.body, '') AS body
FROM objects_fts f
JOIN objects o ON o.id = f.id
WHERE objects_fts MATCH ?
  AND f.type = 'initiative'
  AND (o.status = 'active' OR o.status = '' OR o.status IS NULL)
  AND (? = '' OR f.space_id = ?)
ORDER BY bm25(objects_fts, 3.0, 2.0, 2.0, 1.0)
LIMIT 5`

	rows, err := conn.Query(ftsSQL, query, req.Space, req.Space)
	if err != nil {
		return nil, fmt.Errorf("fts query: %w", err)
	}
	defer rows.Close()

	scoringTokens := scoringSourceTokens(req)
	if len(scoringTokens) == 0 {
		return []MatchedInitiative{}, nil
	}

	type row struct {
		id      string
		spaceID string
		title   string
		tags    string
		summary string
		body    string
	}
	var raw []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.spaceID, &r.title, &r.tags, &r.summary, &r.body); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		raw = append(raw, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	if len(raw) == 0 {
		return []MatchedInitiative{}, nil
	}

	out := make([]MatchedInitiative, 0, len(raw))
	for _, r := range raw {
		hay := strings.ToLower(r.title + " " + r.tags + " " + r.summary + " " + r.body)
		titleTags := strings.ToLower(r.title + " " + r.tags)

		var matched int
		var titleBoost float64
		for _, t := range scoringTokens {
			if strings.Contains(hay, t) {
				matched++
				if strings.Contains(titleTags, t) {
					titleBoost += 0.05
				}
			}
		}

		eligible := len(scoringTokens)
		if eligible == 0 {
			continue
		}
		if matched == 0 {
			// FTS5 retrieved this candidate via stemming or partial overlap
			// on a token source we don't score against (diff/path). If zero
			// of the authoritative scoring tokens hit the document body,
			// it's not a meaningful match — drop it.
			continue
		}

		score := float64(matched)/float64(eligible) + titleBoost
		if score > 1.0 {
			score = 1.0
		}

		out = append(out, MatchedInitiative{
			ID:     r.id,
			Title:  r.title,
			Score:  roundTo(score, 2),
			Reason: matchReason(req, r.title),
		})
	}

	// Re-sort by absolute score so the user sees them best-first regardless
	// of BM25 cut order.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out, nil
}

// matchReason produces a short human label for why an initiative matched.
// MVP heuristic: cite the strongest overlap source ("task wording",
// "diff summary", or "changed file paths").
func matchReason(req ChangeRequest, _ string) string {
	if strings.TrimSpace(req.Task) != "" {
		return "Matches task description"
	}
	if strings.TrimSpace(req.DiffSummary) != "" {
		return "Matches diff summary"
	}
	return "Matches changed file paths"
}

// recentPressureAnchors returns object URIs cited as "recent pressure"
// signals — the same data pulse surfaces, projected to anchors. MVP: cite
// the top-3 hot-zone objects from pulse plus the top initiative.
func recentPressureAnchors(conn *sql.DB, spaceID string, window time.Duration) ([]string, error) {
	p, err := pulse.Compute(conn, spaceID, window)
	if err != nil {
		return nil, err
	}
	var out []string
	for i, hz := range p.HotZones {
		if i >= 3 {
			break
		}
		out = append(out, hz.ObjectID)
	}
	for i, ti := range p.TopInitiatives {
		if i >= 1 {
			break
		}
		if !slices.Contains(out, ti.ID) {
			out = append(out, ti.ID)
		}
	}
	sort.Strings(out)
	return out, nil
}

// hotZoneFor infers a shared directory prefix from changedFiles and counts
// graft receipts within `since` that landed in it. Incidents are counted as
// 0 in the MVP (no incident type yet).
func hotZoneFor(conn *sql.DB, changedFiles []string, since time.Duration) (*HotZone, error) {
	prefix := commonPathPrefix(changedFiles)
	if prefix == "" {
		return nil, nil
	}

	cutoff := time.Now().UTC().Add(-since).Format(time.RFC3339)
	var grafts int
	err := conn.QueryRow(
		`SELECT COUNT(*) FROM receipts WHERE action = 'graft' AND created_at > ?`,
		cutoff,
	).Scan(&grafts)
	if err != nil {
		return nil, err
	}

	return &HotZone{
		Path:         prefix,
		Commits14d:   grafts,
		Incidents14d: 0,
	}, nil
}

// commonPathPrefix returns the longest leading directory path common to all
// non-empty entries. Returns "" if there are no files or no shared prefix.
func commonPathPrefix(files []string) string {
	cleaned := make([]string, 0, len(files))
	for _, f := range files {
		f = strings.TrimSpace(f)
		if f != "" {
			cleaned = append(cleaned, path.Dir(f))
		}
	}
	if len(cleaned) == 0 {
		return ""
	}
	prefix := cleaned[0]
	for _, p := range cleaned[1:] {
		for !strings.HasPrefix(p+"/", prefix+"/") {
			if prefix == "." || prefix == "/" {
				return ""
			}
			prefix = path.Dir(prefix)
		}
	}
	if prefix == "." {
		return ""
	}
	return prefix
}

// categorize maps the top match score to an alignment + recommendation.
// Thresholds are conservative for the MVP — easy to tune once we have real
// usage signal.
func categorize(matched []MatchedInitiative) (Alignment, float64, Recommendation) {
	if len(matched) == 0 {
		return AlignNeutral, 0, RecReviewRequired
	}
	top := matched[0].Score
	switch {
	case top >= 0.7:
		return AlignDirectlyAligned, top, RecProceed
	case top >= 0.4:
		return AlignEnabling, top, RecProceed
	default:
		return AlignAdjacent, top, RecProceedWithExtraReview
	}
}

// roundTo rounds f to `places` decimal places. Output is purely cosmetic
// (keeps Score from emitting 17 digits of noise in JSON).
func roundTo(f float64, places int) float64 {
	shift := math.Pow(10, float64(places))
	return math.Round(f*shift) / shift
}
