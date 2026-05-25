// Package pulse implements time-windowed signal aggregation over the Hyphae
// org graph. It is the read-side companion to recall/graph: not "find docs
// about X" but "what is going on in this space right now."
//
// The primary entry point is ComputeAndCache, which tries a cached result
// first and falls back to Compute + Store on a miss.
package pulse

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ErrNoCache is returned by Cached when no valid cache entry exists for the
// requested (spaceID, windowLabel) pair within the given TTL.
var ErrNoCache = errors.New("pulse: no cache entry")

// defaultWindow is the fallback window used when Compute receives window == 0.
const defaultWindow = 30 * 24 * time.Hour

// Pulse is the v0.1.3 time-windowed signal aggregation result.
type Pulse struct {
	Space       string    `json:"space"`
	Window      string    `json:"window"`        // human form, e.g. "30d"
	WindowStart time.Time `json:"window_start"`
	ComputedAt  time.Time `json:"computed_at"`

	TopInitiatives []TopInitiative `json:"top_initiatives"`
	HotZones       []HotZone       `json:"hot_zones"`
	RecentPressure []Pressure      `json:"recent_pressure"`
	EdgeKindDist   []KindCount     `json:"edge_kind_distribution"`
	Activity       Activity        `json:"activity"`
	TokensUsed     int             `json:"tokens_used"`
}

// TopInitiative is an active initiative ranked by total inbound edges.
type TopInitiative struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	InboundEdges int    `json:"inbound_edges"` // proxy for "things pointing at this initiative"
}

// HotZone is an object that saw notable activity within the window.
type HotZone struct {
	ObjectID     string `json:"object_id"`
	Title        string `json:"title,omitempty"`
	Type         string `json:"type,omitempty"`
	GraftEdgesIn int    `json:"graft_edges_in"` // derived_from edges that landed here in-window
	NewEdgesOut  int    `json:"new_edges_out"`  // edges emitted from this object in-window
}

// Pressure is a named signal (edge kind or receipt action) with a count.
// Topic is a best-effort human label for the signal grouping.
type Pressure struct {
	Kind  string `json:"kind"`  // edge kind or receipt action
	Topic string `json:"topic"` // best-effort label: "edges" for edge kinds, "receipts" for actions
	Count int    `json:"count"`
}

// KindCount is a single row in the edge-kind distribution histogram.
type KindCount struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

// Activity is the summary count block for the window.
type Activity struct {
	SporesSubmitted int `json:"spores_submitted"`
	GraftsApplied   int `json:"grafts_applied"`
	NewObjects      int `json:"new_objects"` // objects whose updated_at is in-window
	NewEdges        int `json:"new_edges"`   // edges created_at in-window
}

// Compute runs the live aggregation against conn.
//
// spaceID is the space URI (e.g. "hypha://m31labs/hyphae"). Pass "" to
// aggregate across all spaces.
//
// window is a Go duration. Pass 0 to use the default 30-day window.
func Compute(conn *sql.DB, spaceID string, window time.Duration) (Pulse, error) {
	if window <= 0 {
		window = defaultWindow
	}

	now := time.Now().UTC()
	windowStart := now.Add(-window)
	windowLabel := durationLabel(window)
	windowStartStr := windowStart.Format(time.RFC3339)

	p := Pulse{
		Space:       spaceID,
		Window:      windowLabel,
		WindowStart: windowStart,
		ComputedAt:  now,
	}

	var err error

	p.TopInitiatives, err = queryTopInitiatives(conn, spaceID)
	if err != nil {
		return Pulse{}, fmt.Errorf("pulse: top initiatives: %w", err)
	}

	p.HotZones, err = queryHotZones(conn, spaceID, windowStartStr)
	if err != nil {
		return Pulse{}, fmt.Errorf("pulse: hot zones: %w", err)
	}

	p.RecentPressure, err = queryRecentPressure(conn, spaceID, windowStartStr)
	if err != nil {
		return Pulse{}, fmt.Errorf("pulse: recent pressure: %w", err)
	}

	p.EdgeKindDist, err = queryEdgeKindDist(conn, spaceID)
	if err != nil {
		return Pulse{}, fmt.Errorf("pulse: edge kind distribution: %w", err)
	}

	p.Activity, err = queryActivity(conn, spaceID, windowStartStr)
	if err != nil {
		return Pulse{}, fmt.Errorf("pulse: activity: %w", err)
	}

	// Estimate token usage from the JSON representation.
	b, err := json.Marshal(p)
	if err != nil {
		return Pulse{}, fmt.Errorf("pulse: marshal for token count: %w", err)
	}
	p.TokensUsed = len(b) / 4

	return p, nil
}

