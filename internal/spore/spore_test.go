package spore_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/hyphae/internal/identity"
	"m31labs.dev/hyphae/internal/spore"
	"m31labs.dev/hyphae/internal/types"
)

// validSporeDoc is a minimal valid spore document modelled on the example in
// /home/draco/.hyphae/spaces/m31labs-hyphae/concepts/spore.md.
var validSporeDoc = []byte(`---
mdpp: "0.1"
id: spore.2026-05-25.cloud-agent.7f3a
type: spore
space: hypha://m31labs/research
status: unreviewed
created: 2026-05-25T18:42:00Z

agent:
  id: agent://cloud/openai/session-7f3a
  kind: ephemeral
  model: gpt-5.5-pro
  run_id: run_7f3a
  task_id: task_hyphae_federation_spec

confidence: medium

source_refs:
  - hypha://m31labs/research/concept.hyphae#federation
  - hypha://m31labs/research/spec.hyphae.v0.1#agent-tools

proposed_edges:
  - kind: supports
    src: spore.2026-05-25.cloud-agent.7f3a
    dst: hypha://m31labs/research/concept.agent-report-back
    confidence: 0.82

proposed_writes:
  - kind: append_section
    target: hypha://m31labs/research/concept.hyphae
    heading: "Ephemeral agent report-back"
---

# Cloud Agent Report: Hyphae report-back protocol

## Summary

Ephemeral cloud agents should be able to submit small, source-grounded
knowledge deltas without cloning the full vault or receiving broad write access.

## Findings

- Agent contributions should land in an inbox as unreviewed spores.
- Trusted agents may auto-graft low-risk reports.
`)

