// Package graph implements the query layer over the Hyphae edges table.
//
// It answers questions like: "what cites this concept?", "what is
// concept.federation related to?", and "trace from concept.spore back to the
// lessons it derived from."
//
// All functions accept a *sql.DB opened via db.Open. They perform only reads.
package graph

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"m31labs.dev/hyphae/internal/types"
)

const defaultLimit = 50

// Neighbor is an edge endpoint plus the object it points to (or minimal info
// if the endpoint isn't in the objects table — e.g. an external URI).
type Neighbor struct {
	Edge     types.Edge `json:"edge"`
	Endpoint string     `json:"endpoint"`        // the src or dst that's "the other end"
	Title    string     `json:"title,omitempty"` // object title if found
	Type     string     `json:"type,omitempty"`  // object type if found
	Space    string     `json:"space,omitempty"` // object space_id if found
}

// TraceStep is one hop in a derivation/citation chain.
type TraceStep struct {
	Edge     types.Edge
	From     string
	To       string
	HopDepth int
}

// Backlinks returns every edge pointing AT objectID (dst_id = objectID).
// kinds is an optional filter; empty means all kinds.
// If limit is 0, defaults to 50.
func Backlinks(conn *sql.DB, objectID string, kinds []types.EdgeKind, limit int) ([]Neighbor, error) {
	if limit <= 0 {
		limit = defaultLimit
	}

	query, args := buildEdgeQuery(
		`SELECT e.id, e.kind, e.src_id, e.dst_id, e.confidence, e.derivation,
		        e.agent_source, e.created_by, e.created_at, e.metadata_json,
		        o.title, o.type, o.space_id
		 FROM edges e
		 LEFT JOIN objects o ON o.id = e.src_id
		 WHERE e.dst_id = ?`,
		objectID, kinds, limit,
	)

	rows, err := conn.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("graph: backlinks query: %w", err)
	}
	defer rows.Close()

	return scanNeighbors(rows, false)
}

// ForwardLinks returns every edge originating FROM objectID (src_id = objectID).
// kinds is an optional filter; empty means all kinds.
// If limit is 0, defaults to 50.
func ForwardLinks(conn *sql.DB, objectID string, kinds []types.EdgeKind, limit int) ([]Neighbor, error) {
	if limit <= 0 {
		limit = defaultLimit
	}

	query, args := buildEdgeQuery(
		`SELECT e.id, e.kind, e.src_id, e.dst_id, e.confidence, e.derivation,
		        e.agent_source, e.created_by, e.created_at, e.metadata_json,
		        o.title, o.type, o.space_id
		 FROM edges e
		 LEFT JOIN objects o ON o.id = e.dst_id
		 WHERE e.src_id = ?`,
		objectID, kinds, limit,
	)

	rows, err := conn.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("graph: forward links query: %w", err)
	}
	defer rows.Close()

	return scanNeighbors(rows, true)
}

// Related returns the union of Backlinks + ForwardLinks, deduped on
// (kind, other-endpoint). Each unique (kind, endpoint) pair appears once.
// Callers should note: two edges of different kinds to the same endpoint are
// NOT considered duplicates and both appear in the result.
func Related(conn *sql.DB, objectID string, kinds []types.EdgeKind, limit int) ([]Neighbor, error) {
	if limit <= 0 {
		limit = defaultLimit
	}

	fwd, err := ForwardLinks(conn, objectID, kinds, limit)
	if err != nil {
		return nil, fmt.Errorf("graph: related (forward): %w", err)
	}

	back, err := Backlinks(conn, objectID, kinds, limit)
	if err != nil {
		return nil, fmt.Errorf("graph: related (back): %w", err)
	}

	// Dedupe on (kind, endpoint). We keep the first occurrence of each pair.
	type key struct {
		kind     types.EdgeKind
		endpoint string
	}
	seen := make(map[key]bool)
	out := make([]Neighbor, 0, len(fwd)+len(back))

	for _, n := range append(fwd, back...) {
		k := key{n.Edge.Kind, n.Endpoint}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, n)
		if len(out) >= limit {
			break
		}
	}

	return out, nil
}

// Trace walks edges of the given kind(s) starting at startID, following
// src → dst ("trace from concept.spore back to the lessons it derived from").
// For derived_from edges, src=X means X was derived from dst=Y; Trace follows
// these forward to find the chain of provenance: start→parent→grandparent.
// Useful as Trace(conn, "concept.x", []EdgeKind{EdgeDerivedFrom, EdgeCites, EdgeSourceRef}, 4).
//
// Returns steps in BFS order, deduped on edge id, cycle-safe.
// Cycle safety is enforced by tracking visited edge IDs, not node IDs.
// This means a graph can have multiple edges between the same pair of nodes
// via different edge instances, and each edge is traversed at most once.
// Infinite loops are impossible because each edge can be visited only once
// and the graph has finitely many edges.
func Trace(conn *sql.DB, startID string, kinds []types.EdgeKind, maxDepth int) ([]TraceStep, error) {
	if maxDepth <= 0 {
		maxDepth = 4
	}

	type queueItem struct {
		nodeID   string
		hopDepth int
	}

	visited := make(map[string]bool) // keyed on edge id
	queue := []queueItem{{nodeID: startID, hopDepth: 0}}
	var result []TraceStep

	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]

		if item.hopDepth >= maxDepth {
			continue
		}

		// Find edges where src_id = item.nodeID (i.e. edges leaving this node).
		edges, err := queryEdgesBySrc(conn, item.nodeID, kinds)
		if err != nil {
			return nil, fmt.Errorf("graph: trace hop %d at %q: %w", item.hopDepth, item.nodeID, err)
		}

		for _, e := range edges {
			if visited[e.ID] {
				continue
			}
			visited[e.ID] = true

			step := TraceStep{
				Edge:     e,
				From:     item.nodeID,
				To:       e.DstID,
				HopDepth: item.hopDepth + 1,
			}
			result = append(result, step)

			// Enqueue the destination as the next node to explore.
			queue = append(queue, queueItem{nodeID: e.DstID, hopDepth: item.hopDepth + 1})
		}
	}

	return result, nil
}