// Cached returns a cached Pulse for (spaceID, windowLabel) if one exists and
// was computed within ttl of now.
//
// Returns (Pulse{}, ErrNoCache) on a cache miss (no row, or row is stale).
// Returns (Pulse, nil) on a hit.
func Cached(conn *sql.DB, spaceID, windowLabel string, ttl time.Duration) (Pulse, error) {
	cutoff := time.Now().UTC().Add(-ttl).Format(time.RFC3339)

	var bodyJSON string
	var computedAtStr string

	err := conn.QueryRow(
		`SELECT body_json, computed_at FROM pulse_cache
		 WHERE space_id = ? AND window = ? AND computed_at > ?
		 ORDER BY computed_at DESC LIMIT 1`,
		spaceID, windowLabel, cutoff,
	).Scan(&bodyJSON, &computedAtStr)
	if errors.Is(err, sql.ErrNoRows) {
		return Pulse{}, ErrNoCache
	}
	if err != nil {
		return Pulse{}, fmt.Errorf("pulse: cached query: %w", err)
	}

	var p Pulse
	if err := json.Unmarshal([]byte(bodyJSON), &p); err != nil {
		return Pulse{}, fmt.Errorf("pulse: cached unmarshal: %w", err)
	}
	return p, nil
}

// Store persists a Pulse into pulse_cache with the given expiry duration.
func Store(conn *sql.DB, p Pulse, expires time.Duration) error {
	b, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("pulse: store marshal: %w", err)
	}

	id := uuid.New().String()
	now := time.Now().UTC()
	computedAt := now.Format(time.RFC3339)
	expiresAt := now.Add(expires).Format(time.RFC3339)

	_, err = conn.Exec(
		`INSERT INTO pulse_cache(id, space_id, window, body_json, token_count, computed_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, p.Space, p.Window, string(b), p.TokensUsed, computedAt, expiresAt,
	)
	if err != nil {
		return fmt.Errorf("pulse: store insert: %w", err)
	}
	return nil
}

// ComputeAndCache is the recommended top-level entry point. It tries Cached
// first; on a miss it calls Compute and then Store before returning.
func ComputeAndCache(conn *sql.DB, spaceID string, window time.Duration, ttl time.Duration) (Pulse, error) {
	if window <= 0 {
		window = defaultWindow
	}
	label := durationLabel(window)

	p, err := Cached(conn, spaceID, label, ttl)
	if err == nil {
		return p, nil
	}
	if !errors.Is(err, ErrNoCache) {
		return Pulse{}, err
	}

	p, err = Compute(conn, spaceID, window)
	if err != nil {
		return Pulse{}, err
	}

	if storeErr := Store(conn, p, ttl); storeErr != nil {
		// Non-fatal: return the freshly computed pulse even if caching fails.
		return p, nil
	}
	return p, nil
}

// --- internal query helpers -------------------------------------------------

func queryTopInitiatives(conn *sql.DB, spaceID string) ([]TopInitiative, error) {
	rows, err := conn.Query(
		`SELECT o.id, COALESCE(o.title, ''), COUNT(e.id) AS edges_in
		 FROM objects o
		 LEFT JOIN edges e ON e.dst_id = o.id
		 WHERE o.type = 'initiative' AND (o.status = 'active' OR o.status = '' OR o.status IS NULL)
		   AND (? = '' OR o.space_id = ?)
		 GROUP BY o.id
		 ORDER BY edges_in DESC
		 LIMIT 5`,
		spaceID, spaceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TopInitiative
	for rows.Next() {
		var ti TopInitiative
		if err := rows.Scan(&ti.ID, &ti.Title, &ti.InboundEdges); err != nil {
			return nil, err
		}
		out = append(out, ti)
	}
	return out, rows.Err()
}

func queryHotZones(conn *sql.DB, spaceID, windowStart string) ([]HotZone, error) {
	rows, err := conn.Query(
		`SELECT o.id, COALESCE(o.title, ''), COALESCE(o.type, ''),
		        SUM(CASE WHEN e_in.kind = 'derived_from' AND e_in.created_at > ? THEN 1 ELSE 0 END) AS graft_in,
		        SUM(CASE WHEN e_out.created_at > ? THEN 1 ELSE 0 END) AS new_out
		 FROM objects o
		 LEFT JOIN edges e_in  ON e_in.dst_id = o.id
		 LEFT JOIN edges e_out ON e_out.src_id = o.id
		 WHERE (? = '' OR o.space_id = ?)
		 GROUP BY o.id
		 HAVING graft_in > 0 OR new_out > 0 OR o.updated_at > ?
		 ORDER BY (graft_in*3 + new_out) DESC
		 LIMIT 8`,
		windowStart, windowStart, spaceID, spaceID, windowStart,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []HotZone
	for rows.Next() {
		var hz HotZone
		if err := rows.Scan(&hz.ObjectID, &hz.Title, &hz.Type, &hz.GraftEdgesIn, &hz.NewEdgesOut); err != nil {
			return nil, err
		}
		out = append(out, hz)
	}
	return out, rows.Err()
}

func queryRecentPressure(conn *sql.DB, spaceID, windowStart string) ([]Pressure, error) {
	// Edge kinds in-window.
	edgeRows, err := conn.Query(
		`SELECT kind, COUNT(*) AS c
		 FROM edges
		 WHERE created_at > ?
		 GROUP BY kind
		 ORDER BY c DESC
		 LIMIT 6`,
		windowStart,
	)
	if err != nil {
		return nil, err
	}
	defer edgeRows.Close()

	var out []Pressure
	for edgeRows.Next() {
		var p Pressure
		if err := edgeRows.Scan(&p.Kind, &p.Count); err != nil {
			return nil, err
		}
		// "edges" is the topic label for edge-derived pressure signals.
		p.Topic = "edges"
		out = append(out, p)
	}
	if err := edgeRows.Err(); err != nil {
		return nil, err
	}

	// Receipt actions in-window.
	receiptRows, err := conn.Query(
		`SELECT action, COUNT(*) AS c
		 FROM receipts
		 WHERE created_at > ?
		 GROUP BY action
		 ORDER BY c DESC
		 LIMIT 4`,
		windowStart,
	)
	if err != nil {
		return nil, err
	}
	defer receiptRows.Close()

	for receiptRows.Next() {
		var p Pressure
		if err := receiptRows.Scan(&p.Kind, &p.Count); err != nil {
			return nil, err
		}
		// "receipts" is the topic label for receipt-action-derived pressure signals.
		p.Topic = "receipts"
		out = append(out, p)
	}
	return out, receiptRows.Err()
}

func queryEdgeKindDist(conn *sql.DB, spaceID string) ([]KindCount, error) {
	rows, err := conn.Query(
		`SELECT kind, COUNT(*) AS c
		 FROM edges
		 GROUP BY kind
		 ORDER BY c DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []KindCount
	for rows.Next() {
		var kc KindCount
		if err := rows.Scan(&kc.Kind, &kc.Count); err != nil {
			return nil, err
		}
		out = append(out, kc)
	}
	return out, rows.Err()
}