// TestHappyPath verifies that a well-formed spore document parses without
// errors, writes to a temp directory, and produces a well-formed receipt.
func TestHappyPath(t *testing.T) {
	s, errs := spore.Parse(validSporeDoc)
	if len(errs) != 0 {
		t.Fatalf("Parse returned unexpected errors: %v", errs)
	}

	// Check required fields are populated.
	if s.ID != "spore.2026-05-25.cloud-agent.7f3a" {
		t.Errorf("ID = %q, want \"spore.2026-05-25.cloud-agent.7f3a\"", s.ID)
	}
	if s.SpaceID != "hypha://m31labs/research" {
		t.Errorf("SpaceID = %q, want \"hypha://m31labs/research\"", s.SpaceID)
	}
	if s.Status != "unreviewed" {
		t.Errorf("Status = %q, want \"unreviewed\"", s.Status)
	}
	if s.AgentID != "agent://cloud/openai/session-7f3a" {
		t.Errorf("AgentID = %q", s.AgentID)
	}
	if s.AgentKind != "ephemeral" {
		t.Errorf("AgentKind = %q, want \"ephemeral\"", s.AgentKind)
	}
	if s.Confidence != "medium" {
		t.Errorf("Confidence = %q, want \"medium\"", s.Confidence)
	}
	if len(s.SourceRefs) != 2 {
		t.Errorf("SourceRefs len = %d, want 2", len(s.SourceRefs))
	}
	if len(s.ProposedEdges) != 1 {
		t.Errorf("ProposedEdges len = %d, want 1", len(s.ProposedEdges))
	}
	if s.ProposedEdges[0].Kind != types.EdgeKind("supports") {
		t.Errorf("ProposedEdges[0].Kind = %q, want \"supports\"", s.ProposedEdges[0].Kind)
	}
	if s.ProposedEdges[0].Confidence != 0.82 {
		t.Errorf("ProposedEdges[0].Confidence = %v, want 0.82", s.ProposedEdges[0].Confidence)
	}
	if len(s.ProposedWrites) != 1 {
		t.Errorf("ProposedWrites len = %d, want 1", len(s.ProposedWrites))
	}
	if s.ProposedWrites[0].Kind != "append_section" {
		t.Errorf("ProposedWrites[0].Kind = %q, want \"append_section\"", s.ProposedWrites[0].Kind)
	}
	if s.Body == "" {
		t.Error("Body is empty, expected parsed document body")
	}
	if s.TokenCount <= 0 {
		t.Errorf("TokenCount = %d, want > 0", s.TokenCount)
	}

	// Submit to a temp space root.
	spaceRoot := t.TempDir()
	filePath, receipt, err := spore.Submit(s, spaceRoot)
	if err != nil {
		t.Fatalf("Submit returned error: %v", err)
	}

	// Verify file was created.
	if _, statErr := os.Stat(filePath); os.IsNotExist(statErr) {
		t.Fatalf("expected file to exist at %s", filePath)
	}

	// Verify file is inside inbox/agents/.
	inboxDir := filepath.Join(spaceRoot, "inbox", "agents")
	if !strings.HasPrefix(filePath, inboxDir) {
		t.Errorf("filePath %q is not inside inbox/agents/", filePath)
	}

	// Verify filename convention: 2026-05-25-<slug>.md
	base := filepath.Base(filePath)
	if !strings.HasPrefix(base, "2026-05-25-") {
		t.Errorf("filename %q does not start with date prefix \"2026-05-25-\"", base)
	}
	if !strings.HasSuffix(base, ".md") {
		t.Errorf("filename %q does not end with .md", base)
	}

	// Verify receipt fields.
	if receipt.ID == "" {
		t.Error("receipt.ID is empty")
	}
	if !strings.HasPrefix(receipt.ID, "hypha-receipt:") {
		t.Errorf("receipt.ID = %q, want prefix \"hypha-receipt:\"", receipt.ID)
	}
	if receipt.SpaceID != s.SpaceID {
		t.Errorf("receipt.SpaceID = %q, want %q", receipt.SpaceID, s.SpaceID)
	}
	if receipt.SubjectID != s.AgentID {
		t.Errorf("receipt.SubjectID = %q, want %q", receipt.SubjectID, s.AgentID)
	}
	if receipt.SubjectKind != "agent" {
		t.Errorf("receipt.SubjectKind = %q, want \"agent\"", receipt.SubjectKind)
	}
	if receipt.Action != "spore:create" {
		t.Errorf("receipt.Action = %q, want \"spore:create\"", receipt.Action)
	}
	if receipt.Status != "accepted_for_review" {
		t.Errorf("receipt.Status = %q, want \"accepted_for_review\"", receipt.Status)
	}
	if receipt.ContentHash == "" {
		t.Error("receipt.ContentHash is empty")
	}
	if receipt.CreatedAt.IsZero() {
		t.Error("receipt.CreatedAt is zero")
	}
	if receipt.NextState != "review" {
		t.Errorf("receipt.NextState = %q, want \"review\"", receipt.NextState)
	}
	if len(receipt.PermissionsUsed) != 1 || receipt.PermissionsUsed[0] != "spore:create" {
		t.Errorf("receipt.PermissionsUsed = %v, want [\"spore:create\"]", receipt.PermissionsUsed)
	}
}

// TestDuplicateSubmit verifies that submitting the same spore twice returns an
// error and does not overwrite the existing file.
func TestDuplicateSubmit(t *testing.T) {
	s, errs := spore.Parse(validSporeDoc)
	if len(errs) != 0 {
		t.Fatalf("Parse returned unexpected errors: %v", errs)
	}

	spaceRoot := t.TempDir()
	_, _, err := spore.Submit(s, spaceRoot)
	if err != nil {
		t.Fatalf("first Submit returned error: %v", err)
	}
	_, _, err = spore.Submit(s, spaceRoot)
	if err == nil {
		t.Fatal("second Submit should have returned an error for duplicate spore")
	}
	if !errors.Is(err, spore.ErrDuplicate) {
		t.Errorf("expected ErrDuplicate, got: %v", err)
	}
}

