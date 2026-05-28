package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"m31labs.dev/hyphae/internal/envelope"
)

// renderOpts describes how a tool's data payload should be packaged.
// Defaults are aggressively token-conscious: single-line full-key JSON.
type renderOpts struct {
	// Format selects the wire shape. "compact" → short-key single-line.
	// "json" → full-key indented (debug). default ("jsonline") → full-key
	// single-line.
	Format envelope.Format
	// MaxTokens is the soft budget the rendered response should fit
	// under. 0 means no enforcement. When over, list-shaped payloads
	// get trailing rows trimmed; non-list payloads pass through with
	// a warning.
	MaxTokens int
	// Fields, when non-empty, limits each row in a list-shaped payload
	// to just these top-level fields. Unknown fields are silently
	// dropped. Used by the list tools.
	Fields []string
}

// optsFromArgs reads the standard token-discipline args (format,
// max_tokens, fields) from a tool's argument map.
func optsFromArgs(args map[string]any, defaultMax int) renderOpts {
	opts := renderOpts{MaxTokens: defaultMax}
	if f, ok := args["format"].(string); ok {
		switch strings.ToLower(strings.TrimSpace(f)) {
		case "compact":
			opts.Format = envelope.FormatCompact
		case "json":
			opts.Format = envelope.FormatJSON
		case "", "jsonline", "line":
			opts.Format = envelope.FormatJSONLine
		}
	}
	if v, ok := args["max_tokens"]; ok {
		switch n := v.(type) {
		case float64:
			opts.MaxTokens = int(n)
		case int:
			opts.MaxTokens = n
		}
	}
	if fs, ok := args["fields"].([]any); ok {
		for _, f := range fs {
			if s, ok := f.(string); ok && s != "" {
				opts.Fields = append(opts.Fields, s)
			}
		}
	}
	return opts
}

// estimateTokens is the len/4 heuristic used everywhere else in hyphae.
func estimateTokens(s string) int { return (len(s) + 3) / 4 }

// render packages data into the envelope using opts, applying budget +
// field-select + truncation. Returns the wire text and any warnings that
// were collected during shaping (e.g. "truncated 3 rows").
func render(toolName string, data any, opts renderOpts) string {
	if opts.Format == envelope.FormatText {
		opts.Format = envelope.FormatJSONLine
	}

	var warnings []envelope.Note

	if len(opts.Fields) > 0 {
		data = projectFields(data, opts.Fields)
	}

	// First pass: encode as-is.
	text, err := encodeEnvelope(toolName, data, warnings, opts.Format)
	if err != nil {
		return fmt.Sprintf(`{"ok":false,"command":%q,"error":%q}`, toolName, err.Error())
	}

	if opts.MaxTokens > 0 && estimateTokens(text) > opts.MaxTokens {
		// Try truncating: if data is a slice or has a slice field, drop trailing rows.
		trimmed, dropped, ok := truncateOverBudget(data, opts.MaxTokens, opts.Format, toolName)
		if ok {
			warnings = append(warnings, envelope.Note{
				Code:    "TRUNCATED",
				Message: fmt.Sprintf("dropped %d trailing row(s) to fit max_tokens=%d", dropped, opts.MaxTokens),
				Hint:    "increase max_tokens or narrow the filter to see more",
			})
			text, _ = encodeEnvelope(toolName, trimmed, warnings, opts.Format)
		} else {
			warnings = append(warnings, envelope.Note{
				Code:    "OVER_BUDGET",
				Message: fmt.Sprintf("response is ~%d tokens, exceeds max_tokens=%d", estimateTokens(text), opts.MaxTokens),
				Hint:    "non-list payload could not be safely truncated",
			})
			text, _ = encodeEnvelope(toolName, data, warnings, opts.Format)
		}
	}

	return text
}

func encodeEnvelope(toolName string, data any, warnings []envelope.Note, f envelope.Format) (string, error) {
	env := envelope.New(toolName, data)
	if len(warnings) > 0 {
		env.Warnings = warnings
	}
	var buf bytes.Buffer
	if err := envelope.Emit(&buf, env, f, nil); err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}

// projectFields shapes a slice-of-maps or slice-of-structs payload to only
// the requested top-level fields. Non-list payloads are returned unchanged.
func projectFields(data any, fields []string) any {
	if len(fields) == 0 {
		return data
	}
	want := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		want[f] = struct{}{}
	}

	// Marshal-roundtrip to a generic shape so we can filter uniformly.
	raw, err := json.Marshal(data)
	if err != nil {
		return data
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return data
	}
	arr, ok := tree.([]any)
	if !ok {
		return data
	}
	for i, row := range arr {
		obj, ok := row.(map[string]any)
		if !ok {
			continue
		}
		for k := range obj {
			if _, keep := want[k]; !keep {
				delete(obj, k)
			}
		}
		arr[i] = obj
	}
	return arr
}

// truncateOverBudget walks the data, finds the largest slice it can drop
// trailing items from, and returns a trimmed payload that fits the budget
// when re-encoded. Returns (trimmedData, droppedCount, true) on success or
// (nil, 0, false) when no slice was found to truncate.
func truncateOverBudget(data any, maxTokens int, f envelope.Format, toolName string) (any, int, bool) {
	// Fast path: data is itself a slice.
	v := reflect.ValueOf(data)
	if !v.IsValid() {
		return nil, 0, false
	}
	if v.Kind() == reflect.Slice {
		orig := v.Len()
		size := orig
		for size > 0 {
			trimmed := v.Slice(0, size).Interface()
			text, err := encodeEnvelope(toolName, trimmed, nil, f)
			if err != nil {
				return nil, 0, false
			}
			if estimateTokens(text) <= maxTokens {
				return trimmed, orig - size, true
			}
			// Drop ~10% of remaining each pass to converge quickly.
			drop := size / 10
			if drop < 1 {
				drop = 1
			}
			size -= drop
		}
		// Couldn't fit even with zero rows — return empty slice.
		empty := reflect.MakeSlice(v.Type(), 0, 0).Interface()
		return empty, orig, true
	}
	// Slow path: data is a map or struct with a slice-shaped field.
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, 0, false
	}
	var tree map[string]any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return nil, 0, false
	}
	// Find the largest array-valued field.
	var biggestKey string
	var biggestLen int
	for k, v := range tree {
		if arr, ok := v.([]any); ok && len(arr) > biggestLen {
			biggestKey = k
			biggestLen = len(arr)
		}
	}
	if biggestKey == "" {
		return nil, 0, false
	}
	arr := tree[biggestKey].([]any)
	size := len(arr)
	for size > 0 {
		clone := make(map[string]any, len(tree))
		for k, v := range tree {
			clone[k] = v
		}
		clone[biggestKey] = arr[:size]
		text, err := encodeEnvelope(toolName, clone, nil, f)
		if err != nil {
			return nil, 0, false
		}
		if estimateTokens(text) <= maxTokens {
			return clone, len(arr) - size, true
		}
		drop := size / 10
		if drop < 1 {
			drop = 1
		}
		size -= drop
	}
	clone := make(map[string]any, len(tree))
	for k, v := range tree {
		clone[k] = v
	}
	clone[biggestKey] = []any{}
	return clone, len(arr), true
}
