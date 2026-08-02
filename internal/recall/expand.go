package recall

import (
	"database/sql"
	"sort"

	"m31labs.dev/hyphae/internal/types"
)

// Lazy graph recall (initiative.graph-native-harness, move G-B): BM25 stays
// the seeder, then recall expands one to two hops over typed edges that
// already exist in the index — wikilink, derived_from, supports — at query
// time, before ranking and token budgeting. No new index and no batch
// summarization; the LazyGraphRAG result is that deferring graph work to
// query time keeps the quality win at a fraction of the index cost.
const (
	// maxExpandSeeds bounds how many top BM25 seeds get their
	// neighborhoods walked.
	maxExpandSeeds = 5
	// maxExpandHops is the walk depth. Hop two runs only from the nodes
	// hop one discovered.
	maxExpandHops = 2
	// maxExpandFrontier bounds the hop-two frontier so a hub node with
	// hundreds of backlinks cannot fan the walk out.
	maxExpandFrontier = 8
	// maxExpandAdded bounds how many neighbor documents join the
	// candidate set. The token budget still applies after ranking.
	maxExpandAdded = 6
	// maxEdgesPerNode bounds the per-node edge fetch.
	maxEdgesPerNode = 64
)

// expandEdgeKinds are the typed edges the walk follows, in both
// directions. These three carry the semantic weight of the graph today;
// source_ref and related stay out because they are high-volume and low
// precision.
var expandEdgeKinds = []types.EdgeKind{
	types.EdgeWikilink,
	types.EdgeDerivedFrom,
	types.EdgeSupports,
}

// edgeKindAttenuation is the fraction of a seed's BM25 rank a neighbor
// inherits per hop, by edge kind. FTS5 bm25 scores are negative and lower
// is better, so multiplying by a factor in (0,1) always ranks a neighbor
// behind its own seed while keeping a strong seed's neighborhood ahead of
// weak direct matches. derived_from is the strongest signal (provenance),
// supports next (evidence), wikilink last (association).
var edgeKindAttenuation = map[types.EdgeKind]float64{
	types.EdgeDerivedFrom: 0.7,
	types.EdgeSupports:    0.6,
	types.EdgeWikilink:    0.5,
}

// expandCandidate is one neighbor discovered by the walk, carrying the
// best (most negative) attenuated rank found for it and the provenance
// used for the Hit.Via field.
type expandCandidate struct {
	id     string
	rank   float64
	kind   types.EdgeKind
	seedID string
}

// expandRows walks typed edges out from the top BM25 seeds and returns
// neighbor documents as extra candidate rows, ranked by attenuated seed
// score. Neighbors already in the seed set are skipped; endpoints that do
// not resolve to an indexed document (external URIs, unindexed ids) drop
// out at fetch time. A walk error degrades to no expansion rather than
// failing recall: the seeds alone are always a valid response.
func expandRows(conn *sql.DB, seeds []ftsRow) []ftsRow {
	if len(seeds) == 0 {
		return nil
	}

	inResults := make(map[string]bool, len(seeds))
	for _, s := range seeds {
		inResults[s.id] = true
	}

	frontier := seeds
	if len(frontier) > maxExpandSeeds {
		frontier = frontier[:maxExpandSeeds]
	}
	scores := make(map[string]expandCandidate)
	frontierScores := make(map[string]float64, len(frontier))
	frontierIDs := make([]string, 0, len(frontier))
	for _, s := range frontier {
		frontierScores[s.id] = s.rank
		frontierIDs = append(frontierIDs, s.id)
	}

	for hop := 1; hop <= maxExpandHops; hop++ {
		var nextIDs []string
		nextScores := make(map[string]float64)
		for _, nodeID := range frontierIDs {
			neighbors, err := neighborEdges(conn, nodeID)
			if err != nil {
				return nil
			}
			for _, n := range neighbors {
				if inResults[n.id] {
					continue
				}
				rank := frontierScores[nodeID] * edgeKindAttenuation[n.kind]
				if existing, seen := scores[n.id]; !seen || rank < existing.rank {
					scores[n.id] = expandCandidate{id: n.id, rank: rank, kind: n.kind, seedID: nodeID}
				}
				if queuedRank, queued := nextScores[n.id]; queued {
					if rank < queuedRank {
						nextScores[n.id] = rank
					}
				} else if len(nextIDs) < maxExpandFrontier {
					nextScores[n.id] = rank
					nextIDs = append(nextIDs, n.id)
				}
			}
		}
		frontierIDs = nextIDs
		frontierScores = nextScores
	}

	if len(scores) == 0 {
		return nil
	}

	ordered := make([]expandCandidate, 0, len(scores))
	for _, c := range scores {
		ordered = append(ordered, c)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].rank != ordered[j].rank {
			return ordered[i].rank < ordered[j].rank
		}
		return ordered[i].id < ordered[j].id
	})

	out := make([]ftsRow, 0, maxExpandAdded)
	for _, c := range ordered {
		if len(out) >= maxExpandAdded {
			break
		}
		row, ok := fetchIndexedRow(conn, c.id)
		if !ok {
			continue
		}
		row.rank = c.rank
		row.via = string(c.kind) + " from " + c.seedID
		out = append(out, row)
	}
	return out
}

// neighborEdge is one typed edge endpoint seen from a node: the other end
// plus the edge kind.
type neighborEdge struct {
	id   string
	kind types.EdgeKind
}

const expandEdgeSQL = `
SELECT kind, src_id, dst_id FROM edges
WHERE (src_id = ? OR dst_id = ?) AND kind IN (?, ?, ?)
LIMIT ?`

// neighborEdges returns the typed-edge neighbors of nodeID in both
// directions, restricted to expandEdgeKinds.
func neighborEdges(conn *sql.DB, nodeID string) ([]neighborEdge, error) {
	rows, err := conn.Query(expandEdgeSQL, nodeID, nodeID,
		string(expandEdgeKinds[0]), string(expandEdgeKinds[1]), string(expandEdgeKinds[2]),
		maxEdgesPerNode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []neighborEdge
	for rows.Next() {
		var kind, src, dst string
		if err := rows.Scan(&kind, &src, &dst); err != nil {
			return nil, err
		}
		other := dst
		if dst == nodeID {
			other = src
		}
		if other == nodeID {
			continue
		}
		out = append(out, neighborEdge{id: other, kind: types.EdgeKind(kind)})
	}
	return out, rows.Err()
}

const expandFetchSQL = `
SELECT id, type, space_id, title, summary, body, length(body)
FROM objects_fts WHERE id = ?`

// fetchIndexedRow loads one indexed document by id. ok is false when the
// id is not in the index (an external URI endpoint, or a stale edge).
func fetchIndexedRow(conn *sql.DB, id string) (ftsRow, bool) {
	var r ftsRow
	err := conn.QueryRow(expandFetchSQL, id).Scan(
		&r.id, &r.typ, &r.spaceID, &r.title, &r.summary, &r.body, &r.bodyLen)
	if err != nil {
		return ftsRow{}, false
	}
	return r, true
}
