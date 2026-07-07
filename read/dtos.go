// Package read exposes a read-only view of the Hyphae SQLite index for use by
// external modules such as m31labs.dev/overstory. Because this package lives
// inside the hyphae module it is allowed to import internal/ packages; no
// internal types leak through the public surface — only plain DTO structs.
package read

import "time"

// Space is a Hyphae space as stored in the index.
type Space struct {
	ID           string
	URI          string
	Authority    string
	Scope        string
	Visibility   string
	RootPath     string
	TrustDefault string
	CreatedAt    time.Time
	MetadataJSON string
}

// Object is an indexed Hyphae artifact.
type Object struct {
	ID           string
	Type         string
	SpaceID      string
	FileID       string
	Status       string
	Title        string
	TagsJSON     string
	Summary      string
	MetadataJSON string
	UpdatedAt    time.Time
}

// ObjectFilter narrows Objects queries. Zero value means no filter (all objects).
type ObjectFilter struct {
	SpaceID string // empty = all spaces
	Type    string // empty = all types
}

// Spore is a portable, source-grounded knowledge contribution row.
type Spore struct {
	ID              string
	SpaceID         string
	FileID          string
	Status          string
	Trust           string
	AgentIdentityID string
	TaskID          string
	RunID           string
	ContentHash     string
	TokenCount      int
	ReceiptID       string
	SubmittedAt     time.Time
	ReviewedAt      *time.Time
	ReviewedBy      string
	Decision        string
	DecisionReason  string
	MetadataJSON    string
}

// SporeFilter narrows Spores queries. Zero value means no filter.
type SporeFilter struct {
	SpaceID string // empty = all spaces
	Status  string // empty = all statuses
}

// Receipt is an audit record for a contribute or graft operation.
type Receipt struct {
	ID           string
	SpaceID      string
	SubjectID    string
	SubjectKind  string
	Action       string
	Status       string
	ContentHash  string
	IdentityID   string
	CreatedAt    time.Time
	ExpiresAt    *time.Time
	MetadataJSON string
}

// ReceiptFilter narrows Receipts queries. Zero value means no filter.
type ReceiptFilter struct {
	SpaceID   string    // empty = all spaces
	SubjectID string    // empty = all subjects
	Action    string    // empty = all actions
	Since     time.Time // zero = all time
}

// Edge is one link in the knowledge graph.
type Edge struct {
	ID           string
	Kind         string
	SrcID        string
	DstID        string
	Confidence   float64
	Derivation   string
	AgentSource  string
	CreatedBy    string
	CreatedAt    time.Time
	MetadataJSON string
}

// Trace is an active or recently-closed in-flight trace record.
// Traces are NOT in the SQLite index; they live under <space-root>/.trace/.
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
	FilePath     string
}

// Pulse is a time-windowed signal aggregation for a space.
type Pulse struct {
	Space      string
	Window     string
	WindowStart time.Time
	ComputedAt time.Time

	TopInitiatives []PulseInitiative
	HotZones       []PulseHotZone
	RecentPressure []PulsePressure
	EdgeKindDist   []PulseKindCount
	Activity       PulseActivity
}

// PulseInitiative is an active initiative ranked by total inbound edges.
type PulseInitiative struct {
	ID           string
	Title        string
	InboundEdges int
}

// PulseHotZone is an object that saw notable activity within the pulse window.
type PulseHotZone struct {
	ObjectID     string
	Title        string
	Type         string
	GraftEdgesIn int
	NewEdgesOut  int
}

// PulsePressure is a named signal with a count.
type PulsePressure struct {
	Kind  string
	Topic string
	Count int
}

// PulseKindCount is one entry in the edge-kind distribution histogram.
type PulseKindCount struct {
	Kind  string
	Count int
}

// PulseActivity is the summary count block for a pulse window.
type PulseActivity struct {
	SporesSubmitted int
	GraftsApplied   int
	NewObjects      int
	NewEdges        int
}
