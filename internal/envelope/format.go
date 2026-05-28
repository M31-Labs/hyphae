package envelope

import (
	"fmt"
	"os"
	"strings"

	"github.com/mattn/go-isatty"
)

// Format selects how an Envelope is rendered.
type Format int

const (
	// FormatText drops envelope chrome and lets the caller's renderer
	// print a human view of the data payload.
	FormatText Format = iota
	// FormatJSON renders the full Envelope as indented, full-key JSON.
	FormatJSON
	// FormatCompact renders the full Envelope as single-line JSON using
	// the documented short-key map. Same data as FormatJSON.
	FormatCompact
	// FormatJSONLine is single-line full-key JSON — no indentation,
	// but every key spelled out. The sweet spot for callers that need
	// machine-readable output without paying the indentation tax or
	// learning the compact key map. Used by the MCP server.
	FormatJSONLine
)

// String returns the user-facing name of the format ("text", "json",
// "compact", "jsonline").
func (f Format) String() string {
	switch f {
	case FormatText:
		return "text"
	case FormatJSON:
		return "json"
	case FormatCompact:
		return "compact"
	case FormatJSONLine:
		return "jsonline"
	default:
		return fmt.Sprintf("format(%d)", int(f))
	}
}

// ParseFormat resolves a user-supplied format name. Empty string returns
// the auto-detected default.
func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return AutoDetect(), nil
	case "text":
		return FormatText, nil
	case "json":
		return FormatJSON, nil
	case "compact":
		return FormatCompact, nil
	case "jsonline", "json-line", "line":
		return FormatJSONLine, nil
	default:
		return FormatText, fmt.Errorf("unknown --format %q (expected text|json|compact|jsonline)", s)
	}
}

// AutoDetect returns the default format for the current process. The order
// is:
//
//  1. HYPHAE_FORMAT env var, if it parses.
//  2. FormatText if stdout is a TTY.
//  3. FormatCompact otherwise (piped, redirected, agent caller).
//
// AutoDetect never returns an error; an invalid HYPHAE_FORMAT is silently
// ignored in favor of the TTY check.
func AutoDetect() Format {
	if v := strings.TrimSpace(os.Getenv("HYPHAE_FORMAT")); v != "" {
		switch strings.ToLower(v) {
		case "text":
			return FormatText
		case "json":
			return FormatJSON
		case "compact":
			return FormatCompact
		}
	}
	if isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd()) {
		return FormatText
	}
	return FormatCompact
}
