// Package spore validates and writes mdpp spore documents to a space inbox.
//
// A spore is a portable, source-grounded knowledge contribution submitted by
// an ephemeral agent. This package handles v0.1 parse, validate, and write;
// signing (Ed25519) and graph-index integration are deferred to v0.1.1.
package spore

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/odvcencio/hyphae/internal/types"
	"github.com/odvcencio/mdpp"
)

// ValidationError describes one required-field violation.
type ValidationError struct {
	Field   string // dotted path, e.g. "agent.id"
	Message string
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("spore validation error: field %q: %s", e.Field, e.Message)
}

// tokenCap is the hard limit on estimated body tokens.
const tokenCap = 5000

// valid status values for a spore.
var validStatuses = map[string]bool{
	"unreviewed": true,
	"accepted":   true,
	"partial":    true,
	"rejected":   true,
	"duplicate":  true,
	"superseded": true,
	"archived":   true,
}

// valid confidence values.
var validConfidence = map[string]bool{
	"low":    true,
	"medium": true,
	"high":   true,
}

// valid agent.kind values.
var validAgentKinds = map[string]bool{
	"ephemeral":  true,
	"persistent": true,
	"ci":         true,
	"human":      true,
	"service":    true,
}

// valid agent URI prefixes.
var agentURIPrefixes = []string{"agent://", "identity://", "service://"}

// valid proposed_write kind values.
var validWriteKinds = map[string]bool{
	"append_section": true,
	"insert_after":   true,
	"replace_block":  true,
	"create_file":    true,
	"add_tag":        true,
}

// valid proposed_edge kind values.
var validEdgeKinds = map[string]bool{
	"supports":     true,
	"derived_from": true,
	"applies_to":   true,
	"blocks":       true,
	"cites":        true,
}

