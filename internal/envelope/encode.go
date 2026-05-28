package envelope

import (
	"encoding/json"
	"fmt"
	"io"
)

// TextRenderer prints a human-readable view of an Envelope's data payload
// to w. It is invoked by Emit in FormatText mode and should not emit
// envelope chrome (ok/command/version) — that information is implicit in
// the calling command and the absence of an error.
type TextRenderer func(w io.Writer, data any) error

// Emit writes env to w in the requested format.
//
// In FormatText mode: if env.OK and textRenderer is non-nil, the renderer
// prints the payload. If env.OK and textRenderer is nil, falls back to
// indented JSON (the safest thing to do when no human view exists). If
// not env.OK, errors are printed to w as `error: <message>` lines with
// optional `  hint: <hint>` follow-ups.
//
// In FormatJSON mode: env is marshaled with two-space indentation.
// In FormatCompact mode: env is marshaled, every key is rewritten via the
// fullToCompact map recursively, and the result is emitted single-line.
func Emit(w io.Writer, env *Envelope, f Format, textRenderer TextRenderer) error {
	if env == nil {
		return fmt.Errorf("envelope: Emit got nil envelope")
	}
	switch f {
	case FormatText:
		if !env.OK {
			return emitErrorsText(w, env.Errors)
		}
		if textRenderer == nil {
			return encodeJSON(w, env)
		}
		if err := textRenderer(w, env.Data); err != nil {
			return err
		}
		return emitWarningsText(w, env.Warnings)
	case FormatJSON:
		return encodeJSON(w, env)
	case FormatCompact:
		return encodeCompact(w, env)
	case FormatJSONLine:
		return encodeJSONLine(w, env)
	default:
		return fmt.Errorf("envelope: unknown format %d", f)
	}
}

func emitErrorsText(w io.Writer, errs []Note) error {
	for _, e := range errs {
		if _, err := fmt.Fprintf(w, "error: %s\n", e.Message); err != nil {
			return err
		}
		if e.Hint != "" {
			if _, err := fmt.Fprintf(w, "  hint: %s\n", e.Hint); err != nil {
				return err
			}
		}
	}
	return nil
}

func emitWarningsText(w io.Writer, warns []Note) error {
	for _, n := range warns {
		if _, err := fmt.Fprintf(w, "warning: %s\n", n.Message); err != nil {
			return err
		}
	}
	return nil
}

func encodeJSON(w io.Writer, env *Envelope) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(env)
}

// encodeJSONLine writes a single-line, full-key JSON encoding (no
// indentation). About 30% fewer bytes than encodeJSON without changing
// any field names — the right default for parse-friendly machine output.
func encodeJSONLine(w io.Writer, env *Envelope) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(env)
}

func encodeCompact(w io.Writer, env *Envelope) error {
	raw, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("envelope: marshal: %w", err)
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return fmt.Errorf("envelope: roundtrip: %w", err)
	}
	rewritten := rewriteKeys(tree)
	out, err := json.Marshal(rewritten)
	if err != nil {
		return fmt.Errorf("envelope: re-marshal: %w", err)
	}
	if _, err := w.Write(out); err != nil {
		return err
	}
	_, err = w.Write([]byte{'\n'})
	return err
}

// rewriteKeys walks v recursively, replacing object keys with their
// compact-form equivalents per fullToCompact. Keys absent from the map
// pass through.
func rewriteKeys(v any) any {
	switch n := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(n))
		for k, child := range n {
			short, ok := fullToCompact[k]
			if !ok {
				short = k
			}
			out[short] = rewriteKeys(child)
		}
		return out
	case []any:
		out := make([]any, len(n))
		for i, child := range n {
			out[i] = rewriteKeys(child)
		}
		return out
	default:
		return v
	}
}