// buildEdgeQuery constructs a parameterized query with optional kind filtering
// and a LIMIT clause.
//
// baseSQL must end with a WHERE clause condition (e.g. "WHERE e.dst_id = ?").
// objectID is the value for that condition.
func buildEdgeQuery(baseSQL, objectID string, kinds []types.EdgeKind, limit int) (string, []any) {
	args := []any{objectID}

	var sb strings.Builder
	sb.WriteString(baseSQL)

	if len(kinds) > 0 {
		sb.WriteString(" AND e.kind IN (")
		for i, k := range kinds {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString("?")
			args = append(args, string(k))
		}
		sb.WriteString(")")
	}

	sb.WriteString(" LIMIT ?")
	args = append(args, limit)

	return sb.String(), args
}

// queryEdgesBySrc returns edges where src_id = nodeID, filtered by kinds.
func queryEdgesBySrc(conn *sql.DB, nodeID string, kinds []types.EdgeKind) ([]types.Edge, error) {
	baseSQL := `SELECT id, kind, src_id, dst_id, confidence, derivation,
	                   agent_source, created_by, created_at, metadata_json
	            FROM edges
	            WHERE src_id = ?`

	args := []any{nodeID}
	var sb strings.Builder
	sb.WriteString(baseSQL)

	if len(kinds) > 0 {
		sb.WriteString(" AND kind IN (")
		for i, k := range kinds {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString("?")
			args = append(args, string(k))
		}
		sb.WriteString(")")
	}

	rows, err := conn.Query(sb.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var edges []types.Edge
	for rows.Next() {
		e, err := scanEdge(rows)
		if err != nil {
			return nil, err
		}
		edges = append(edges, e)
	}
	return edges, rows.Err()
}

// scanNeighbors reads rows from a query that joins edges LEFT JOIN objects.
// isForward indicates whether the "other end" is dst_id (true) or src_id (false).
func scanNeighbors(rows *sql.Rows, isForward bool) ([]Neighbor, error) {
	var out []Neighbor
	for rows.Next() {
		var (
			id           string
			kind         string
			srcID        string
			dstID        string
			confidence   sql.NullFloat64
			derivation   sql.NullString
			agentSource  sql.NullString
			createdBy    sql.NullString
			createdAt    string
			metadataJSON sql.NullString
			title        sql.NullString
			objType      sql.NullString
			spaceID      sql.NullString
		)

		if err := rows.Scan(
			&id, &kind, &srcID, &dstID, &confidence, &derivation,
			&agentSource, &createdBy, &createdAt, &metadataJSON,
			&title, &objType, &spaceID,
		); err != nil {
			return nil, fmt.Errorf("graph: scan neighbor row: %w", err)
		}

		ts, _ := time.Parse(time.RFC3339, createdAt)
		edge := types.Edge{
			ID:           id,
			Kind:         types.EdgeKind(kind),
			SrcID:        srcID,
			DstID:        dstID,
			Confidence:   confidence.Float64,
			Derivation:   derivation.String,
			AgentSource:  agentSource.String,
			CreatedBy:    createdBy.String,
			CreatedAt:    ts,
			MetadataJSON: metadataJSON.String,
		}

		var endpoint string
		if isForward {
			endpoint = dstID
		} else {
			endpoint = srcID
		}

		out = append(out, Neighbor{
			Edge:     edge,
			Endpoint: endpoint,
			Title:    title.String,
			Type:     objType.String,
			Space:    spaceID.String,
		})
	}
	return out, rows.Err()
}

// scanEdge reads one edge row (10 columns: no object join).
func scanEdge(rows *sql.Rows) (types.Edge, error) {
	var (
		id           string
		kind         string
		srcID        string
		dstID        string
		confidence   sql.NullFloat64
		derivation   sql.NullString
		agentSource  sql.NullString
		createdBy    sql.NullString
		createdAt    string
		metadataJSON sql.NullString
	)

	if err := rows.Scan(
		&id, &kind, &srcID, &dstID, &confidence, &derivation,
		&agentSource, &createdBy, &createdAt, &metadataJSON,
	); err != nil {
		return types.Edge{}, fmt.Errorf("graph: scan edge row: %w", err)
	}

	ts, _ := time.Parse(time.RFC3339, createdAt)
	return types.Edge{
		ID:           id,
		Kind:         types.EdgeKind(kind),
		SrcID:        srcID,
		DstID:        dstID,
		Confidence:   confidence.Float64,
		Derivation:   derivation.String,
		AgentSource:  agentSource.String,
		CreatedBy:    createdBy.String,
		CreatedAt:    ts,
		MetadataJSON: metadataJSON.String,
	}, nil
}
