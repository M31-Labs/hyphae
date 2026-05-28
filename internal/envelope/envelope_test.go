package envelope

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestNewSetsRequiredFields(t *testing.T) {
	SetHyphaeVersion("0.1.8")
	env := New("recall", map[string]any{"query": "x"})
	if !env.OK {
		t.Fatalf("OK should be true for New")
	}
	if env.Command != "recall" {
		t.Fatalf("Command = %q, want recall", env.Command)
	}
	if env.HyphaeVersion != "0.1.8" {
		t.Fatalf("HyphaeVersion = %q, want 0.1.8", env.HyphaeVersion)
	}
	if env.Schema != SchemaVersion {
		t.Fatalf("Schema = %d, want %d", env.Schema, SchemaVersion)
	}
	if env.Warnings == nil || env.Errors == nil {
		t.Fatalf("Warnings/Errors must be initialized to [] not nil")
	}
}

func TestEmitJSONFullKeys(t *testing.T) {
	SetHyphaeVersion("0.1.8")
	env := New("recall", map[string]any{"query": "x", "summary": "found 0"})

	var buf bytes.Buffer
	if err := Emit(&buf, env, FormatJSON, nil); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	got := buf.String()
	for _, want := range []string{
		`"ok": true`,
		`"command": "recall"`,
		`"hyphae_version": "0.1.8"`,
		`"schema": 1`,
		`"data":`,
		`"warnings": []`,
		`"errors": []`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("JSON output missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestEmitCompactShortKeys(t *testing.T) {
	SetHyphaeVersion("0.1.8")
	env := New("recall", map[string]any{
		"query":   "x",
		"summary": "found 0",
		"anchors": []any{
			map[string]any{"uri": "hypha://a", "title": "T", "score": 1.5, "tokens_full": 12},
		},
		"tokens_used": 4,
	})

	var buf bytes.Buffer
	if err := Emit(&buf, env, FormatCompact, nil); err != nil {
		t.Fatalf("Emit compact: %v", err)
	}
	got := buf.String()

	if strings.Contains(got, "\n") && !strings.HasSuffix(got, "\n") {
		t.Errorf("compact output should be single-line + trailing newline, got %q", got)
	}
	for _, want := range []string{
		`"ok":true`,
		`"c":"recall"`,
		`"v":"0.1.8"`,
		`"s":1`,
		`"d":`,
		`"w":[]`,
		`"e":[]`,
		`"q":"x"`,
		`"su":"found 0"`,
		`"a":[`,
		`"u":"hypha://a"`,
		`"t":"T"`,
		`"sc":1.5`,
		`"tf":12`,
		`"tu":4`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("compact output missing %q\ngot: %s", want, got)
		}
	}
	for _, unwanted := range []string{
		`"command"`,
		`"hyphae_version"`,
		`"data"`,
		`"query"`,
		`"summary"`,
		`"anchors"`,
		`"uri"`,
		`"title"`,
		`"score"`,
		`"tokens_full"`,
		`"tokens_used"`,
	} {
		if strings.Contains(got, unwanted) {
			t.Errorf("compact output should not contain full key %q\ngot: %s", unwanted, got)
		}
	}
}

func TestEmitCompactLosslessRoundtripStructure(t *testing.T) {
	SetHyphaeVersion("0.1.8")
	data := map[string]any{
		"query":   "test",
		"anchors": []any{map[string]any{"uri": "a", "title": "b"}},
	}
	env := New("recall", data)

	var buf bytes.Buffer
	if err := Emit(&buf, env, FormatCompact, nil); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	if got["c"] != "recall" {
		t.Errorf("c (command) = %v, want recall", got["c"])
	}
	d, ok := got["d"].(map[string]any)
	if !ok {
		t.Fatalf("d (data) not an object: %T", got["d"])
	}
	if d["q"] != "test" {
		t.Errorf("q (query) = %v, want test", d["q"])
	}
	anchors, ok := d["a"].([]any)
	if !ok || len(anchors) != 1 {
		t.Fatalf("a (anchors) shape wrong: %v", d["a"])
	}
	first, ok := anchors[0].(map[string]any)
	if !ok {
		t.Fatalf("anchor[0] not an object: %T", anchors[0])
	}
	if first["u"] != "a" || first["t"] != "b" {
		t.Errorf("anchor inner keys not rewritten: %v", first)
	}
}

func TestEmitTextUsesRendererAndDropsChrome(t *testing.T) {
	env := New("recall", "hello")
	var buf bytes.Buffer
	if err := Emit(&buf, env, FormatText, func(w io.Writer, data any) error {
		s, _ := data.(string)
		_, err := w.Write([]byte(s))
		return err
	}); err != nil {
		t.Fatalf("Emit text: %v", err)
	}
	got := buf.String()
	if got != "hello" {
		t.Errorf("text output = %q, want %q", got, "hello")
	}
}

func TestEmitTextOnErrorPrintsErrors(t *testing.T) {
	env := NewError("recall",
		Note{Code: "NOT_FOUND", Message: "no hits", Hint: "try a broader query"},
	)
	var buf bytes.Buffer
	if err := Emit(&buf, env, FormatText, nil); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "error: no hits") {
		t.Errorf("expected error line, got: %s", got)
	}
	if !strings.Contains(got, "hint: try a broader query") {
		t.Errorf("expected hint line, got: %s", got)
	}
}

func TestParseFormat(t *testing.T) {
	cases := []struct {
		in      string
		want    Format
		wantErr bool
	}{
		{"text", FormatText, false},
		{"json", FormatJSON, false},
		{"compact", FormatCompact, false},
		{"JSON", FormatJSON, false},
		{" Compact ", FormatCompact, false},
		{"bogus", FormatText, true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := ParseFormat(c.in)
			if (err != nil) != c.wantErr {
				t.Fatalf("ParseFormat(%q) err=%v, wantErr=%v", c.in, err, c.wantErr)
			}
			if !c.wantErr && got != c.want {
				t.Errorf("ParseFormat(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