// Parse parses a raw mdpp spore document and returns the typed Spore plus any
// validation errors. A non-empty []ValidationError means the spore is not
// submittable as-is.
func Parse(source []byte) (types.Spore, []ValidationError) {
	doc, err := mdpp.Parse(source)
	if err != nil {
		return types.Spore{}, []ValidationError{{Field: "document", Message: fmt.Sprintf("mdpp parse failed: %v", err)}}
	}

	fm := doc.Frontmatter()
	if fm == nil {
		return types.Spore{}, []ValidationError{{Field: "frontmatter", Message: "no frontmatter block found"}}
	}

	var errs []ValidationError

	addErr := func(field, msg string) {
		errs = append(errs, ValidationError{Field: field, Message: msg})
	}

	// ── id ───────────────────────────────────────────────────────────────────
	id := stringField(fm, "id")
	if id == "" {
		addErr("id", "required field missing")
	} else if !strings.HasPrefix(id, "spore.") {
		addErr("id", fmt.Sprintf("must start with \"spore.\", got %q", id))
	}

	// ── type ─────────────────────────────────────────────────────────────────
	typVal := stringField(fm, "type")
	if typVal == "" {
		addErr("type", "required field missing")
	} else if typVal != "spore" {
		addErr("type", fmt.Sprintf("must be \"spore\", got %q", typVal))
	}

	// ── space ─────────────────────────────────────────────────────────────────
	space := stringField(fm, "space")
	if space == "" {
		addErr("space", "required field missing")
	} else if !strings.HasPrefix(space, "hypha://") {
		addErr("space", fmt.Sprintf("must be a hypha:// URI, got %q", space))
	}

	// ── status ────────────────────────────────────────────────────────────────
	status := stringField(fm, "status")
	if status == "" {
		addErr("status", "required field missing")
	} else if !validStatuses[status] {
		addErr("status", fmt.Sprintf("invalid value %q; must be one of: unreviewed, accepted, partial, rejected, duplicate, superseded, archived", status))
	}

	// ── created ───────────────────────────────────────────────────────────────
	// YAML parsers (gopkg.in/yaml.v3) auto-convert ISO 8601 timestamps to
	// time.Time, so we must handle both string and time.Time here.
	var createdAt time.Time
	switch v := fm["created"].(type) {
	case time.Time:
		createdAt = v.UTC()
	case string:
		if v == "" {
			addErr("created", "required field missing")
		} else {
			t, parseErr := time.Parse(time.RFC3339, v)
			if parseErr != nil {
				addErr("created", fmt.Sprintf("must be ISO 8601 (RFC3339), got %q: %v", v, parseErr))
			} else {
				createdAt = t.UTC()
			}
		}
	case nil:
		addErr("created", "required field missing")
	default:
		addErr("created", fmt.Sprintf("must be ISO 8601 (RFC3339) timestamp, got %T", v))
	}

	// ── agent block ───────────────────────────────────────────────────────────
	var agentID, agentKind, agentModel, taskID, runID string
	agentBlock, agentOK := fm["agent"].(map[string]any)
	if !agentOK || agentBlock == nil {
		addErr("agent", "required block missing")
	} else {
		agentID = stringField(agentBlock, "id")
		if agentID == "" {
			addErr("agent.id", "required field missing")
		} else if !hasAnyPrefix(agentID, agentURIPrefixes) {
			addErr("agent.id", fmt.Sprintf("must start with agent://, identity://, or service://; got %q", agentID))
		}

		agentKind = stringField(agentBlock, "kind")
		if agentKind == "" {
			addErr("agent.kind", "required field missing")
		} else if !validAgentKinds[agentKind] {
			addErr("agent.kind", fmt.Sprintf("invalid value %q; must be one of: ephemeral, persistent, ci, human, service", agentKind))
		}

		agentModel = stringField(agentBlock, "model") // optional
		taskID = stringField(agentBlock, "task_id")   // optional
		runID = stringField(agentBlock, "run_id")     // optional
	}

	// ── confidence ────────────────────────────────────────────────────────────
	confidence := stringField(fm, "confidence")
	if confidence == "" {
		addErr("confidence", "required field missing")
	} else if !validConfidence[confidence] {
		addErr("confidence", fmt.Sprintf("invalid value %q; must be one of: low, medium, high", confidence))
	}

	// ── source_refs ───────────────────────────────────────────────────────────
	sourceRefs := stringSliceField(fm, "source_refs")
	if len(sourceRefs) == 0 {
		addErr("source_refs", "required field missing; at least one source_ref is required")
	}

	// ── proposed_writes (optional) ────────────────────────────────────────────
	var proposedWrites []types.ProposedWrite
	if rawWrites, ok := fm["proposed_writes"]; ok && rawWrites != nil {
		writes, writeErrs := parseProposedWrites(rawWrites)
		errs = append(errs, writeErrs...)
		proposedWrites = writes
	}

	// ── proposed_edges (optional) ─────────────────────────────────────────────
	var proposedEdges []types.ProposedEdge
	if rawEdges, ok := fm["proposed_edges"]; ok && rawEdges != nil {
		edges, edgeErrs := parseProposedEdges(rawEdges)
		errs = append(errs, edgeErrs...)
		proposedEdges = edges
	}

	// ── body ──────────────────────────────────────────────────────────────────
	body := extractBody(doc)

	// ── token estimate ────────────────────────────────────────────────────────
	tokenCount := len(body) / 4

	s := types.Spore{
		ID:             id,
		SpaceID:        space,
		Status:         status,
		AgentID:        agentID,
		AgentKind:      agentKind,
		AgentModel:     agentModel,
		TaskID:         taskID,
		RunID:          runID,
		Confidence:     confidence,
		SourceRefs:     sourceRefs,
		ProposedWrites: proposedWrites,
		ProposedEdges:  proposedEdges,
		Body:           body,
		TokenCount:     tokenCount,
		SubmittedAt:    createdAt,
	}

	return s, errs
}

