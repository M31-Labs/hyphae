// Package types defines the Hyphae object model.
//
// Mirrors the SQLite schema at ~/.hyphae/spaces/m31labs-hyphae/protocols/schema.sql
// and the concept docs under .../concepts/. Every Object lives in a Space and
// is parsed out of an mdpp file's frontmatter + body.
package types

import "time"

// ObjectType is the typed kind of a Hyphae artifact.
type ObjectType string

const (
	TypeSpace       ObjectType = "space"
	TypeConcept     ObjectType = "concept"
	TypeDecision    ObjectType = "decision"
	TypeInitiative  ObjectType = "initiative"
	TypeLesson      ObjectType = "lesson"
	TypeSpec        ObjectType = "spec"
	TypePlan        ObjectType = "plan"
	TypeSpore       ObjectType = "spore"
	TypeSkill       ObjectType = "skill"
	TypeProtocol    ObjectType = "protocol"
	TypeIntegration ObjectType = "integration"
	TypeReadme      ObjectType = "readme"
	TypeIdentity    ObjectType = "identity"
	TypeTrace       ObjectType = "trace"
)

// TraceStatus values for the trace lifecycle (per decision 0009).
const (
	TraceStatusOpen       = "open"
	TraceStatusSucceeded  = "succeeded"
	TraceStatusFailed     = "failed"
	TraceStatusKilled     = "killed"
	TraceStatusSuperseded = "superseded"
)

// Trace is the in-flight, checkpoint-emitted record of how a piece of work
// happened. Spores say *what* was done; receipts say *it happened, signed*;
// traces say *how the work went*. See concept.trace + decision.0009.
type Trace struct {
	ID           string
	SpaceID      string
	AgentID      string
	AgentParent  string
	AgentSession string
	TaskRef      string
	Phase        string
	Status       string
	Started      time.Time
	LastTick     time.Time
	LinkedSpore  string
	Ticks        []Tick
	Body         string // free-form body (e.g. compacted work-log after `done`)
	FilePath     string
}

// Tick is one checkpoint within a Trace.
type Tick struct {
	At      time.Time
	Message string
}

// Object is the indexed form of a Hyphae artifact.
type Object struct {
	ID        string                 // frontmatter `id:`
	Type      ObjectType             // frontmatter `type:`
	SpaceID   string                 // owning space (e.g. "m31labs/hyphae")
	FilePath  string                 // absolute on-disk path
	Status    string                 // frontmatter `status:`
	Title     string                 // first H1 in body
	Summary   string                 // indexer-derived one-sentence summary
	Tags      []string               // frontmatter `tags:`
	Body      string                 // mdpp body text
	Frontmtr  map[string]any // raw parsed frontmatter
	UpdatedAt time.Time
}

// Anchor is a stable address for a block within an Object's file.
type Anchor struct {
	ID          string // "hypha://<space>/<file-path>#<heading-or-hash>"
	ObjectID    string
	HeadingPath string // "/Architecture/Federation"
	StartByte   int
	EndByte     int
	StartLine   int
	EndLine     int
	NodeKind    string // heading|paragraph|list|fence|...
}

// EdgeKind classifies a graph edge.
type EdgeKind string

const (
	EdgeRelated      EdgeKind = "related"
	EdgeSourceRef    EdgeKind = "source_ref"
	EdgeAppliesTo    EdgeKind = "applies_to"
	EdgeSupports     EdgeKind = "supports"
	EdgeDerivedFrom  EdgeKind = "derived_from"
	EdgeSupersedes   EdgeKind = "supersedes"
	EdgeSupersededBy EdgeKind = "superseded_by"
	EdgeBlocks       EdgeKind = "blocks"
	EdgeCites        EdgeKind = "cites"
	EdgeWikilink     EdgeKind = "wikilink" // [[name]]
	EdgeLinkRef      EdgeKind = "linkref"  // [text](hypha://...)
)

// Edge is one link in the graph.
type Edge struct {
	ID           string
	Kind         EdgeKind
	SrcID        string // Object.ID or Anchor.ID
	DstID        string
	Confidence   float64
	Derivation   string // "frontmatter" | "linkref" | "wikilink" | "graft" | "manual"
	AgentSource  string
	CreatedBy    string
	CreatedAt    time.Time
	MetadataJSON string
}

// Spore is a portable, source-grounded knowledge contribution.
type Spore struct {
	ID             string
	SpaceID        string
	FilePath       string
	Status         string // unreviewed | accepted | partial | rejected | duplicate | superseded | archived
	Trust          string // untrusted | subscribed | team | trusted
	AgentID        string
	AgentKind      string
	AgentModel     string
	TaskID         string
	RunID          string
	Confidence     string // low | medium | high
	SourceRefs     []string
	ProposedWrites []ProposedWrite
	ProposedEdges  []ProposedEdge
	Body           string
	ContentHash    string
	TokenCount     int
	ReceiptID      string
	SubmittedAt    time.Time
}

// ProposedWrite is a structural edit a spore proposes during graft.
type ProposedWrite struct {
	Kind    string // append_section | insert_after | replace_block | create_file | add_tag
	Target  string // hypha:// URI + anchor
	Payload map[string]any
	Status  string // pending | applied | rejected
}

// ProposedEdge is an edge a spore proposes during graft.
type ProposedEdge struct {
	SrcID      string
	DstID      string
	Kind       EdgeKind
	Confidence float64
	Status     string // pending | applied | rejected
}

// Capability is a scoped, short-lived permission token.
type Capability struct {
	ID            string
	Subject       string // identity:// or agent:// URI
	SpaceID       string
	Permissions   []string
	Limits        Limits
	IssuedBy      string
	IssuedAt      time.Time
	ExpiresAt     time.Time
	RevokedAt     *time.Time
}

// Limits are per-token caps enforced on every operation.
type Limits struct {
	MaxSpores         int    `json:"max_spores,omitempty"`
	MaxBytes          int    `json:"max_bytes,omitempty"`
	MaxRecallResults  int    `json:"max_recall_results,omitempty"`
	MaxResponseTokens int    `json:"max_response_tokens,omitempty"`
	MaxResponseShape  string `json:"max_response_shape,omitempty"`
	AllowedPaths      []string `json:"allowed_paths,omitempty"`
	DeniedPaths       []string `json:"denied_paths,omitempty"`
}

// Receipt is the durable audit record of any contribute or graft operation.
type Receipt struct {
	ID              string
	SpaceID         string
	SubjectID       string
	SubjectKind     string
	Action          string // "spore:create" | "graft" | "spore:reject" | "cap:issue" | ...
	Status          string
	ContentHash     string
	IdentityID      string
	CreatedAt       time.Time
	ExpiresAt       *time.Time
	PermissionsUsed []string
	NextState       string
	MetadataJSON    string
}

// ResponseShape governs the size and form of a recall/assess response.
type ResponseShape string

const (
	ShapeCountOnly      ResponseShape = "count_only"
	ShapeHeadline       ResponseShape = "headline"
	ShapeSummaryAnchors ResponseShape = "summary+anchors"
	ShapeCitedSpans     ResponseShape = "cited_spans"
	ShapeFullDocuments  ResponseShape = "full_documents"
)

// Budget is the per-call response budget.
type Budget struct {
	MaxResponseTokens int           `json:"max_response_tokens,omitempty"`
	Shape             ResponseShape `json:"shape,omitempty"`
}

// DefaultBudget is the v0.1 default: summary+anchors at ≤800 tokens.
func DefaultBudget() Budget {
	return Budget{MaxResponseTokens: 800, Shape: ShapeSummaryAnchors}
}
