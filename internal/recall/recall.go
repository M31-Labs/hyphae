// Package recall implements the Hyphae read path: FTS5-powered lookup
// with token-budgeted responses for agent consumption.
package recall

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"m31labs.dev/hyphae/internal/types"
)

// Hit is one ranked recall result. Snippets are short excerpts from the
// matched document with anchor + line citations.
type Hit struct {
	URI        string    `json:"uri"`
	Title      string    `json:"title"`
	TokensFull int       `json:"tokens_full"` // estimated tokens in full doc
	Score      float64   `json:"score"`       // BM25 rank score (lower = better per FTS5)
	Snippets   []Snippet `json:"snippets,omitempty"`
}

// Snippet is a short excerpt of a Hit's body around matched query terms,
// paired with a Citation pointing back into the source document.
type Snippet struct {
	Text     string   `json:"text"`
	Citation Citation `json:"citation"`
}

// Citation locates a Snippet inside the source document. Anchor is the
// nearest preceding heading as a `hypha://...#slug` URI; Line and EndLine
// are 1-indexed positions inside the indexed body (not the file — the
// frontmatter is excluded from the index).
type Citation struct {
	Anchor  string `json:"anchor,omitempty"`
	Line    int    `json:"line"`
	EndLine int    `json:"end_line,omitempty"`
}

// Response is the v0.2 recall response. Anchors was renamed to Hits in
// v0.1.9 when snippets+citations landed; the old name was a collision with
// mdpp's per-heading anchor IDs.
type Response struct {
	Summary    string              `json:"summary"`
	Hits       []Hit               `json:"hits"`
	TokensUsed int                 `json:"tokens_used"`
	Query      string              `json:"query"`
	Shape      types.ResponseShape `json:"shape"`
}