// TestMissingAgentID verifies that Parse returns a ValidationError for agent.id
// when the agent block is present but the id field is missing.
func TestMissingAgentID(t *testing.T) {
	doc := []byte(`---
mdpp: "0.1"
id: spore.2026-05-25.test-agent.ab12
type: spore
space: hypha://m31labs/research
status: unreviewed
created: 2026-05-25T10:00:00Z

agent:
  kind: ephemeral

confidence: medium

source_refs:
  - hypha://m31labs/research/concept.hyphae
---

# Body text here.
`)

	_, errs := spore.Parse(doc)
	if len(errs) == 0 {
		t.Fatal("expected validation errors, got none")
	}

	found := false
	for _, e := range errs {
		if e.Field == "agent.id" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected ValidationError for field \"agent.id\", got: %v", errs)
	}
}

// TestMissingRequiredFields verifies that each missing required field generates
// a separate ValidationError.
func TestMissingRequiredFields(t *testing.T) {
	// Document with no frontmatter fields at all except type.
	doc := []byte(`---
type: spore
---

# Minimal body.
`)

	_, errs := spore.Parse(doc)
	if len(errs) == 0 {
		t.Fatal("expected validation errors, got none")
	}

	requiredFields := []string{"id", "space", "status", "created", "confidence", "source_refs"}
	errMap := make(map[string]bool)
	for _, e := range errs {
		errMap[e.Field] = true
	}
	for _, field := range requiredFields {
		if !errMap[field] {
			t.Errorf("expected ValidationError for field %q, not found in: %v", field, errs)
		}
	}
}

// TestInvalidEnumValues verifies that bad enum values (status, confidence,
// agent.kind) each produce a ValidationError.
func TestInvalidEnumValues(t *testing.T) {
	doc := []byte(`---
id: spore.2026-05-25.test.enum
type: spore
space: hypha://m31labs/research
status: invalid-status
created: 2026-05-25T10:00:00Z

agent:
  id: agent://cloud/test
  kind: unknown-kind

confidence: extreme

source_refs:
  - hypha://m31labs/research/concept.hyphae
---

# Body.
`)

	_, errs := spore.Parse(doc)
	errMap := make(map[string]bool)
	for _, e := range errs {
		errMap[e.Field] = true
	}

	for _, field := range []string{"status", "agent.kind", "confidence"} {
		if !errMap[field] {
			t.Errorf("expected ValidationError for %q with invalid enum value; errors: %v", field, errs)
		}
	}
}

// TestTokenCapEnforced verifies that Submit refuses a spore whose body exceeds
// the 5000-token hard cap.
func TestTokenCapEnforced(t *testing.T) {
	// Build a spore with a ~5001 token body (len > 20000 bytes).
	bigBody := strings.Repeat("a", 20004)

	s := types.Spore{
		ID:          "spore.2026-05-25.test.toobig",
		SpaceID:     "hypha://m31labs/research",
		Status:      "unreviewed",
		AgentID:     "agent://cloud/test",
		AgentKind:   "ephemeral",
		Confidence:  "medium",
		SourceRefs:  []string{"hypha://m31labs/research/concept.hyphae"},
		Body:        bigBody,
		TokenCount:  len(bigBody) / 4, // 5001
		SubmittedAt: time.Now().UTC(),
	}

	spaceRoot := t.TempDir()
	_, _, err := spore.Submit(s, spaceRoot)
	if err == nil {
		t.Fatal("expected error for body exceeding token cap, got nil")
	}
	if !strings.Contains(err.Error(), "hard cap") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestIDNotSporePrefixed verifies that Parse returns a ValidationError when
// the id does not start with "spore.".
func TestIDNotSporePrefixed(t *testing.T) {
	doc := []byte(`---
id: concept.not-a-spore
type: spore
space: hypha://m31labs/research
status: unreviewed
created: 2026-05-25T10:00:00Z

agent:
  id: agent://cloud/test
  kind: ephemeral

confidence: low

source_refs:
  - hypha://m31labs/research/concept.hyphae
---

# Body.
`)

	_, errs := spore.Parse(doc)
	found := false
	for _, e := range errs {
		if e.Field == "id" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected ValidationError for field \"id\", got: %v", errs)
	}
}

