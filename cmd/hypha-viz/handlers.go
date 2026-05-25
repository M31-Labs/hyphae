//go:build !js

package main

import (
	"database/sql"
	"strconv"
	"strings"

	"github.com/odvcencio/gosx/server"
	"github.com/odvcencio/hyphae/internal/recall"
	"github.com/odvcencio/hyphae/internal/types"
	"github.com/odvcencio/hyphae/internal/vizdata"
)

// handleGraph handles GET /api/graph[?center=&depth=&kind=&limit=].
//
// Without center: returns the full graph (capped at 500 nodes).
// With center: BFS subgraph from that id to depth (default 2).
// kind: comma-separated edge-kind filter.
// limit: node cap for full-graph mode (default 500).
func handleGraph(conn *sql.DB) server.APIHandler {
	return func(ctx *server.Context) (any, error) {
		q := ctx.Request.URL.Query()

		centerID := strings.TrimSpace(q.Get("center"))
		kindStr := strings.TrimSpace(q.Get("kind"))
		depthStr := strings.TrimSpace(q.Get("depth"))
		limitStr := strings.TrimSpace(q.Get("limit"))

		depth := 2
		if depthStr != "" {
			if d, err := strconv.Atoi(depthStr); err == nil && d > 0 {
				depth = d
			}
		}
		limit := 500
		if limitStr != "" {
			if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
				limit = l
			}
		}

		var kindFilter []types.EdgeKind
		if kindStr != "" {
			for _, k := range strings.Split(kindStr, ",") {
				if t := strings.TrimSpace(k); t != "" {
					kindFilter = append(kindFilter, types.EdgeKind(t))
				}
			}
		}

		if centerID != "" {
			resp, err := vizdata.Subgraph(conn, centerID, depth, kindFilter)
			if err != nil {
				return nil, err
			}
			return resp, nil
		}

		resp, err := vizdata.FullGraph(conn, kindFilter, limit)
		if err != nil {
			return nil, err
		}
		return resp, nil
	}
}

// handleSearch handles GET /api/search?q=.
// Wraps internal/recall.Recall with default budget (summary+anchors, 800 tokens).
func handleSearch(conn *sql.DB) server.APIHandler {
	return func(ctx *server.Context) (any, error) {
		q := strings.TrimSpace(ctx.Request.URL.Query().Get("q"))
		if q == "" {
			return map[string]any{
				"summary": "",
				"anchors": []any{},
				"query":   "",
			}, nil
		}
		resp, err := recall.Recall(conn, q, 12, types.DefaultBudget())
		if err != nil {
			return nil, err
		}
		return resp, nil
	}
}

// handleObject handles GET /api/object/{id}.
// Returns object metadata, anchors, backlinks, and forward links.
func handleObject(conn *sql.DB) server.APIHandler {
	return func(ctx *server.Context) (any, error) {
		id := strings.TrimSpace(ctx.Request.PathValue("id"))
		if id == "" {
			// Fall back to query parameter for compatibility.
			id = strings.TrimSpace(ctx.Request.URL.Query().Get("id"))
		}
		if id == "" {
			return map[string]any{"error": "missing id"}, nil
		}
		detail, err := vizdata.GetObjectDetail(conn, id)
		if err != nil {
			return nil, err
		}
		if detail == nil || detail.Object == nil {
			return map[string]any{"error": "not found", "id": id}, nil
		}
		return detail, nil
	}
}
