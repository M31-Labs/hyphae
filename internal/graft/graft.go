// Package graft implements the graft operation: turning a reviewed spore into
// canonical knowledge by applying its proposed_writes and proposed_edges.
//
// v0.1.2 scope:
//   - Supported write kinds: append_section, insert_after, replace_block,
//     create_file, add_tag.
//   - mdpp.fmt is NOT run after edits (v0.1.3 TODO: wire in mdpp formatter).
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
	"m31labs.dev/hyphae/internal/types"
	"m31labs.dev/mdpp"
)

// tagsFlowRe matches an inline flow-style tags line: tags: [a, b, c]
var tagsFlowRe = regexp.MustCompile(`(?m)^(tags:\s*\[)(.*?)(\]\s*)$`)

// tagsBlockRe matches a block-list tags section: "tags:\n  - item\n" etc.
// We use a simpler approach: find the "tags:" line and its following "  - " entries.
var tagsKeyRe = regexp.MustCompile(`(?m)^tags:\s*$`)

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

// unsupportedWriteKinds lists kinds not yet implemented; empty as of v0.1.2.
var unsupportedWriteKinds = map[string]bool{}

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

		var (
			aw          *AppliedWrite
			skip        *SkippedWrite
			edgeSrc     string
			fatalErr    error
		)

		switch pw.Kind {
		case "append_section", "insert_after":
			aw, skip, edgeSrc, fatalErr = applyInsertWrite(pw, installRoot, rollback)
		case "create_file":
			aw, skip, edgeSrc, fatalErr = applyCreateFile(pw, installRoot)
		case "replace_block":
			aw, skip, edgeSrc, fatalErr = applyReplaceBlock(pw, installRoot, rollback)
		case "add_tag":
			aw, skip, edgeSrc, fatalErr = applyAddTag(pw, installRoot, rollback)
		default:
			skippedWrites = append(skippedWrites, SkippedWrite{
				Kind:      pw.Kind,
				TargetURI: pw.Target,
				Reason:    fmt.Sprintf("unknown write kind %q", pw.Kind),
			})
			continue
		}

		if fatalErr != nil {
			return Result{}, fatalErr
		}
		if skip != nil {
			skippedWrites = append(skippedWrites, *skip)
			continue
		}

		appliedWrites = append(appliedWrites, *aw)
		if !containsString(touchedFiles, aw.TargetFile) {
			touchedFiles = append(touchedFiles, aw.TargetFile)
		}

		// ── Step 5: record derived_from edge for this write ──────────────────
		edge := types.Edge{
			ID:          fmt.Sprintf("edge:derived_from:%s->%s", edgeSrc, sporeID),
			Kind:        types.EdgeDerivedFrom,
			SrcID:       edgeSrc,
			DstID:       sporeID,
			Confidence:  1.0,
			Derivation:  "graft",
			AgentSource: spore.AgentID,
			CreatedBy:   grafter,
			CreatedAt:   now,
		}
		if dbErr := persistEdge(conn, edge); dbErr != nil {
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

// ─── write-kind handlers ──────────────────────────────────────────────────────

// applyInsertWrite handles append_section and insert_after writes.
// Returns (appliedWrite, nil, edgeSrc, nil) on success,
// (nil, skippedWrite, "", nil) on a soft skip, or (nil, nil, "", err) on a
// fatal (unrecoverable) error.
func applyInsertWrite(
	pw types.ProposedWrite,
	installRoot string,
	rollback map[string][]byte,
) (*AppliedWrite, *SkippedWrite, string, error) {
	skip := func(reason string) (*AppliedWrite, *SkippedWrite, string, error) {
		return nil, &SkippedWrite{Kind: pw.Kind, TargetURI: pw.Target, Reason: reason}, "", nil
	}

	targetFile, anchorSlug, canonicalURI, err := resolveTarget(installRoot, pw.Target)
	if err != nil {
		return skip(fmt.Sprintf("target resolution failed: %v", err))
	}
	if _, statErr := os.Stat(targetFile); os.IsNotExist(statErr) {
		return skip("target file not found")
	}

	origBytes, readErr := os.ReadFile(targetFile)
	if readErr != nil {
		return skip(fmt.Sprintf("read target file: %v", readErr))
	}
	if _, saved := rollback[targetFile]; !saved {
		rollback[targetFile] = origBytes
	}

	insertText, buildErr := buildInsertText(pw)
	if buildErr != nil {
		return skip(fmt.Sprintf("build insert text: %v", buildErr))
	}

	insertOffset, locErr := locateInsertionPoint(origBytes, anchorSlug, pw.Kind)
	if locErr != nil {
		return skip(fmt.Sprintf("locate insertion point: %v", locErr))
	}

	newBytes := spliceBytes(origBytes, insertOffset, insertText)
	if _, parseErr := mdpp.Parse(newBytes); parseErr != nil {
		_ = os.WriteFile(targetFile, rollback[targetFile], 0o644)
		delete(rollback, targetFile)
		return skip(fmt.Sprintf("post-edit re-parse failed: %v; rolled back", parseErr))
	}

	if writeErr := os.WriteFile(targetFile, newBytes, 0o644); writeErr != nil {
		return nil, nil, "", fmt.Errorf("graft: write canonical file %s: %w", targetFile, writeErr)
	}

	aw := &AppliedWrite{
		Kind:       pw.Kind,
		TargetURI:  pw.Target,
		TargetFile: targetFile,
		InsertedAt: mdpp.Range{StartByte: insertOffset, EndByte: insertOffset + len(insertText)},
	}
	return aw, nil, canonicalURI, nil
}

// applyCreateFile handles the create_file write kind.
// Target URI is a space URI (hypha://<authority>/<name>); the actual path
// comes from payload["path"]. payload["body"] is the full file content.
func applyCreateFile(
	pw types.ProposedWrite,
	installRoot string,
) (*AppliedWrite, *SkippedWrite, string, error) {
	skip := func(reason string) (*AppliedWrite, *SkippedWrite, string, error) {
		return nil, &SkippedWrite{Kind: pw.Kind, TargetURI: pw.Target, Reason: reason}, "", nil
	}

	relPath, _ := pw.Payload["path"].(string)
	if relPath == "" {
		return skip("create_file payload missing 'path'")
	}
	body, _ := pw.Payload["body"].(string)
	if body == "" {
		return skip("create_file payload missing 'body'")
	}
	// Ensure body ends with a newline.
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}

	// Resolve space root directory from the space URI.
	// URI format: hypha://<authority>/<name>  (no path component)
	spaceDir, authority, name, resolveErr := resolveSpaceURI(installRoot, pw.Target)
	if resolveErr != nil {
		return skip(fmt.Sprintf("target resolution failed: %v", resolveErr))
	}

	absFile := filepath.Join(spaceDir, relPath)

	// Refuse to overwrite.
	if _, statErr := os.Stat(absFile); statErr == nil {
		return skip("target file already exists; create_file refuses to overwrite")
	}

	// Create parent directories.
	if mkdirErr := os.MkdirAll(filepath.Dir(absFile), 0o755); mkdirErr != nil {
		return nil, nil, "", fmt.Errorf("graft: create directories for %s: %w", absFile, mkdirErr)
	}

	// Write the file.
	if writeErr := os.WriteFile(absFile, []byte(body), 0o644); writeErr != nil {
		return nil, nil, "", fmt.Errorf("graft: write new file %s: %w", absFile, writeErr)
	}

	// Verify it parses cleanly.
	if _, parseErr := mdpp.Parse([]byte(body)); parseErr != nil {
		_ = os.Remove(absFile)
		return skip(fmt.Sprintf("new file does not parse: %v", parseErr))
	}

	// Build the canonical URI for this new file.
	// Strip .md extension from relPath if present for the URI.
	uriRelPath := strings.TrimSuffix(relPath, ".md")
	fileCanonicalURI := fmt.Sprintf("hypha://%s/%s/%s", authority, name, uriRelPath)

	aw := &AppliedWrite{
		Kind:       pw.Kind,
		TargetURI:  fileCanonicalURI,
		TargetFile: absFile,
		InsertedAt: mdpp.Range{StartByte: 0, EndByte: len(body)},
	}
	return aw, nil, fileCanonicalURI, nil
}

