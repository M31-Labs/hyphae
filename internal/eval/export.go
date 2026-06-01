package eval

import (
	"database/sql"
	"fmt"
	"strings"
)

const exportSQL = `
SELECT id, title, tags, summary, body
FROM objects_fts
WHERE space_id = ?`

// ExportCorpus reads every indexed object for one space and flattens it into a
// BEIR corpus doc. spaceID must be the BARE space id (e.g. "m31labs/hyphae",
// not the "hypha://..." URI).
//
// Title is carried in the BEIR title field; Text concatenates the remaining
// indexed fields (tags, summary, body) — the faithful single-field
// representation manta (R2/R3) consumes, since manta cannot replicate hyphae's
// per-column BM25 weights. The production retriever (R1) does NOT use this
// export; it queries the live weighted index via recall.RankIDs.
func ExportCorpus(conn *sql.DB, spaceID string) ([]CorpusDoc, error) {
	rows, err := conn.Query(exportSQL, spaceID)
	if err != nil {
		return nil, fmt.Errorf("eval: export query: %w", err)
	}
	defer rows.Close()

	var out []CorpusDoc
	for rows.Next() {
		var id, title, tags, summary, body string
		if err := rows.Scan(&id, &title, &tags, &summary, &body); err != nil {
			return nil, fmt.Errorf("eval: export scan: %w", err)
		}
		parts := make([]string, 0, 3)
		for _, p := range []string{tags, summary, body} {
			if s := strings.TrimSpace(p); s != "" {
				parts = append(parts, s)
			}
		}
		out = append(out, CorpusDoc{
			ID:    id,
			Title: title,
			Text:  strings.Join(parts, "\n"),
		})
	}
	return out, rows.Err()
}