// Index inserts or updates one object in the FTS5 table. Idempotent —
// it deletes any existing row with the same id first.
func Index(conn *sql.DB, obj types.Object) error {
	tx, err := conn.Begin()
	if err != nil {
		return fmt.Errorf("recall: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if err := indexInTx(tx, obj); err != nil {
		return err
	}
	return tx.Commit()
}

// IndexBatch indexes many objects in one transaction.
func IndexBatch(conn *sql.DB, objs []types.Object) error {
	tx, err := conn.Begin()
	if err != nil {
		return fmt.Errorf("recall: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	for _, obj := range objs {
		if err := indexInTx(tx, obj); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func indexInTx(tx *sql.Tx, obj types.Object) error {
	// Delete existing row so the upsert is idempotent.
	if _, err := tx.Exec(`DELETE FROM objects_fts WHERE id = ?`, obj.ID); err != nil {
		return fmt.Errorf("recall: delete existing fts row %q: %w", obj.ID, err)
	}

	tags := strings.Join(obj.Tags, " ")
	_, err := tx.Exec(
		`INSERT INTO objects_fts(id, type, space_id, title, tags, summary, body)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		obj.ID, string(obj.Type), obj.SpaceID,
		obj.Title, tags, obj.Summary, obj.Body,
	)
	if err != nil {
		return fmt.Errorf("recall: insert fts row %q: %w", obj.ID, err)
	}
	return nil
}

// sanitizeQuery reduces the query to ASCII alphanumerics and spaces so every
// remaining token is a plain FTS5 term. All FTS5 syntax characters (`.`,
// `,`, `:`, `-`, `*`, `(`, `)`, etc.) are special and silently break the
// query — strip rather than escape. Multiple words become an implicit-AND
// query via FTS5.
func sanitizeQuery(q string) string {
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

// estimateTokens returns a rough token count for a string using the len/4 heuristic.
func estimateTokens(s string) int {
	return (len(s) + 3) / 4 // ceiling division
}

// objectURI constructs a hypha:// URI for an object.
func objectURI(spaceID, id string) string {
	return fmt.Sprintf("hypha://%s/object/%s", spaceID, id)
}

// Recall runs an FTS5 query and returns a budgeted Response.
// If budget.Shape is empty, defaults to summary+anchors.
// If budget.MaxResponseTokens is 0, defaults to 800.
// limit is the max hit count to consider before token budgeting.
func Recall(conn *sql.DB, query string, limit int, budget types.Budget) (Response, error) {
	// Apply defaults.
	if budget.Shape == "" {
		budget.Shape = types.ShapeSummaryAnchors
	}
	if budget.MaxResponseTokens == 0 {
		budget.MaxResponseTokens = 800
	}
	if limit <= 0 {
		limit = 20
	}

	// Validate shape.
	switch budget.Shape {
	case types.ShapeFullDocuments:
		return Response{}, errors.New("recall: shape not supported in v0.1: full_documents")
	case types.ShapeCitedSpans:
		// CitedSpans now naturally maps to hits+snippets+citations; fall through.
	}

	sanitized := sanitizeQuery(query)

	// No query terms after sanitization — return empty.
	if sanitized == "" {
		resp := Response{
			Summary:    fmt.Sprintf("No matches found for: %s", query),
			Hits:       []Hit{},
			TokensUsed: 0,
			Query:      query,
			Shape:      budget.Shape,
		}
		resp.TokensUsed = estimateTokens(resp.Summary)
		return resp, nil
	}

	// BM25 column weights: title=3.0, tags=2.0, summary=2.0, body=1.0
	// Column order matches CREATE VIRTUAL TABLE: id, type, space_id, title, tags, summary, body
	// UNINDEXED columns count as weight 0 but still consume a position.
	// bm25() weights are: (table, w_col0, w_col1, ...) where col0 is the first
	// column in the FTS table definition. UNINDEXED columns are skipped in the
	// weight list — only indexed columns receive weights.
	//
	// Note: modernc.org/sqlite FTS5 bm25() accepts one weight per *indexed*
	// column only. Our indexed columns in order: title, tags, summary, body (4 total).
	const ftsSQL = `
SELECT id, type, space_id, title, summary, body,
       length(body) AS body_len,
       bm25(objects_fts, 3.0, 2.0, 2.0, 1.0) AS rank
FROM objects_fts
WHERE objects_fts MATCH ?
ORDER BY rank
LIMIT ?`

	rows, err := conn.Query(ftsSQL, sanitized, limit)
	if err != nil {
		// FTS5 MATCH returns an error on empty results in some drivers; also
		// catches syntax errors.
		return Response{}, fmt.Errorf("recall: fts query: %w", err)
	}
	defer rows.Close()

	type row struct {
		id      string
		typ     string
		spaceID string
		title   string
		summary string
		body    string
		bodyLen int64
		rank    float64
	}

	var results []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.typ, &r.spaceID, &r.title, &r.summary, &r.body, &r.bodyLen, &r.rank); err != nil {
			return Response{}, fmt.Errorf("recall: scan row: %w", err)
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return Response{}, fmt.Errorf("recall: rows error: %w", err)
	}

	terms := strings.Fields(sanitized)

	// Per-hit snippet budget shrinks with hit-count to stay near the
	// overall token target. With ~4-5 hits and ~3 snippets each, the
	// response lands in the 700-1100 token range.
	maxSnippetsPerHit := 3
	switch budget.Shape {
	case types.ShapeHeadline:
		maxSnippetsPerHit = 0
	case types.ShapeCountOnly:
		maxSnippetsPerHit = 0
	}

	hits := make([]Hit, 0, len(results))
	for _, r := range results {
		h := Hit{
			URI:        objectURI(r.spaceID, r.id),
			Title:      r.title,
			TokensFull: int(r.bodyLen) / 4,
			Score:      r.rank,
		}
		if maxSnippetsPerHit > 0 {
			h.Snippets = extractSnippets(r.body, terms, h.URI, maxSnippetsPerHit)
		}
		hits = append(hits, h)
	}

	// Build summary text.
	var summary string
	if len(results) == 0 {
		summary = fmt.Sprintf("No matches found for: %s", query)
	} else {
		// Collect top-3 titles for the summary sentence.
		topN := 3
		if len(results) < topN {
			topN = len(results)
		}
		titles := make([]string, topN)
		for i := 0; i < topN; i++ {
			titles[i] = results[i].title
		}
		spaceID := results[0].spaceID
		summary = fmt.Sprintf("Found %d matches in %s. Top: %s.",
			len(results), spaceID, strings.Join(titles, "; "))
	}

	// Shape-specific hit caps.
	maxHits := 10
	switch budget.Shape {
	case types.ShapeHeadline:
		maxHits = 1
	case types.ShapeCountOnly:
		maxHits = 0
	}

	if len(hits) > maxHits {
		hits = hits[:maxHits]
	}

	// Token-budget trim: first drop snippets from the tail; if still over,
	// drop entire hits. Always keep at least one hit if any matched.
	computeTokensUsed := func(s string, hh []Hit) int {
		total := len(s)
		for _, h := range hh {
			total += len(h.URI) + len(h.Title)
			for _, sn := range h.Snippets {
				total += len(sn.Text) + len(sn.Citation.Anchor)
			}
		}
		return (total + 3) / 4
	}

	// First pass: drop trailing snippets when over budget.
	for computeTokensUsed(summary, hits) > budget.MaxResponseTokens {
		dropped := false
		for i := len(hits) - 1; i >= 0; i-- {
			if len(hits[i].Snippets) > 0 {
				hits[i].Snippets = hits[i].Snippets[:len(hits[i].Snippets)-1]
				dropped = true
				break
			}
		}
		if !dropped {
			break
		}
	}

	// Second pass: drop trailing hits if still over budget.
	for len(hits) > 1 {
		used := computeTokensUsed(summary, hits)
		if used <= budget.MaxResponseTokens {
			break
		}
		hits = hits[:len(hits)-1]
	}

	// Headline shape: cap summary at ~100 tokens.
	if budget.Shape == types.ShapeHeadline {
		const maxSummaryTokens = 100
		if estimateTokens(summary) > maxSummaryTokens {
			// Truncate to 400 bytes (≈100 tokens), trim to last space.
			cutAt := 400
			if len(summary) < cutAt {
				cutAt = len(summary)
			}
			truncated := summary[:cutAt]
			if idx := strings.LastIndex(truncated, " "); idx > 0 {
				truncated = truncated[:idx]
			}
			summary = truncated + "…"
		}
	}

	resp := Response{
		Summary:    summary,
		Hits:       hits,
		TokensUsed: computeTokensUsed(summary, hits),
		Query:      query,
		Shape:      budget.Shape,
	}

	return resp, nil
}

// extractSnippets finds up to maxSnippets short excerpts of body around
// case-insensitive matches of the given query terms. Matches are clustered
// (within ~200 chars) into windows; densest clusters win, ties broken by
// document order. Each snippet carries a Citation pointing at the nearest
// preceding markdown heading (anchor) and the 1-indexed line range within
// body. Returns nil when there is nothing meaningful to extract.
func extractSnippets(body string, terms []string, docURI string, maxSnippets int) []Snippet {
	if body == "" || len(terms) == 0 || maxSnippets <= 0 {
		return nil
	}

	lower := strings.ToLower(body)
	type pos struct{ start, end int }
	var positions []pos
	for _, t := range terms {
		tl := strings.ToLower(t)
		if tl == "" {
			continue
		}
		from := 0
		for from < len(lower) {
			i := strings.Index(lower[from:], tl)
			if i < 0 {
				break
			}
			absStart := from + i
			positions = append(positions, pos{absStart, absStart + len(tl)})
			from = absStart + len(tl)
		}
	}
	if len(positions) == 0 {
		return nil
	}

	sort.Slice(positions, func(i, j int) bool { return positions[i].start < positions[j].start })

	const clusterGap = 200
	type cluster struct {
		start, end, hits int
	}
	clusters := []cluster{{positions[0].start, positions[0].end, 1}}
	for _, p := range positions[1:] {
		last := &clusters[len(clusters)-1]
		if p.start-last.end < clusterGap {
			if p.end > last.end {
				last.end = p.end
			}
			last.hits++
		} else {
			clusters = append(clusters, cluster{p.start, p.end, 1})
		}
	}

	// Rank by hit density, ties broken by document order.
	sort.SliceStable(clusters, func(i, j int) bool {
		if clusters[i].hits != clusters[j].hits {
			return clusters[i].hits > clusters[j].hits
		}
		return clusters[i].start < clusters[j].start
	})

	if len(clusters) > maxSnippets {
		clusters = clusters[:maxSnippets]
	}

	// Re-sort by document order for a stable, readable response.
	sort.Slice(clusters, func(i, j int) bool { return clusters[i].start < clusters[j].start })

	const pad = 80
	out := make([]Snippet, 0, len(clusters))
	for _, c := range clusters {
		winStart := c.start - pad
		if winStart < 0 {
			winStart = 0
		}
		winEnd := c.end + pad
		if winEnd > len(body) {
			winEnd = len(body)
		}

		// Snap to word boundary on the left (move forward to next space/newline).
		for winStart > 0 && winStart < c.start && body[winStart] != ' ' && body[winStart] != '\n' {
			winStart++
		}
		// Snap to word boundary on the right (move forward to next space/newline).
		for winEnd < len(body) && body[winEnd] != ' ' && body[winEnd] != '\n' {
			winEnd++
		}

		text := strings.TrimSpace(body[winStart:winEnd])
		text = strings.Join(strings.Fields(text), " ")
		if text == "" {
			continue
		}
		if winStart > 0 {
			text = "…" + text
		}
		if winEnd < len(body) {
			text = text + "…"
		}

		anchor, line, endLine := citationFor(body, winStart, winEnd, docURI)
		out = append(out, Snippet{
			Text:     text,
			Citation: Citation{Anchor: anchor, Line: line, EndLine: endLine},
		})
	}
	return out
}

// citationFor locates the snippet window inside body: 1-indexed line range
// plus the nearest preceding markdown heading turned into an anchor URI.
// Headings are recognized as lines starting with `#` (any level). When no
// heading precedes the window, the citation has no anchor fragment.
func citationFor(body string, winStart, winEnd int, docURI string) (string, int, int) {
	line := 1 + strings.Count(body[:winStart], "\n")
	endLine := 1 + strings.Count(body[:winEnd], "\n")

	// Walk backward through line starts looking for the most recent heading.
	var headingSlug string
	cursor := winStart
	for cursor > 0 {
		nl := strings.LastIndex(body[:cursor], "\n")
		var lineStart int
		if nl < 0 {
			lineStart = 0
		} else {
			lineStart = nl + 1
		}
		lineEnd := lineStart
		if nlEnd := strings.Index(body[lineStart:], "\n"); nlEnd >= 0 {
			lineEnd = lineStart + nlEnd
		} else {
			lineEnd = len(body)
		}
		trimmed := strings.TrimSpace(body[lineStart:lineEnd])
		if strings.HasPrefix(trimmed, "#") {
			text := strings.TrimLeft(trimmed, "#")
			text = strings.TrimSpace(text)
			headingSlug = slugify(text)
			break
		}
		if nl < 0 {
			break
		}
		cursor = nl
	}

	anchor := docURI
	if headingSlug != "" {
		anchor = docURI + "#" + headingSlug
	}
	return anchor, line, endLine
}

// slugify produces a lowercase-hyphenated slug from a heading text. Matches
// the mdpp anchor-slug rule: keep alphanumerics, collapse other runs to a
// single hyphen, trim trailing hyphens.
func slugify(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevHyphen := true
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			prevHyphen = false
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
			prevHyphen = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			prevHyphen = false
		default:
			if !prevHyphen {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	result := b.String()
	return strings.TrimRight(result, "-")
}
