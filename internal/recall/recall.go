// Package recall implements the Hyphae read path: FTS5-powered lookup
// with token-budgeted responses for agent consumption.
package recall

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/odvcencio/hyphae/internal/types"
)

// Anchor is the recall-response form of a citation.
type Anchor struct {
	URI        string  `json:"uri"`
	Title      string  `json:"title"`
	TokensFull int     `json:"tokens_full"` // estimated tokens in full doc
	Score      float64 `json:"score"`       // BM25 rank score (lower = better per FTS5)
}

// Response is the v0.1 recall response.
type Response struct {
	Summary    string              `json:"summary"`
	Anchors    []Anchor            `json:"anchors"`
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

// sanitizeQuery strips FTS5 operator characters and returns a plain-term query.
// For v0.1 we keep it simple: strip `"`, `*`, `(`, `)`, `:`, `-`, `^` then
// collapse whitespace. Multiple words become an implicit-AND query via FTS5.
func sanitizeQuery(q string) string {
	replacer := strings.NewReplacer(
		`"`, " ",
		`*`, " ",
		`(`, " ",
		`)`, " ",
		`:`, " ",
		`-`, " ",
		`^`, " ",
	)
	cleaned := replacer.Replace(q)
	// Collapse runs of whitespace into a single space.
	fields := strings.Fields(cleaned)
	return strings.Join(fields, " ")
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
// limit is the max anchor count to consider before token budgeting.
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
		// CitedSpans: treat like SummaryAnchors for v0.1 — anchors without full bodies.
		// Fall through to normal processing.
	}

	sanitized := sanitizeQuery(query)

	// No query terms after sanitization — return empty.
	if sanitized == "" {
		resp := Response{
			Summary:    fmt.Sprintf("No matches found for: %s", query),
			Anchors:    []Anchor{},
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
SELECT id, type, space_id, title, summary,
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
		bodyLen int64
		rank    float64
	}

	var results []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.typ, &r.spaceID, &r.title, &r.summary, &r.bodyLen, &r.rank); err != nil {
			return Response{}, fmt.Errorf("recall: scan row: %w", err)
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return Response{}, fmt.Errorf("recall: rows error: %w", err)
	}

	// Build anchors.
	anchors := make([]Anchor, 0, len(results))
	for _, r := range results {
		anchors = append(anchors, Anchor{
			URI:        objectURI(r.spaceID, r.id),
			Title:      r.title,
			TokensFull: int(r.bodyLen) / 4,
			Score:      r.rank,
		})
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

	// Shape-specific anchor caps.
	maxAnchors := 10
	switch budget.Shape {
	case types.ShapeHeadline:
		maxAnchors = 1
	case types.ShapeCountOnly:
		maxAnchors = 0
	}

	if len(anchors) > maxAnchors {
		anchors = anchors[:maxAnchors]
	}

	// Token-budget trim: drop anchors from the end until under budget.
	// Always keep at least 1 anchor if any matched.
	computeTokensUsed := func(s string, aa []Anchor) int {
		total := len(s)
		for _, a := range aa {
			total += len(a.URI) + len(a.Title)
		}
		return (total + 3) / 4
	}

	for len(anchors) > 1 {
		used := computeTokensUsed(summary, anchors)
		if used <= budget.MaxResponseTokens {
			break
		}
		anchors = anchors[:len(anchors)-1]
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
		Anchors:    anchors,
		TokensUsed: computeTokensUsed(summary, anchors),
		Query:      query,
		Shape:      budget.Shape,
	}

	return resp, nil
}