// applyReplaceBlock handles the replace_block write kind.
// Target: hypha://<...>/<file>#<anchor>  identifying a heading whose section
// content (heading line preserved, body replaced) will be spliced with new body.
func applyReplaceBlock(
	pw types.ProposedWrite,
	installRoot string,
	rollback map[string][]byte,
) (*AppliedWrite, *SkippedWrite, string, error) {
	skip := func(reason string) (*AppliedWrite, *SkippedWrite, string, error) {
		return nil, &SkippedWrite{Kind: pw.Kind, TargetURI: pw.Target, Reason: reason}, "", nil
	}

	body, _ := pw.Payload["body"].(string)
	if body == "" {
		return skip("replace_block payload missing 'body'")
	}
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}

	targetFile, anchorSlug, canonicalURI, err := resolveTarget(installRoot, pw.Target)
	if err != nil {
		return skip(fmt.Sprintf("target resolution failed: %v", err))
	}
	if anchorSlug == "" {
		return skip("replace_block requires an anchor in the target URI")
	}
	if _, statErr := os.Stat(targetFile); os.IsNotExist(statErr) {
		return skip("target file not found")
	}

	origBytes, readErr := os.ReadFile(targetFile)
	if readErr != nil {
		return skip(fmt.Sprintf("read target file: %v", readErr))
	}
	if _, saved := rollback[targetFile]; !saved {
		rollback[targetFile] = origBytes
	}

	// Parse and find the target heading.
	doc, parseErr := mdpp.Parse(origBytes)
	if parseErr != nil {
		return skip(fmt.Sprintf("parse target file: %v", parseErr))
	}
	headings := doc.AST().Find(mdpp.NodeHeading)
	if len(headings) == 0 {
		return skip("no headings found in target file")
	}

	targetIdx := -1
	for i, h := range headings {
		if slugify(strings.TrimSpace(h.Text())) == anchorSlug {
			targetIdx = i
			break
		}
	}
	if targetIdx < 0 {
		return skip(fmt.Sprintf("anchor %q not found in target file", anchorSlug))
	}

	targetHeading := headings[targetIdx]
	targetLevel := targetHeading.Level()

	// Section start = start of the heading node.
	sectionStart := targetHeading.Range.StartByte
	// Section end = start of the next heading at same or higher level, or EOF.
	sectionEnd := len(origBytes)
	for i := targetIdx + 1; i < len(headings); i++ {
		if headings[i].Level() <= targetLevel {
			sectionEnd = headings[i].Range.StartByte
			break
		}
	}

	// Extract the heading line verbatim (preserve level and title).
	headingLine := extractHeadingLine(origBytes, targetHeading.Range.StartByte)

	// Build replacement: headingLine + body
	replacement := headingLine + body

	// Splice: original[:sectionStart] + replacement + original[sectionEnd:]
	newBytes := make([]byte, 0, len(origBytes)-( sectionEnd-sectionStart)+len(replacement))
	newBytes = append(newBytes, origBytes[:sectionStart]...)
	newBytes = append(newBytes, []byte(replacement)...)
	newBytes = append(newBytes, origBytes[sectionEnd:]...)

	// Verify.
	if _, reParseErr := mdpp.Parse(newBytes); reParseErr != nil {
		_ = os.WriteFile(targetFile, rollback[targetFile], 0o644)
		delete(rollback, targetFile)
		return skip(fmt.Sprintf("post-edit re-parse failed: %v; rolled back", reParseErr))
	}

	if writeErr := os.WriteFile(targetFile, newBytes, 0o644); writeErr != nil {
		return nil, nil, "", fmt.Errorf("graft: write canonical file %s: %w", targetFile, writeErr)
	}

	aw := &AppliedWrite{
		Kind:       pw.Kind,
		TargetURI:  pw.Target,
		TargetFile: targetFile,
		InsertedAt: mdpp.Range{StartByte: sectionStart, EndByte: sectionStart + len(replacement)},
	}
	return aw, nil, canonicalURI, nil
}