// TestInvalidAgentURIPrefix verifies that an agent.id without a valid URI
// prefix produces a ValidationError.
func TestInvalidAgentURIPrefix(t *testing.T) {
	doc := []byte(`---
id: spore.2026-05-25.test.uribad
type: spore
space: hypha://m31labs/research
status: unreviewed
created: 2026-05-25T10:00:00Z

agent:
  id: http://not-valid
  kind: ephemeral

confidence: low

source_refs:
  - hypha://m31labs/research/concept.hyphae
---

# Body.
`)

	_, errs := spore.Parse(doc)
	found := false
	for _, e := range errs {
		if e.Field == "agent.id" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected ValidationError for field \"agent.id\", got: %v", errs)
	}
}

// TestNoFrontmatter verifies that a document without frontmatter returns a
// ValidationError.
func TestNoFrontmatter(t *testing.T) {
	doc := []byte("# Just a heading\n\nNo frontmatter here.\n")
	_, errs := spore.Parse(doc)
	if len(errs) == 0 {
		t.Fatal("expected validation errors for document without frontmatter")
	}
}

// TestFilenameSlug verifies that the filename slug strips the spore date prefix
// and replaces non-alphanumeric characters with dashes.
func TestFilenameSlug(t *testing.T) {
	s, errs := spore.Parse(validSporeDoc)
	if len(errs) != 0 {
		t.Fatalf("Parse returned unexpected errors: %v", errs)
	}

	spaceRoot := t.TempDir()
	filePath, _, err := spore.Submit(s, spaceRoot)
	if err != nil {
		t.Fatalf("Submit returned error: %v", err)
	}

	base := filepath.Base(filePath)
	// For id "spore.2026-05-25.cloud-agent.7f3a", after stripping
	// "spore.2026-05-25." we get "cloud-agent.7f3a".
	// Non-alphanumeric (the dot) → dash: "cloud-agent-7f3a".
	// Filename: "2026-05-25-cloud-agent-7f3a.md"
	expected := "2026-05-25-cloud-agent-7f3a.md"
	if base != expected {
		t.Errorf("filename = %q, want %q", base, expected)
	}
}

// ─── Signing tests ────────────────────────────────────────────────────────────

// makeTestIdentity generates a fresh identity for use in signing tests.
func makeTestIdentity(t *testing.T) (identity.Identity, identity.PrivateKey) {
	t.Helper()
	id, priv, err := identity.Generate("m31labs", "testbot", "hypha://m31labs/research")
	if err != nil {
		t.Fatalf("identity.Generate: %v", err)
	}
	return id, priv
}

// resolverFor returns an IdentityResolver that recognises exactly one identity.
func resolverFor(id identity.Identity) spore.IdentityResolver {
	return func(uri string) (identity.Identity, error) {
		if uri == id.ID {
			return id, nil
		}
		return identity.Identity{}, fmt.Errorf("unknown identity %q", uri)
	}
}

// TestSignVerifyRoundtrip verifies that Sign produces a document that Verify
// accepts, and that the output contains a `signature:` block.
func TestSignVerifyRoundtrip(t *testing.T) {
	id, priv := makeTestIdentity(t)
	resolver := resolverFor(id)

	signed, err := spore.Sign(validSporeDoc, priv, id.ID)
	if err != nil {
		t.Fatalf("Sign returned error: %v", err)
	}

	if !strings.Contains(string(signed), "signature:") {
		t.Error("signed output does not contain a `signature:` block")
	}

	if err := spore.Verify(signed, resolver); err != nil {
		t.Fatalf("Verify returned error: %v", err)
	}
}

// TestVerifyUnsigned verifies that Verify returns ErrUnsigned for a spore
// without a signature block.
func TestVerifyUnsigned(t *testing.T) {
	resolver := func(uri string) (identity.Identity, error) {
		return identity.Identity{}, nil
	}

	err := spore.Verify(validSporeDoc, resolver)
	if !errors.Is(err, spore.ErrUnsigned) {
		t.Fatalf("expected ErrUnsigned, got: %v", err)
	}
}