// Submit writes a validated spore to inbox/agents/ inside the given space
// root. The filename is derived from the spore id. Returns the persisted file
// path and a Receipt.
//
// Submit re-validates and refuses to write if validation errors exist.
// Submit also enforces a hard cap of 5000 estimated tokens on body size.
func Submit(s types.Spore, spaceRoot string) (filePath string, r types.Receipt, err error) {
	// Re-validate struct fields before writing.
	structErrs := validateStruct(s)
	if len(structErrs) > 0 {
		msgs := make([]string, len(structErrs))
		for i, e := range structErrs {
			msgs[i] = e.Error()
		}
		return "", types.Receipt{}, fmt.Errorf("spore failed validation: %s", strings.Join(msgs, "; "))
	}

	// Token cap.
	if s.TokenCount > tokenCap {
		return "", types.Receipt{}, fmt.Errorf("spore body exceeds hard cap (5000 tokens estimated)")
	}

	// Derive filename.
	filename := sporeFilename(s)

	// Ensure inbox directory exists.
	inboxDir := filepath.Join(spaceRoot, "inbox", "agents")
	if mkErr := os.MkdirAll(inboxDir, 0o755); mkErr != nil {
		return "", types.Receipt{}, fmt.Errorf("failed to create inbox directory: %w", mkErr)
	}

	filePath = filepath.Join(inboxDir, filename)

	// Guard against duplicates.
	if _, statErr := os.Stat(filePath); statErr == nil {
		return "", types.Receipt{}, fmt.Errorf("spore already exists at %s", filePath)
	}

	// Reconstruct document bytes (frontmatter + body).
	fileBytes := reconstructSource(s)

	// Write file.
	if writeErr := os.WriteFile(filePath, fileBytes, 0o644); writeErr != nil {
		return "", types.Receipt{}, fmt.Errorf("failed to write spore file: %w", writeErr)
	}

	// Compute content hash.
	hash := sha256.Sum256(fileBytes)
	contentHash := fmt.Sprintf("%x", hash[:])

	// Build receipt.
	shortID := sporeShortID(s.ID)
	dateStr := s.SubmittedAt.Format("2006-01-02")

	r = types.Receipt{
		ID:              fmt.Sprintf("hypha-receipt:%s:%s", dateStr, shortID),
		SpaceID:         s.SpaceID,
		SubjectID:       s.AgentID,
		SubjectKind:     "agent",
		Action:          "spore:create",
		Status:          "accepted_for_review",
		ContentHash:     contentHash,
		CreatedAt:       time.Now().UTC(),
		PermissionsUsed: []string{"spore:create"},
		NextState:       "review",
	}

	s.ContentHash = contentHash
	s.FilePath = filePath
	s.ReceiptID = r.ID

	return filePath, r, nil
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// stringField safely retrieves a string value from a map.
func stringField(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

// stringSliceField retrieves a []string from a frontmatter map field.
// The YAML parser may return []any or []string.
func stringSliceField(m map[string]any, key string) []string {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// hasAnyPrefix checks if s has at least one of the given prefixes.
func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// extractBody returns the body text (everything after the frontmatter block).
func extractBody(doc *mdpp.Document) string {
	if doc == nil {
		return ""
	}
	if doc.Root == nil {
		return ""
	}
	// Find the frontmatter node end byte.
	fmEnd := 0
	for _, child := range doc.Root.Children {
		if child != nil && child.Type.String() == "Frontmatter" {
			fmEnd = child.Range.EndByte
			break
		}
	}
	if fmEnd == 0 {
		// No frontmatter: return entire source.
		return string(doc.Source)
	}
	if fmEnd >= len(doc.Source) {
		return ""
	}
	body := doc.Source[fmEnd:]
	// Strip leading newline(s) after frontmatter delimiter.
	body = bytes.TrimLeft(body, "\n")
	return string(body)
}

// parseProposedWrites converts raw YAML []any to []types.ProposedWrite.
func parseProposedWrites(raw any) ([]types.ProposedWrite, []ValidationError) {
	list, ok := raw.([]any)
	if !ok {
		return nil, []ValidationError{{Field: "proposed_writes", Message: "must be a list"}}
	}

	var out []types.ProposedWrite
	var errs []ValidationError

	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			errs = append(errs, ValidationError{
				Field:   fmt.Sprintf("proposed_writes[%d]", i),
				Message: "each entry must be a mapping",
			})
			continue
		}
		kind := stringField(m, "kind")
		if kind == "" {
			errs = append(errs, ValidationError{
				Field:   fmt.Sprintf("proposed_writes[%d].kind", i),
				Message: "required field missing",
			})
			continue
		}
		if !validWriteKinds[kind] {
			errs = append(errs, ValidationError{
				Field:   fmt.Sprintf("proposed_writes[%d].kind", i),
				Message: fmt.Sprintf("invalid kind %q; must be one of: append_section, insert_after, replace_block, create_file, add_tag", kind),
			})
		}
		target := stringField(m, "target")
		// Build payload from remaining fields.
		payload := make(map[string]any)
		for k, v := range m {
			if k != "kind" && k != "target" {
				payload[k] = v
			}
		}
		out = append(out, types.ProposedWrite{
			Kind:    kind,
			Target:  target,
			Payload: payload,
			Status:  "pending",
		})
	}
	return out, errs
}

// parseProposedEdges converts raw YAML []any to []types.ProposedEdge.
func parseProposedEdges(raw any) ([]types.ProposedEdge, []ValidationError) {
	list, ok := raw.([]any)
	if !ok {
		return nil, []ValidationError{{Field: "proposed_edges", Message: "must be a list"}}
	}

	var out []types.ProposedEdge
	var errs []ValidationError

	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			errs = append(errs, ValidationError{
				Field:   fmt.Sprintf("proposed_edges[%d]", i),
				Message: "each entry must be a mapping",
			})
			continue
		}
		kind := stringField(m, "kind")
		if kind == "" {
			errs = append(errs, ValidationError{
				Field:   fmt.Sprintf("proposed_edges[%d].kind", i),
				Message: "required field missing",
			})
			continue
		}
		if !validEdgeKinds[kind] {
			errs = append(errs, ValidationError{
				Field:   fmt.Sprintf("proposed_edges[%d].kind", i),
				Message: fmt.Sprintf("invalid kind %q; must be one of: supports, derived_from, applies_to, blocks, cites", kind),
			})
		}
		src := stringField(m, "src")
		dst := stringField(m, "dst")
		conf := floatField(m, "confidence")
		out = append(out, types.ProposedEdge{
			SrcID:      src,
			DstID:      dst,
			Kind:       types.EdgeKind(kind),
			Confidence: conf,
			Status:     "pending",
		})
	}
	return out, errs
}

