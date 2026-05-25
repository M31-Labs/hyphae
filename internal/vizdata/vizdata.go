// Package vizdata provides graph-query helpers for the hypha-viz visualization
// server. It bridges the internal/graph package (per-node BFS) and raw SQLite
// queries (full-graph dump) into the JSON shapes the API layer needs.
package vizdata

import (
	"database/sql"
	"fmt"

	"github.com/odvcencio/hyphae/internal/graph"
	"github.com/odvcencio/hyphae/internal/types"
)

// Node is the JSON shape returned by /api/graph for a graph node.
type Node struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Type  string `json:"type"`
	Title string `json:"title"`
	Space string `json:"space"`
}

// Edge is the JSON shape returned by /api/graph for a graph edge.
type Edge struct {
	From       string `json:"from"`
	To         string `json:"to"`
	Kind       string `json:"kind"`
	Derivation string `json:"derivation"`
}

// GraphResponse is the /api/graph response payload.
type GraphResponse struct {
	Nodes     []Node `json:"nodes"`
	Edges     []Edge `json:"edges"`
	Truncated bool   `json:"truncated,omitempty"`
}

// ObjectDetail is the /api/object/:id response payload.
type ObjectDetail struct {
	Object    *ObjectRow        `json:"object"`
	Anchors   []AnchorRow       `json:"anchors"`
	Backlinks []graph.Neighbor  `json:"backlinks"`
	Forward   []graph.Neighbor  `json:"forward"`
}

// ObjectRow holds the fields of a row from the objects table.
type ObjectRow struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	SpaceID string `json:"space_id"`
	Status  string `json:"status"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Tags    string `json:"tags_json"`
}

// AnchorRow holds the fields of a row from the anchors table.
type AnchorRow struct {
	ID          string `json:"id"`
	ObjectID    string `json:"object_id"`
	HeadingPath string `json:"heading_path"`
	StartLine   int    `json:"start_line"`
	EndLine     int    `json:"end_line"`
	NodeKind    string `json:"node_kind"`
}

const fullGraphNodeCap = 500

// FullGraph returns up to fullGraphNodeCap nodes and all edges between them.
// If the objects table has more rows, Truncated is set to true.
func FullGraph(conn *sql.DB, kindFilter []types.EdgeKind, limit int) (GraphResponse, error) {
	if limit <= 0 {
		limit = fullGraphNodeCap
	}

	// Count total objects.
	var total int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM objects`).Scan(&total); err != nil {
		return GraphResponse{}, fmt.Errorf("vizdata: count objects: %w", err)
	}

	truncated := total > limit

	// Load objects up to the cap.
	rows, err := conn.Query(`SELECT id, type, space_id, title FROM objects LIMIT ?`, limit)
	if err != nil {
		return GraphResponse{}, fmt.Errorf("vizdata: query objects: %w", err)
	}
	defer rows.Close()

	nodeMap := make(map[string]bool, limit)
	var nodes []Node
	for rows.Next() {
		var id, typ, spaceID, title string
		if err := rows.Scan(&id, &typ, &spaceID, &title); err != nil {
			return GraphResponse{}, fmt.Errorf("vizdata: scan object row: %w", err)
		}
		label := title
		if label == "" {
			label = id
		}
		nodes = append(nodes, Node{
			ID:    id,
			Label: label,
			Type:  typ,
			Title: title,
			Space: spaceID,
		})
		nodeMap[id] = true
	}
	if err := rows.Err(); err != nil {
		return GraphResponse{}, fmt.Errorf("vizdata: objects rows: %w", err)
	}

	// Load edges filtered to the node set (and optionally by kind).
	edgeQuery, edgeArgs := buildEdgesQuery(kindFilter)
	erows, err := conn.Query(edgeQuery, edgeArgs...)
	if err != nil {
		return GraphResponse{}, fmt.Errorf("vizdata: query edges: %w", err)
	}
	defer erows.Close()

	var edges []Edge
	for erows.Next() {
		var srcID, dstID, kind string
		var derivation sql.NullString
		if err := erows.Scan(&srcID, &dstID, &kind, &derivation); err != nil {
			return GraphResponse{}, fmt.Errorf("vizdata: scan edge row: %w", err)
		}
		// Only include edges where both endpoints are in our node set.
		if !nodeMap[srcID] || !nodeMap[dstID] {
			continue
		}
		edges = append(edges, Edge{
			From:       srcID,
			To:         dstID,
			Kind:       kind,
			Derivation: derivation.String,
		})
	}
	if err := erows.Err(); err != nil {
		return GraphResponse{}, fmt.Errorf("vizdata: edges rows: %w", err)
	}

	if nodes == nil {
		nodes = []Node{}
	}
	if edges == nil {
		edges = []Edge{}
	}

	return GraphResponse{
		Nodes:     nodes,
		Edges:     edges,
		Truncated: truncated,
	}, nil
}

