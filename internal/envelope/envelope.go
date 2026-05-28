// Package envelope is the uniform wire shape every `hypha` command writes
// when invoked with --format json or --format compact.
//
// The same Envelope value can be rendered three ways:
//
//	FormatJSON      — full-key, indented JSON. Human-debuggable, parser-friendly.
//	FormatCompact   — same fields, single-line, documented short keys.
//	                  Lossless of FormatJSON; produced for agent callers.
//	FormatText      — envelope chrome dropped; the caller's textRenderer
//	                  prints a human view of the data payload.
//
// SchemaVersion bumps only when the Envelope or the short-key map changes;
// it is independent of the Hyphae release version.
package envelope

// SchemaVersion is the envelope schema version. Bump when the Envelope
// struct or the short-key map (see keys.go) changes shape.
const SchemaVersion = 1

// Envelope is the outer shape of every machine-readable hypha response.
//
// Convention: Data carries the command-specific payload. Errors and Warnings
// are always present (possibly empty) so parsers can rely on the field
// without a presence check.
type Envelope struct {
	OK            bool   `json:"ok"`
	Command       string `json:"command"`
	HyphaeVersion string `json:"hyphae_version"`
	Schema        int    `json:"schema"`
	Data          any    `json:"data,omitempty"`
	Warnings      []Note `json:"warnings"`
	Errors        []Note `json:"errors"`
}

// Note is a typed warning or error inside an Envelope.
//
// Code is a stable machine identifier (e.g. NOT_FOUND, BAD_INPUT). Hint
// is an actionable human suggestion. Path is a JSONPath-ish pointer into
// Data when the note refers to a specific field.
type Note struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
	Path    string `json:"path,omitempty"`
}

// New constructs an Envelope for a successful command response.
func New(command string, data any) *Envelope {
	return &Envelope{
		OK:            true,
		Command:       command,
		HyphaeVersion: HyphaeVersion,
		Schema:        SchemaVersion,
		Data:          data,
		Warnings:      []Note{},
		Errors:        []Note{},
	}
}

// NewError constructs an Envelope for a failed command response.
func NewError(command string, errs ...Note) *Envelope {
	return &Envelope{
		OK:            false,
		Command:       command,
		HyphaeVersion: HyphaeVersion,
		Schema:        SchemaVersion,
		Warnings:      []Note{},
		Errors:        errs,
	}
}

// AddWarning appends a Warning, initializing the slice if nil.
func (e *Envelope) AddWarning(n Note) {
	e.Warnings = append(e.Warnings, n)
}

// HyphaeVersion is the running binary's version. Wired by main at startup
// via SetHyphaeVersion so the envelope package does not depend on cmd.
var HyphaeVersion = "0.0.0"

// SetHyphaeVersion records the running binary's version string. Call once
// at startup before emitting envelopes.
func SetHyphaeVersion(v string) { HyphaeVersion = v }