// floatField safely retrieves a float64 from a map.
func floatField(m map[string]any, key string) float64 {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch f := v.(type) {
	case float64:
		return f
	case int:
		return float64(f)
	}
	return 0
}

// validateStruct validates required fields on an already-parsed Spore struct.
// This mirrors the Parse validation but operates on the struct directly for
// Submit's re-validation step.
func validateStruct(s types.Spore) []ValidationError {
	var errs []ValidationError
	addErr := func(field, msg string) {
		errs = append(errs, ValidationError{Field: field, Message: msg})
	}
	if s.ID == "" {
		addErr("id", "required field missing")
	} else if !strings.HasPrefix(s.ID, "spore.") {
		addErr("id", fmt.Sprintf("must start with \"spore.\", got %q", s.ID))
	}
	if s.SpaceID == "" {
		addErr("space", "required field missing")
	} else if !strings.HasPrefix(s.SpaceID, "hypha://") {
		addErr("space", fmt.Sprintf("must be a hypha:// URI, got %q", s.SpaceID))
	}
	if s.Status == "" {
		addErr("status", "required field missing")
	} else if !validStatuses[s.Status] {
		addErr("status", fmt.Sprintf("invalid status %q", s.Status))
	}
	if s.AgentID == "" {
		addErr("agent.id", "required field missing")
	} else if !hasAnyPrefix(s.AgentID, agentURIPrefixes) {
		addErr("agent.id", fmt.Sprintf("must start with agent://, identity://, or service://; got %q", s.AgentID))
	}
	if s.AgentKind == "" {
		addErr("agent.kind", "required field missing")
	} else if !validAgentKinds[s.AgentKind] {
		addErr("agent.kind", fmt.Sprintf("invalid kind %q", s.AgentKind))
	}
	if s.Confidence == "" {
		addErr("confidence", "required field missing")
	} else if !validConfidence[s.Confidence] {
		addErr("confidence", fmt.Sprintf("invalid confidence %q", s.Confidence))
	}
	if len(s.SourceRefs) == 0 {
		addErr("source_refs", "at least one source_ref is required")
	}
	return errs
}