// applyAddTag handles the add_tag write kind.
// Target: hypha://<...>/<file>  (no anchor).
// payload["tag"] is the string tag to add to the frontmatter tags: list.
func applyAddTag(
	pw types.ProposedWrite,
	installRoot string,
	rollback map[string][]byte,
) (*AppliedWrite, *SkippedWrite, string, error) {
	skip := func(reason string) (*AppliedWrite, *SkippedWrite, string, error) {
		return nil, &SkippedWrite{Kind: pw.Kind, TargetURI: pw.Target, Reason: reason}, "", nil
	}

	tag, _ := pw.Payload["tag"].(string)
	if tag == "" {
		return skip("add_tag payload missing 'tag'")
	}

	targetFile, _, canonicalURI, err := resolveTarget(installRoot, pw.Target)
	if err != nil {
		return skip(fmt.Sprintf("target resolution failed: %v", err))
	}
	if _, statErr := os.Stat(targetFile); os.IsNotExist(statErr) {
		return skip("target file not found")
	}

	origBytes, readErr := os.ReadFile(targetFile)
	if readErr != nil {
		return skip(fmt.Sprintf("read target file: %v", readErr))
	}

	// Locate the frontmatter byte range.
	doc, parseErr := mdpp.Parse(origBytes)
	if parseErr != nil {
		return skip(fmt.Sprintf("parse target file: %v", parseErr))
	}

	fmStart, fmEnd := 0, 0
	for _, child := range doc.AST().Children {
		if child.Type == mdpp.NodeFrontmatter {
			fmStart = child.Range.StartByte
			fmEnd = child.Range.EndByte
			break
		}
	}
	if fmEnd == 0 {
		return skip("no frontmatter in target file")
	}

	// Check if tag already exists.
	fm := doc.Frontmatter()
	if existingTags, ok := fm["tags"]; ok && existingTags != nil {
		if tagInList(existingTags, tag) {
			return skip("tag already present")
		}
	}

	// Save rollback AFTER confirming we'll modify.
	if _, saved := rollback[targetFile]; !saved {
		rollback[targetFile] = origBytes
	}

	fmSlice := origBytes[fmStart:fmEnd]
	newFMSlice, editErr := addTagToFrontmatter(fmSlice, tag)
	if editErr != nil {
		return skip(fmt.Sprintf("edit frontmatter: %v", editErr))
	}

	newBytes := make([]byte, 0, len(origBytes)+len(newFMSlice)-len(fmSlice))
	newBytes = append(newBytes, origBytes[:fmStart]...)
	newBytes = append(newBytes, newFMSlice...)
	newBytes = append(newBytes, origBytes[fmEnd:]...)

	// Verify tag was actually added.
	verDoc, verErr := mdpp.Parse(newBytes)
	if verErr != nil {
		_ = os.WriteFile(targetFile, rollback[targetFile], 0o644)
		delete(rollback, targetFile)
		return skip(fmt.Sprintf("post-edit re-parse failed: %v; rolled back", verErr))
	}
	if !tagInList(verDoc.Frontmatter()["tags"], tag) {
		_ = os.WriteFile(targetFile, rollback[targetFile], 0o644)
		delete(rollback, targetFile)
		return skip("post-edit verification: tag not found in parsed frontmatter; rolled back")
	}

	if writeErr := os.WriteFile(targetFile, newBytes, 0o644); writeErr != nil {
		return nil, nil, "", fmt.Errorf("graft: write canonical file %s: %w", targetFile, writeErr)
	}

	aw := &AppliedWrite{
		Kind:       pw.Kind,
		TargetURI:  pw.Target,
		TargetFile: targetFile,
		InsertedAt: mdpp.Range{StartByte: fmStart, EndByte: fmStart + len(newFMSlice)},
	}
	return aw, nil, canonicalURI, nil
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

// resolveSpaceURI parses a space-level URI (hypha://<authority>/<name>) and
// returns the absolute space directory, authority, and name.
func resolveSpaceURI(installRoot, spaceURI string) (spaceDir, authority, name string, err error) {
	if !strings.HasPrefix(spaceURI, "hypha://") {
		return "", "", "", fmt.Errorf("space URI must start with hypha://, got %q", spaceURI)
	}
	rest := strings.TrimPrefix(spaceURI, "hypha://")
	// Strip any anchor.
	if idx := strings.LastIndex(rest, "#"); idx >= 0 {
		rest = rest[:idx]
	}
	// Strip trailing slash.
	rest = strings.TrimRight(rest, "/")
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 {
		return "", "", "", fmt.Errorf("space URI must have authority/name, got %q", spaceURI)
	}
	authority = parts[0]
	name = parts[1]
	dirName := fmt.Sprintf("%s-%s", authority, name)
	spaceDir = filepath.Join(installRoot, "spaces", dirName)
	return spaceDir, authority, name, nil
}

// extractHeadingLine returns the raw source line for the heading at startByte.
// It reads from startByte to the next newline (inclusive) in src.
func extractHeadingLine(src []byte, startByte int) string {
	end := startByte
	for end < len(src) && src[end] != '\n' {
		end++
	}
	if end < len(src) {
		end++ // include the newline
	}
	return string(src[startByte:end])
}

// tagInList reports whether tag exists in raw frontmatter tags value.
// The value may be []any (list of strings) or a single string.
func tagInList(raw any, tag string) bool {
	switch v := raw.(type) {
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s == tag {
				return true
			}
		}
	case []string:
		for _, s := range v {
			if s == tag {
				return true
			}
		}
	case string:
		return v == tag
	}
	return false
}

