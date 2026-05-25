// Package graft implements the graft operation: turning a reviewed spore into
// canonical knowledge by applying its proposed_writes and proposed_edges.
//
// v0.1.1 scope:
//   - Supported write kinds: append_section, insert_after.
//   - Unsupported: replace_block, create_file, add_tag — returned as SkippedWrites.
//   - mdpp.fmt is NOT run after edits (v0.1.2 TODO: wire in mdpp formatter).
//   - Receipts are returned, not persisted; the Phase 2 integrator persists them.
package graft

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/odvcencio/hyphae/internal/types"
	"github.com/odvcencio/mdpp"
)

// Result describes the outcome of a graft.
type Result struct {
	SporeID        string
	NewSporeStatus string // "accepted" | "partial"
	AppliedWrites  []AppliedWrite
	SkippedWrites  []SkippedWrite
	AppliedEdges   []types.Edge
	TouchedFiles   []string // absolute paths of canonical files modified
	Receipt        types.Receipt
}

// AppliedWrite records where a proposed write landed.
type AppliedWrite struct {
	Kind       string     // e.g. "append_section"
	TargetURI  string
	TargetFile string    // absolute path
	InsertedAt mdpp.Range // byte range of the inserted content
}

// SkippedWrite records a proposed write that was not applied.
type SkippedWrite struct {
	Kind      string
	TargetURI string
	Reason    string
}

// unsupportedWriteKinds lists kinds deferred to v0.1.2.
var unsupportedWriteKinds = map[string]bool{
	"replace_block": true,
	"create_file":   true,
	"add_tag":       true,
}