// TestTamperedBody verifies that Verify detects a body modification after
// signing.
func TestTamperedBody(t *testing.T) {
	id, priv := makeTestIdentity(t)
	resolver := resolverFor(id)

	signed, err := spore.Sign(validSporeDoc, priv, id.ID)
	if err != nil {
		t.Fatalf("Sign returned error: %v", err)
	}

	// Append a line to the body to tamper with the content.
	tampered := append(signed, []byte("\ntampered\n")...)

	err = spore.Verify(tampered, resolver)
	if err == nil {
		t.Fatal("Verify should have returned an error for tampered body, got nil")
	}
}

// TestWorkLogAppendPreservesSignature proves that the `trace done --link-spore`
// work-log append — a "## Work log (trace.…)" section added to the body AFTER
// signing — does not invalidate the signature. The signed region is the
// authored content, which ends where the tool-appended work log begins.
func TestWorkLogAppendPreservesSignature(t *testing.T) {
	id, priv := makeTestIdentity(t)
	resolver := resolverFor(id)

	signed, err := spore.Sign(validSporeDoc, priv, id.ID)
	if err != nil {
		t.Fatalf("Sign returned error: %v", err)
	}

	// Exactly the shape internal/trace.appendWorkLogToSpore writes.
	workLog := "\n## Work log (trace.2026-05-28.testbot.abcd)\n\n" +
		"_Compacted from trace `trace.2026-05-28.testbot.abcd` (succeeded, ticks=1, " +
		"started=2026-05-28T18:00:00Z, last_tick=2026-05-28T18:05:00Z)._\n\n" +
		"- 2026-05-28T18:01:00Z  did the thing\n"
	withLog := append(append([]byte{}, signed...), []byte(workLog)...)

	if err := spore.Verify(withLog, resolver); err != nil {
		t.Fatalf("Verify should pass after work-log append, got: %v", err)
	}
}

// TestUnknownSigner verifies that Verify returns an error when the resolver
// does not recognise the signing key URI.
func TestUnknownSigner(t *testing.T) {
	id, priv := makeTestIdentity(t)

	signed, err := spore.Sign(validSporeDoc, priv, id.ID)
	if err != nil {
		t.Fatalf("Sign returned error: %v", err)
	}

	// Resolver that never resolves anything.
	unknownResolver := func(uri string) (identity.Identity, error) {
		return identity.Identity{}, fmt.Errorf("no such identity")
	}

	err = spore.Verify(signed, unknownResolver)
	if err == nil {
		t.Fatal("Verify should have returned an error for unknown signer, got nil")
	}
	if !strings.Contains(err.Error(), "unknown signer") {
		t.Errorf("expected 'unknown signer' in error, got: %v", err)
	}
}

// TestProposedWriteRejectsPayloadWrapper guards against the dogfood failure
// mode where a caller nests write-kind-specific fields under a `payload:` key
// (e.g. for create_file). Submit silently accepted this; graft then skipped
// at apply time with "create_file payload missing 'path'". Surface at submit.
func TestProposedWriteRejectsPayloadWrapper(t *testing.T) {
	doc := []byte(`---
mdpp: "0.1"
id: spore.2026-05-25.test-agent.pw01
type: spore
space: hypha://m31labs/hyphae
status: unreviewed
created: 2026-05-25T10:00:00Z

agent:
  id: agent://test/runner
  kind: ephemeral

confidence: medium

source_refs:
  - hypha://m31labs/hyphae/concepts/spore

proposed_writes:
  - kind: create_file
    target: hypha://m31labs/hyphae
    payload:
      path: skills/new-skill.md
      body: "---\nid: skill.new\n---\n# Body\n"
---

# Body
`)
	_, errs := spore.Parse(doc)
	if len(errs) == 0 {
		t.Fatal("expected validation error for proposed_writes[0].payload wrapper, got none")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Field, "proposed_writes[0]") && strings.Contains(e.Message, "payload") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error mentioning payload wrapper on proposed_writes[0], got: %v", errs)
	}
}