func queryActivity(conn *sql.DB, spaceID, windowStart string) (Activity, error) {
	var a Activity

	if err := conn.QueryRow(
		`SELECT COUNT(*) FROM receipts WHERE action = 'spore:create' AND created_at > ?`,
		windowStart,
	).Scan(&a.SporesSubmitted); err != nil {
		return Activity{}, err
	}

	if err := conn.QueryRow(
		`SELECT COUNT(*) FROM receipts WHERE action = 'graft' AND created_at > ?`,
		windowStart,
	).Scan(&a.GraftsApplied); err != nil {
		return Activity{}, err
	}

	if err := conn.QueryRow(
		`SELECT COUNT(*) FROM objects WHERE updated_at > ?`,
		windowStart,
	).Scan(&a.NewObjects); err != nil {
		return Activity{}, err
	}

	if err := conn.QueryRow(
		`SELECT COUNT(*) FROM edges WHERE created_at > ?`,
		windowStart,
	).Scan(&a.NewEdges); err != nil {
		return Activity{}, err
	}

	return a, nil
}

// durationLabel converts a Go duration to a compact human label.
// 30d, 7d, 14d, 90d, etc. Falls back to the duration string for unusual values.
func durationLabel(d time.Duration) string {
	hours := d.Hours()
	days := int(hours / 24)
	if days > 0 && float64(days)*24 == hours {
		return fmt.Sprintf("%dd", days)
	}
	return d.String()
}