// Apply loads spore <sporeID> from <spaceRoot>/inbox/agents/, applies its
// proposed_writes and proposed_edges to canonical files under installRoot
// (which is the directory containing spaces/), and returns the Result.
//
// grafter is the identity URI of the human or agent invoking the graft;
// it is recorded in the receipt and on each derived_from edge.
//
// On any unrecoverable error mid-graft, Apply MUST roll back any file
// writes it has made and return without persisting partial state.
func Apply(conn *sql.DB, installRoot, spaceRoot, sporeID, grafter string) (Result, error) {
	// ── Step 1: locate and read the spore file ──────────────────────────────
	sporeFile, sporeBytes, err := findSpore(spaceRoot, sporeID)
	if err != nil {
		return Result{}, fmt.Errorf("graft: find spore %q: %w", sporeID, err)
	}

	// ── Step 2: parse the spore frontmatter ─────────────────────────────────
	spore, err := parseSpore(sporeBytes)
	if err != nil {
		return Result{}, fmt.Errorf("graft: parse spore %q: %w", sporeID, err)
	}
	if spore.Status != "unreviewed" {
		return Result{}, fmt.Errorf("graft: spore %q has status %q; only 'unreviewed' spores can be grafted", sporeID, spore.Status)
	}

	now := time.Now().UTC()

	// Track rollback state: original bytes for each modified canonical file.
	rollback := map[string][]byte{}
	// Ensure rollback on unrecoverable error.
	needRollback := true
	defer func() {
		if needRollback {
			for path, orig := range rollback {
				_ = os.WriteFile(path, orig, 0o644)
			}
		}
	}()

	var (
		appliedWrites []AppliedWrite
		skippedWrites []SkippedWrite
		touchedFiles  []string
		appliedEdges  []types.Edge
	)

	// ── Step 3-4: process proposed_writes ───────────────────────────────────
	for _, pw := range spore.ProposedWrites {
		// Unsupported kinds.
		if unsupportedWriteKinds[pw.Kind] {
			skippedWrites = append(skippedWrites, SkippedWrite{
				Kind:      pw.Kind,
				TargetURI: pw.Target,
				Reason:    "unsupported write kind in v0.1.1",
			})
			continue
		}

		if pw.Kind != "append_section" && pw.Kind != "insert_after" {
			skippedWrites = append(skippedWrites, SkippedWrite{
				Kind:      pw.Kind,
				TargetURI: pw.Target,
				Reason:    fmt.Sprintf("unknown write kind %q", pw.Kind),
			})
			continue
		}

		// Resolve target URI → canonical file path + anchor slug.
		targetFile, anchorSlug, canonicalURI, err := resolveTarget(installRoot, pw.Target)
		if err != nil {
			skippedWrites = append(skippedWrites, SkippedWrite{
				Kind:      pw.Kind,
				TargetURI: pw.Target,
				Reason:    fmt.Sprintf("target resolution failed: %v", err),
			})
			continue
		}

		// Check file existence.
		if _, statErr := os.Stat(targetFile); os.IsNotExist(statErr) {
			skippedWrites = append(skippedWrites, SkippedWrite{
				Kind:      pw.Kind,
				TargetURI: pw.Target,
				Reason:    "target file not found",
			})
			continue
		}

		// Read original file (save for rollback before first modification).
		origBytes, readErr := os.ReadFile(targetFile)
		if readErr != nil {
			skippedWrites = append(skippedWrites, SkippedWrite{
				Kind:      pw.Kind,
				TargetURI: pw.Target,
				Reason:    fmt.Sprintf("read target file: %v", readErr),
			})
			continue
		}
		if _, saved := rollback[targetFile]; !saved {
			rollback[targetFile] = origBytes
		}

		// Build the text to insert.
		insertText, buildErr := buildInsertText(pw)
		if buildErr != nil {
			skippedWrites = append(skippedWrites, SkippedWrite{
				Kind:      pw.Kind,
				TargetURI: pw.Target,
				Reason:    fmt.Sprintf("build insert text: %v", buildErr),
			})
			continue
		}

		// Locate heading and compute insertion byte offset.
		insertOffset, locErr := locateInsertionPoint(origBytes, anchorSlug, pw.Kind)
		if locErr != nil {
			skippedWrites = append(skippedWrites, SkippedWrite{
				Kind:      pw.Kind,
				TargetURI: pw.Target,
				Reason:    fmt.Sprintf("locate insertion point: %v", locErr),
			})
			continue
		}

		// Splice the text in.
		newBytes := spliceBytes(origBytes, insertOffset, insertText)

		// Re-parse to verify document still parses cleanly.
		if _, parseErr := mdpp.Parse(newBytes); parseErr != nil {
			skippedWrites = append(skippedWrites, SkippedWrite{
				Kind:      pw.Kind,
				TargetURI: pw.Target,
				Reason:    fmt.Sprintf("post-edit re-parse failed: %v; rolled back", parseErr),
			})
			// Restore from rollback map — we already saved the original.
			_ = os.WriteFile(targetFile, rollback[targetFile], 0o644)
			// Remove from rollback so defer doesn't double-write it.
			delete(rollback, targetFile)
			continue
		}

		// Write the modified file.
		if writeErr := os.WriteFile(targetFile, newBytes, 0o644); writeErr != nil {
			// Return an unrecoverable error; defer will roll back.
			return Result{}, fmt.Errorf("graft: write canonical file %s: %w", targetFile, writeErr)
		}

		// Track the inserted range.
		insertRange := mdpp.Range{
			StartByte: insertOffset,
			EndByte:   insertOffset + len(insertText),
		}

		aw := AppliedWrite{
			Kind:       pw.Kind,
			TargetURI:  pw.Target,
			TargetFile: targetFile,
			InsertedAt: insertRange,
		}
		appliedWrites = append(appliedWrites, aw)

		// Mark as touched (deduplicate).
		if !containsString(touchedFiles, targetFile) {
			touchedFiles = append(touchedFiles, targetFile)
		}

		// ── Step 5: record derived_from edge for this write ──────────────────
		edge := types.Edge{
			ID:          fmt.Sprintf("edge:derived_from:%s->%s", canonicalURI, sporeID),
			Kind:        types.EdgeDerivedFrom,
			SrcID:       canonicalURI,
			DstID:       sporeID,
			Confidence:  1.0,
			Derivation:  "graft",
			AgentSource: spore.AgentID,
			CreatedBy:   grafter,
			CreatedAt:   now,
		}
		if dbErr := persistEdge(conn, edge); dbErr != nil {
			// Return unrecoverable; defer will roll back.
			return Result{}, fmt.Errorf("graft: persist derived_from edge: %w", dbErr)
		}
		appliedEdges = append(appliedEdges, edge)
	}

	// ── Step 6: process proposed_edges ──────────────────────────────────────
	for _, pe := range spore.ProposedEdges {
		edge := types.Edge{
			ID:          fmt.Sprintf("edge:%s:%s->%s:%s", pe.Kind, pe.SrcID, pe.DstID, uuid.New().String()[:8]),
			Kind:        pe.Kind,
			SrcID:       pe.SrcID,
			DstID:       pe.DstID,
			Confidence:  pe.Confidence,
			Derivation:  "graft",
			AgentSource: spore.AgentID,
			CreatedBy:   grafter,
			CreatedAt:   now,
		}
		if dbErr := persistEdge(conn, edge); dbErr != nil {
			return Result{}, fmt.Errorf("graft: persist proposed edge: %w", dbErr)
		}
		appliedEdges = append(appliedEdges, edge)
	}

	// ── Step 7: determine final spore status ─────────────────────────────────
	// Convention: if zero proposed_writes were in the spore, status = "accepted"
	// (edges-only graft is a valid full success). If there were proposed_writes
	// and ALL were skipped (zero applied), status = "partial" to flag that no
	// canonical writes landed — the caller can promote to "rejected" if desired.
	// If at least one write applied and some were skipped, status = "partial".
	// If all proposed_writes applied, status = "accepted".
	newStatus := computeStatus(len(spore.ProposedWrites), len(appliedWrites))

	// Update the spore frontmatter on disk.
	updatedSporeBytes, updateErr := updateSporeStatus(sporeBytes, newStatus)
	if updateErr != nil {
		return Result{}, fmt.Errorf("graft: update spore status: %w", updateErr)
	}
	if writeErr := os.WriteFile(sporeFile, updatedSporeBytes, 0o644); writeErr != nil {
		return Result{}, fmt.Errorf("graft: write spore file: %w", writeErr)
	}

	// ── Step 8: build receipt ────────────────────────────────────────────────
	hash := sha256.Sum256(updatedSporeBytes)
	contentHash := fmt.Sprintf("%x", hash[:])

	// Determine subject kind from grafter URI prefix.
	subjectKind := "human"
	if strings.HasPrefix(grafter, "agent://") || strings.HasPrefix(grafter, "service://") {
		subjectKind = "agent"
	}

	receipt := types.Receipt{
		ID:              fmt.Sprintf("hypha-receipt:graft:%s:%s", now.Format("2006-01-02"), uuid.New().String()[:8]),
		SpaceID:         spore.SpaceID,
		SubjectID:       grafter,
		SubjectKind:     subjectKind,
		Action:          "graft",
		Status:          newStatus,
		ContentHash:     contentHash,
		CreatedAt:       now,
		PermissionsUsed: []string{"canonical:write"},
		NextState:       "canonical",
	}

	// Success: disable rollback.
	needRollback = false

	return Result{
		SporeID:        sporeID,
		NewSporeStatus: newStatus,
		AppliedWrites:  appliedWrites,
		SkippedWrites:  skippedWrites,
		AppliedEdges:   appliedEdges,
		TouchedFiles:   touchedFiles,
		Receipt:        receipt,
	}, nil
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// findSpore walks spaceRoot/inbox/agents/ looking for a .md file whose
// frontmatter id: matches sporeID. Returns the absolute path and file bytes.
func findSpore(spaceRoot, sporeID string) (string, []byte, error) {
	inboxDir := filepath.Join(spaceRoot, "inbox", "agents")
	entries, err := os.ReadDir(inboxDir)
	if err != nil {
		return "", nil, fmt.Errorf("read inbox dir %s: %w", inboxDir, err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		path := filepath.Join(inboxDir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		doc, err := mdpp.Parse(data)
		if err != nil {
			continue
		}
		fm := doc.Frontmatter()
		if fm == nil {
			continue
		}
		id, _ := fm["id"].(string)
		if id == sporeID {
			return path, data, nil
		}
	}
	return "", nil, fmt.Errorf("spore %q not found in %s", sporeID, inboxDir)
}

// sporeData holds the minimal fields we need from a parsed spore.
type sporeData struct {
	SpaceID        string
	AgentID        string
	Status         string
	ProposedWrites []types.ProposedWrite
	ProposedEdges  []types.ProposedEdge
}

// parseSpore extracts sporeData from raw bytes.
func parseSpore(data []byte) (sporeData, error) {
	doc, err := mdpp.Parse(data)
	if err != nil {
		return sporeData{}, fmt.Errorf("mdpp.Parse: %w", err)
	}
	fm := doc.Frontmatter()
	if fm == nil {
		return sporeData{}, fmt.Errorf("no frontmatter")
	}

	spaceID, _ := fm["space"].(string)
	status, _ := fm["status"].(string)

	var agentID string
	if agentBlock, ok := fm["agent"].(map[string]any); ok {
		agentID, _ = agentBlock["id"].(string)
	}

	var proposedWrites []types.ProposedWrite
	if rawWrites, ok := fm["proposed_writes"]; ok && rawWrites != nil {
		proposedWrites = parseFrontmatterWrites(rawWrites)
	}

	var proposedEdges []types.ProposedEdge
	if rawEdges, ok := fm["proposed_edges"]; ok && rawEdges != nil {
		proposedEdges = parseFrontmatterEdges(rawEdges)
	}

	return sporeData{
		SpaceID:        spaceID,
		AgentID:        agentID,
		Status:         status,
		ProposedWrites: proposedWrites,
		ProposedEdges:  proposedEdges,
	}, nil
}

// parseFrontmatterWrites converts raw YAML to []types.ProposedWrite.
func parseFrontmatterWrites(raw any) []types.ProposedWrite {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []types.ProposedWrite
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := m["kind"].(string)
		target, _ := m["target"].(string)
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
	return out
}

// parseFrontmatterEdges converts raw YAML to []types.ProposedEdge.
func parseFrontmatterEdges(raw any) []types.ProposedEdge {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []types.ProposedEdge
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := m["kind"].(string)
		src, _ := m["src"].(string)
		dst, _ := m["dst"].(string)
		var conf float64
		switch v := m["confidence"].(type) {
		case float64:
			conf = v
		case int:
			conf = float64(v)
		}
		out = append(out, types.ProposedEdge{
			SrcID:      src,
			DstID:      dst,
			Kind:       types.EdgeKind(kind),
			Confidence: conf,
			Status:     "pending",
		})
	}
	return out
}

// resolveTarget parses a hypha:// target URI and returns:
//   - absolute path to the canonical file
//   - anchor slug (after #)
//   - canonical URI (with anchor) for edge recording
//
// URI format: hypha://<authority>/<name>/<path>#<anchor>
// The file lives at: installRoot/spaces/<authority>-<name>/<path>.md
func resolveTarget(installRoot, targetURI string) (filePath, anchorSlug, canonicalURI string, err error) {
	if !strings.HasPrefix(targetURI, "hypha://") {
		return "", "", "", fmt.Errorf("target URI must start with hypha://, got %q", targetURI)
	}
	rest := strings.TrimPrefix(targetURI, "hypha://")

	// Split anchor.
	anchor := ""
	if idx := strings.LastIndex(rest, "#"); idx >= 0 {
		anchor = rest[idx+1:]
		rest = rest[:idx]
	}

	// rest = "<authority>/<name>/<path...>"
	// First two path components are authority and name.
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 3 {
		return "", "", "", fmt.Errorf("target URI must have authority/name/path, got %q", targetURI)
	}
	authority := parts[0]
	name := parts[1]
	filePart := parts[2] // e.g. "concepts/spore"

	spaceDir := fmt.Sprintf("%s-%s", authority, name) // e.g. "m31labs-hyphae"
	absFile := filepath.Join(installRoot, "spaces", spaceDir, filePart+".md")

	// Canonical URI without extension, with anchor.
	uriPath := fmt.Sprintf("%s/%s/%s", authority, name, filePart)
	canonicalURI = fmt.Sprintf("hypha://%s", uriPath)
	if anchor != "" {
		canonicalURI += "#" + anchor
	}

	return absFile, anchor, canonicalURI, nil
}

// buildInsertText constructs the text to insert for a proposed write.
//
// Payload fields:
//   - "body": raw markdown text to insert (used as-is, with trailing newline)
//   - "heading": if present (and body absent), generate a new H2 with that text
func buildInsertText(pw types.ProposedWrite) (string, error) {
	if body, ok := pw.Payload["body"].(string); ok && body != "" {
		// Ensure the text ends with a newline.
		if !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		return body, nil
	}
	if heading, ok := pw.Payload["heading"].(string); ok && heading != "" {
		return fmt.Sprintf("\n## %s\n", heading), nil
	}
	return "", fmt.Errorf("proposed_write payload must have 'body' or 'heading'")
}

// locateInsertionPoint finds the byte offset at which to insert content for
// the given anchor slug and write kind in the source document.
//
// For "append_section": inserts at the end of the heading's section (start of
// the next sibling heading at the same or higher level, or end of file).
//
// For "insert_after": inserts immediately after the heading node itself.
func locateInsertionPoint(src []byte, anchorSlug, kind string) (int, error) {
	doc, err := mdpp.Parse(src)
	if err != nil {
		return 0, fmt.Errorf("mdpp.Parse: %w", err)
	}

	headings := doc.AST().Find(mdpp.NodeHeading)
	if len(headings) == 0 {
		return 0, fmt.Errorf("no headings found in target file")
	}

	// If no anchor specified, append at end of file.
	if anchorSlug == "" {
		return len(src), nil
	}

	// Find the target heading by slug.
	targetIdx := -1
	for i, h := range headings {
		if slugify(strings.TrimSpace(h.Text())) == anchorSlug {
			targetIdx = i
			break
		}
	}
	if targetIdx < 0 {
		return 0, fmt.Errorf("anchor %q not found in target file", anchorSlug)
	}

	targetHeading := headings[targetIdx]
	targetLevel := targetHeading.Level()

	switch kind {
	case "insert_after":
		// Insert right after the heading node.
		return targetHeading.Range.EndByte, nil

	case "append_section":
		// Find the next heading at the same or higher (lower number) level.
		for i := targetIdx + 1; i < len(headings); i++ {
			if headings[i].Level() <= targetLevel {
				// Insert just before this heading.
				return headings[i].Range.StartByte, nil
			}
		}
		// No such heading found; append at end of file.
		return len(src), nil
	}

	return 0, fmt.Errorf("unhandled kind %q in locateInsertionPoint", kind)
}

// spliceBytes inserts insertText at offset in src, returning the new []byte.
func spliceBytes(src []byte, offset int, insertText string) []byte {
	insert := []byte(insertText)
	result := make([]byte, 0, len(src)+len(insert))
	result = append(result, src[:offset]...)
	result = append(result, insert...)
	result = append(result, src[offset:]...)
	return result
}

// statusLineRe matches a status: line anywhere in frontmatter.
// We constrain the replacement to within the frontmatter byte range.
var statusLineRe = regexp.MustCompile(`(?m)^(status:\s*)\S+(\s*)$`)

// updateSporeStatus rewrites the status: field in the spore's frontmatter.
// It locates the NodeFrontmatter range, applies the regex only within that
// slice, and splices it back.
func updateSporeStatus(src []byte, newStatus string) ([]byte, error) {
	doc, err := mdpp.Parse(src)
	if err != nil {
		return nil, fmt.Errorf("mdpp.Parse spore: %w", err)
	}

	// Find frontmatter byte range.
	fmStart, fmEnd := 0, 0
	for _, child := range doc.AST().Children {
		if child.Type == mdpp.NodeFrontmatter {
			fmStart = child.Range.StartByte
			fmEnd = child.Range.EndByte
			break
		}
	}
	if fmEnd == 0 {
		return nil, fmt.Errorf("no frontmatter node found in spore")
	}

	fmSlice := src[fmStart:fmEnd]
	newFM := statusLineRe.ReplaceAll(fmSlice, []byte("${1}"+newStatus+"${2}"))
	if string(newFM) == string(fmSlice) {
		return nil, fmt.Errorf("status: line not found in frontmatter")
	}

	// Splice updated frontmatter back.
	result := make([]byte, 0, len(src)+len(newFM)-len(fmSlice))
	result = append(result, src[:fmStart]...)
	result = append(result, newFM...)
	result = append(result, src[fmEnd:]...)
	return result, nil
}

// computeStatus derives the new spore status from write counts.
//
// Convention:
//   - 0 proposed_writes → all edge-only: "accepted"
//   - N proposed_writes, all applied → "accepted"
//   - N proposed_writes, at least one applied, at least one skipped → "partial"
//   - N proposed_writes, NONE applied → "partial" (partial is the closest
//     first-class outcome for zero-write grafts; the spec does not define
//     "rejected" as an Apply outcome — rejection is a separate operation).
func computeStatus(totalWrites, appliedWrites int) string {
	if totalWrites == 0 || totalWrites == appliedWrites {
		return "accepted"
	}
	return "partial"
}

// persistEdge writes one edge row to the edges table.
func persistEdge(conn *sql.DB, e types.Edge) error {
	_, err := conn.Exec(`
		INSERT OR IGNORE INTO edges
			(id, kind, src_id, dst_id, confidence, derivation, agent_source, created_by, created_at, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID,
		string(e.Kind),
		e.SrcID,
		e.DstID,
		e.Confidence,
		e.Derivation,
		nullableString(e.AgentSource),
		nullableString(e.CreatedBy),
		e.CreatedAt.UTC().Format(time.RFC3339),
		nil,
	)
	return err
}

// slugify converts a heading text to a URL-safe slug matching parser.slugify.
// Lowercase; runs of non-alphanumeric → single "-"; trim edges.
func slugify(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	inDash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			inDash = false
		} else {
			if !inDash {
				b.WriteByte('-')
				inDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// containsString reports whether s is in the slice.
func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// nullableString converts "" to nil for SQL NULLs.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