// addTagToFrontmatter edits the raw frontmatter bytes (the full `---\n...\n---`
// block) to add tag to the tags: list. It handles three cases:
//
//  1. Inline flow: tags: [a, b]         → tags: [a, b, newtag]
//  2. Block list: tags:\n  - a\n  - b\n → ... + "  - newtag\n"
//  3. Absent: inserts tags: [newtag] before the closing ---
func addTagToFrontmatter(fmSlice []byte, tag string) ([]byte, error) {
	s := string(fmSlice)

	// Case 1: inline flow style — tags: [a, b] on a single line.
	if tagsFlowRe.MatchString(s) {
		result := tagsFlowRe.ReplaceAllStringFunc(s, func(match string) string {
			sub := tagsFlowRe.FindStringSubmatch(match)
			// sub[1] = "tags: [", sub[2] = "a, b", sub[3] = "]..."
			existing := strings.TrimSpace(sub[2])
			if existing == "" {
				return sub[1] + tag + sub[3]
			}
			return sub[1] + existing + ", " + tag + sub[3]
		})
		return []byte(result), nil
	}

	// Case 2: block list style — tags: on its own line followed by "  - item" lines.
	// Find "tags:" line (alone on its line).
	if tagsKeyRe.MatchString(s) {
		// Find the position of "tags:\n" and append a new "  - tag\n" item after
		// all existing "  - " items in this block.
		loc := tagsKeyRe.FindStringIndex(s)
		if loc != nil {
			insertPos := loc[1] // just after "tags:\n"
			// Scan forward past all "  - ..." lines.
			remaining := s[insertPos:]
			consumed := 0
			for {
				// Lines that belong to the block-list value: start with whitespace + "- "
				nlIdx := strings.IndexByte(remaining[consumed:], '\n')
				if nlIdx < 0 {
					break
				}
				line := remaining[consumed : consumed+nlIdx]
				trimmed := strings.TrimLeft(line, " \t")
				if strings.HasPrefix(trimmed, "- ") {
					consumed += nlIdx + 1
				} else {
					break
				}
			}
			// Insert "  - tag\n" at position insertPos+consumed.
			absInsert := insertPos + consumed
			result := s[:absInsert] + "  - " + tag + "\n" + s[absInsert:]
			return []byte(result), nil
		}
	}

	// Case 3: no tags: field — insert before closing "---".
	// Find the closing "---" of the frontmatter.
	// The fmSlice is "---\n...\n---\n"; find the last "---".
	closingRe := regexp.MustCompile(`(?m)^---\s*$`)
	locs := closingRe.FindAllStringIndex(s, -1)
	if len(locs) < 2 {
		// Malformed frontmatter; insert at end of slice just before final newline.
		newLine := "tags: [" + tag + "]\n"
		return []byte(s + newLine), nil
	}
	// Insert before the closing ---
	closingStart := locs[len(locs)-1][0]
	result := s[:closingStart] + "tags: [" + tag + "]\n" + s[closingStart:]
	return []byte(result), nil
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