// nonAlphanumRe matches characters that are not alphanumeric.
var nonAlphanumRe = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// sporeFilename returns the filename for a spore.
// Pattern: <YYYY-MM-DD>-<slug>.md
// where slug = spore id with "spore.<date>." prefix stripped,
// then non-alphanumeric replaced with "-".
func sporeFilename(s types.Spore) string {
	dateStr := s.SubmittedAt.Format("2006-01-02")

	// Strip "spore." prefix.
	slug := strings.TrimPrefix(s.ID, "spore.")
	// Strip leading "<YYYY-MM-DD>." if present.
	if len(slug) >= 11 && slug[10] == '.' {
		_, err := time.Parse("2006-01-02", slug[:10])
		if err == nil {
			slug = slug[11:]
		}
	}
	// Replace non-alphanumeric runs with "-", then trim trailing dashes.
	slug = nonAlphanumRe.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	slug = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' {
			return r
		}
		return '-'
	}, slug)

	return fmt.Sprintf("%s-%s.md", dateStr, slug)
}

// sporeShortID returns the short identifier portion of a spore ID for use in
// receipt IDs. For "spore.2026-05-25.cloud-agent.7f3a" it returns "7f3a".
// Falls back to the full ID if the pattern doesn't match.
func sporeShortID(id string) string {
	// Remove "spore." prefix.
	rest := strings.TrimPrefix(id, "spore.")
	// Find last dot-separated segment.
	parts := strings.Split(rest, ".")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return id
}

// reconstructSource builds the mdpp document bytes from a Spore struct.
func reconstructSource(s types.Spore) []byte {
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("mdpp: \"0.1\"\n"))
	sb.WriteString(fmt.Sprintf("id: %s\n", s.ID))
	sb.WriteString("type: spore\n")
	sb.WriteString(fmt.Sprintf("space: %s\n", s.SpaceID))
	sb.WriteString(fmt.Sprintf("status: %s\n", s.Status))
	sb.WriteString(fmt.Sprintf("created: %s\n", s.SubmittedAt.Format(time.RFC3339)))
	sb.WriteString("\nagent:\n")
	sb.WriteString(fmt.Sprintf("  id: %s\n", s.AgentID))
	sb.WriteString(fmt.Sprintf("  kind: %s\n", s.AgentKind))
	if s.AgentModel != "" {
		sb.WriteString(fmt.Sprintf("  model: %s\n", s.AgentModel))
	}
	if s.RunID != "" {
		sb.WriteString(fmt.Sprintf("  run_id: %s\n", s.RunID))
	}
	if s.TaskID != "" {
		sb.WriteString(fmt.Sprintf("  task_id: %s\n", s.TaskID))
	}
	sb.WriteString(fmt.Sprintf("\nconfidence: %s\n", s.Confidence))
	if len(s.SourceRefs) > 0 {
		sb.WriteString("\nsource_refs:\n")
		for _, ref := range s.SourceRefs {
			sb.WriteString(fmt.Sprintf("  - %s\n", ref))
		}
	}
	if len(s.ProposedEdges) > 0 {
		sb.WriteString("\nproposed_edges:\n")
		for _, e := range s.ProposedEdges {
			sb.WriteString(fmt.Sprintf("  - kind: %s\n", e.Kind))
			sb.WriteString(fmt.Sprintf("    src: %s\n", e.SrcID))
			sb.WriteString(fmt.Sprintf("    dst: %s\n", e.DstID))
			sb.WriteString(fmt.Sprintf("    confidence: %.2f\n", e.Confidence))
		}
	}
	if len(s.ProposedWrites) > 0 {
		sb.WriteString("\nproposed_writes:\n")
		for _, w := range s.ProposedWrites {
			sb.WriteString(fmt.Sprintf("  - kind: %s\n", w.Kind))
			if w.Target != "" {
				sb.WriteString(fmt.Sprintf("    target: %s\n", w.Target))
			}
			if heading, ok := w.Payload["heading"].(string); ok && heading != "" {
				sb.WriteString(fmt.Sprintf("    heading: %q\n", heading))
			}
		}
	}
	sb.WriteString("---\n")
	if s.Body != "" {
		sb.WriteString("\n")
		sb.WriteString(s.Body)
	}
	return []byte(sb.String())
}