// buildEdgesQuery constructs the edge SELECT with an optional kind filter.
func buildEdgesQuery(kinds []types.EdgeKind) (string, []any) {
	base := `SELECT src_id, dst_id, kind, derivation FROM edges`
	if len(kinds) == 0 {
		return base, nil
	}
	base += " WHERE kind IN ("
	args := make([]any, 0, len(kinds))
	for i, k := range kinds {
		if i > 0 {
			base += ", "
		}
		base += "?"
		args = append(args, string(k))
	}
	base += ")"
	return base, args
}

// Subgraph performs a BFS from centerID up to depth hops, collecting all nodes
// and edges encountered. kindFilter optionally restricts which edge kinds are
// followed.
func Subgraph(conn *sql.DB, centerID string, depth int, kindFilter []types.EdgeKind) (GraphResponse, error) {
	if depth <= 0 {
		depth = 2
	}

	type qitem struct {
		id    string
		level int
	}

	visited := make(map[string]bool)
	nodeMap := make(map[string]Node)
	var edgeList []Edge

	queue := []qitem{{id: centerID, level: 0}}
	visited[centerID] = true

	// Seed the center node info.
	if obj, err := GetObjectRow(conn, centerID); err == nil && obj != nil {
		label := obj.Title
		if label == "" {
			label = obj.ID
		}
		nodeMap[centerID] = Node{
			ID:    obj.ID,
			Label: label,
			Type:  obj.Type,
			Title: obj.Title,
			Space: obj.SpaceID,
		}
	} else {
		nodeMap[centerID] = Node{ID: centerID, Label: centerID}
	}

	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]

		if item.level >= depth {
			continue
		}

		// Gather forward links.
		fwd, err := graph.ForwardLinks(conn, item.id, kindFilter, 100)
		if err != nil {
			return GraphResponse{}, fmt.Errorf("vizdata: subgraph forward %q: %w", item.id, err)
		}
		for _, n := range fwd {
			edgeList = append(edgeList, Edge{
				From:       n.Edge.SrcID,
				To:         n.Edge.DstID,
				Kind:       string(n.Edge.Kind),
				Derivation: n.Edge.Derivation,
			})
			if !visited[n.Endpoint] {
				visited[n.Endpoint] = true
				label := n.Title
				if label == "" {
					label = n.Endpoint
				}
				nodeMap[n.Endpoint] = Node{
					ID:    n.Endpoint,
					Label: label,
					Type:  n.Type,
					Title: n.Title,
					Space: n.Space,
				}
				queue = append(queue, qitem{id: n.Endpoint, level: item.level + 1})
			}
		}

		// Gather backlinks.
		back, err := graph.Backlinks(conn, item.id, kindFilter, 100)
		if err != nil {
			return GraphResponse{}, fmt.Errorf("vizdata: subgraph back %q: %w", item.id, err)
		}
		for _, n := range back {
			edgeList = append(edgeList, Edge{
				From:       n.Edge.SrcID,
				To:         n.Edge.DstID,
				Kind:       string(n.Edge.Kind),
				Derivation: n.Edge.Derivation,
			})
			if !visited[n.Endpoint] {
				visited[n.Endpoint] = true
				label := n.Title
				if label == "" {
					label = n.Endpoint
				}
				nodeMap[n.Endpoint] = Node{
					ID:    n.Endpoint,
					Label: label,
					Type:  n.Type,
					Title: n.Title,
					Space: n.Space,
				}
				queue = append(queue, qitem{id: n.Endpoint, level: item.level + 1})
			}
		}
	}

	nodes := make([]Node, 0, len(nodeMap))
	for _, n := range nodeMap {
		nodes = append(nodes, n)
	}

	// Deduplicate edges by (from, to, kind).
	edgeList = dedupeEdges(edgeList)

	if nodes == nil {
		nodes = []Node{}
	}
	if edgeList == nil {
		edgeList = []Edge{}
	}

	return GraphResponse{
		Nodes: nodes,
		Edges: edgeList,
	}, nil
}