// TestProposedWriteCreateFileRequiresPathAndBody verifies that submit refuses
// a create_file write missing 'path' or 'body' instead of deferring the
// error to graft.
func TestProposedWriteCreateFileRequiresPathAndBody(t *testing.T) {
	cases := []struct {
		name      string
		writeYAML string
		want      string
	}{
		{
			name: "missing-path",
			writeYAML: `proposed_writes:
  - kind: create_file
    target: hypha://m31labs/hyphae
    body: "x"`,
			want: "path",
		},
		{
			name: "missing-body",
			writeYAML: `proposed_writes:
  - kind: create_file
    target: hypha://m31labs/hyphae
    path: skills/x.md`,
			want: "body",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := []byte(fmt.Sprintf(`---
mdpp: "0.1"
id: spore.2026-05-25.test-agent.cf01
type: spore
space: hypha://m31labs/hyphae
status: unreviewed
created: 2026-05-25T10:00:00Z

agent:
  id: agent://test/runner
  kind: ephemeral

confidence: medium

source_refs:
  - hypha://m31labs/hyphae/concepts/spore

%s
---

# Body
`, tc.writeYAML))
			_, errs := spore.Parse(doc)
			if len(errs) == 0 {
				t.Fatalf("expected validation error for missing %s, got none", tc.want)
			}
			found := false
			for _, e := range errs {
				if strings.Contains(e.Field, "proposed_writes[0]") &&
					(strings.Contains(e.Field, tc.want) || strings.Contains(e.Message, tc.want)) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected error mentioning %q on proposed_writes[0], got: %v", tc.want, errs)
			}
		})
	}
}

// TestProposedEdgeRejectsSrcMismatch guards against the dogfood failure mode
// where proposed_edges[i].src does not equal the spore's own id. Graft would
// persist dangling edges keyed off the YAML's stale src. Surface at submit.
func TestProposedEdgeRejectsSrcMismatch(t *testing.T) {
	doc := []byte(`---
mdpp: "0.1"
id: spore.2026-05-25.test-agent.em01
type: spore
space: hypha://m31labs/hyphae
status: unreviewed
created: 2026-05-25T10:00:00Z

agent:
  id: agent://test/runner
  kind: ephemeral

confidence: medium

source_refs:
  - hypha://m31labs/hyphae/concepts/spore

proposed_edges:
  - kind: cites
    src: spore.2026-05-25.test-agent.em01-stale
    dst: concept.spore
    confidence: 1.0
---

# Body
`)
	_, errs := spore.Parse(doc)
	if len(errs) == 0 {
		t.Fatal("expected validation error for proposed_edges[0].src mismatch, got none")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Field, "proposed_edges[0].src") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error on proposed_edges[0].src, got: %v", errs)
	}
}

// TestProposedEdgeAcceptsMatchingSrc is the positive case for the prior test:
// when src == spore.id, validation must pass.
func TestProposedEdgeAcceptsMatchingSrc(t *testing.T) {
	doc := []byte(`---
mdpp: "0.1"
id: spore.2026-05-25.test-agent.em02
type: spore
space: hypha://m31labs/hyphae
status: unreviewed
created: 2026-05-25T10:00:00Z

agent:
  id: agent://test/runner
  kind: ephemeral

confidence: medium

source_refs:
  - hypha://m31labs/hyphae/concepts/spore

proposed_edges:
  - kind: cites
    src: spore.2026-05-25.test-agent.em02
    dst: concept.spore
    confidence: 1.0
---

# Body
`)
	_, errs := spore.Parse(doc)
	for _, e := range errs {
		if strings.Contains(e.Field, "proposed_edges") {
			t.Errorf("unexpected validation error on proposed_edges: %v", e)
		}
	}
}
