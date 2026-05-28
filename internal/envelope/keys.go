package envelope

// fullToCompact is the full-key → short-key map used by FormatCompact.
//
// Rules:
//   - Keys are rewritten recursively through the entire JSON tree,
//     including nested objects and arrays.
//   - Keys absent from the map pass through unchanged. Safe default
//     for newly-introduced fields; add them here when token budget matters.
//   - Mappings are write-only: hyphae does not consume its own compact
//     output, so there is no decoder pair.
//
// When you add a new command's data payload, add its keys here.
var fullToCompact = map[string]string{
	// Envelope chrome.
	"ok":             "ok",
	"command":        "c",
	"hyphae_version": "v",
	"schema":         "s",
	"data":           "d",
	"warnings":       "w",
	"errors":         "e",

	// Note.
	"code":    "co",
	"message": "m",
	"hint":    "h",
	"path":    "p",

	// Recall response.
	"query":       "q",
	"summary":     "su",
	"hits":        "hs",
	"anchors":     "a", // legacy; kept until snippet-citation anchor key collision is resolved
	"tokens_used": "tu",
	"shape":       "sh",

	// Recall hit.
	"uri":         "u",
	"title":       "t",
	"tokens_full": "tf",
	"score":       "sc",
	"snippets":    "sn",

	// Recall snippet + citation.
	"text":     "tx",
	"citation": "ci",
	"anchor":   "an",
	"line":     "ln",
	"end_line": "el",

	// Pulse.
	"space":             "sp",
	"window":            "wn",
	"window_start":      "ws",
	"computed_at":       "ca",
	"top_initiatives":   "ti",
	"hot_zones":         "hz",
	"recent_pressure":   "rp",
	"edge_distribution": "ed",
	"activity":          "ac",
	"id":                "i",
	"inbound_edges":     "ie",

	// Spore list.
	"status":       "st",
	"submitted_at": "sa",

	// Spaces list.
	"name":      "n",
	"root":      "r",
	"object_id": "oi",

	// Trace list.
	"agent":   "ag",
	"task":    "tk",
	"phase":   "ph",
	"started": "sd",

	// Assess.
	"alignment":          "al",
	"recommendation":     "rc",
	"matched_initiatives": "mi",
	"reason":             "rn",
	"risks":              "rk",
	"hot_zone":           "hzn",
}