func dedupeEdges(edges []Edge) []Edge {
	type key struct{ from, to, kind string }
	seen := make(map[key]bool, len(edges))
	out := edges[:0]
	for _, e := range edges {
		k := key{e.From, e.To, e.Kind}
		if !seen[k] {
			seen[k] = true
			out = append(out, e)
		}
	}
	return out
}

// GetObjectRow fetches one row from the objects table. Returns nil, nil if not found.
func GetObjectRow(conn *sql.DB, id string) (*ObjectRow, error) {
	row := conn.QueryRow(
		`SELECT id, type, space_id, COALESCE(status,''), title, COALESCE(summary,''), COALESCE(tags_json,'[]')
		 FROM objects WHERE id = ?`, id,
	)
	var obj ObjectRow
	err := row.Scan(&obj.ID, &obj.Type, &obj.SpaceID, &obj.Status, &obj.Title, &obj.Summary, &obj.Tags)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("vizdata: get object %q: %w", id, err)
	}
	return &obj, nil
}

// GetAnchors returns all anchors for objectID.
func GetAnchors(conn *sql.DB, objectID string) ([]AnchorRow, error) {
	rows, err := conn.Query(
		`SELECT id, object_id, COALESCE(heading_path,''), start_line, end_line, COALESCE(node_kind,'')
		 FROM anchors WHERE object_id = ? ORDER BY start_line`,
		objectID,
	)
	if err != nil {
		return nil, fmt.Errorf("vizdata: get anchors %q: %w", objectID, err)
	}
	defer rows.Close()

	var out []AnchorRow
	for rows.Next() {
		var a AnchorRow
		if err := rows.Scan(&a.ID, &a.ObjectID, &a.HeadingPath, &a.StartLine, &a.EndLine, &a.NodeKind); err != nil {
			return nil, fmt.Errorf("vizdata: scan anchor: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetObjectDetail assembles the full detail payload for /api/object/:id.
func GetObjectDetail(conn *sql.DB, id string) (*ObjectDetail, error) {
	obj, err := GetObjectRow(conn, id)
	if err != nil {
		return nil, err
	}

	anchors, err := GetAnchors(conn, id)
	if err != nil {
		return nil, err
	}

	backlinks, err := graph.Backlinks(conn, id, nil, 50)
	if err != nil {
		return nil, fmt.Errorf("vizdata: backlinks %q: %w", id, err)
	}

	forward, err := graph.ForwardLinks(conn, id, nil, 50)
	if err != nil {
		return nil, fmt.Errorf("vizdata: forward %q: %w", id, err)
	}

	if anchors == nil {
		anchors = []AnchorRow{}
	}
	if backlinks == nil {
		backlinks = []graph.Neighbor{}
	}
	if forward == nil {
		forward = []graph.Neighbor{}
	}

	return &ObjectDetail{
		Object:    obj,
		Anchors:   anchors,
		Backlinks: backlinks,
		Forward:   forward,
	}, nil
}
