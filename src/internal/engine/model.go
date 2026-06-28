package engine

// IngestInput is the JSON payload accepted on stdin by `kg ingest`. The primary write
// is a DECISION carrying a batch of MUTATIONS over the element graph. One ingest may
// carry one decision (`decision`) or several (`decisions`); each becomes one immutable
// event.
type IngestInput struct {
	Decision  *DecisionInput  `json:"decision,omitempty"`
	Decisions []DecisionInput `json:"decisions,omitempty"`
}

// DecisionInput is one decision the AI/human records.
type DecisionInput struct {
	Title        string          `json:"title"`
	Rationale    string          `json:"rationale,omitempty"`
	Author       string          `json:"author,omitempty"`
	Refs         []RefInput      `json:"refs,omitempty"`
	SupersedesOn []string        `json:"supersedes_on,omitempty"` // extra element refs this decision is the new authority on
	Mutations    []MutationInput `json:"mutations"`
}

// MutationInput is one structural change. Element references use "kind:name" (or just
// "name", defaulting kind to "concept"); the engine resolves them to deterministic ids.
type MutationInput struct {
	Op string `json:"op"` // upsert_element | add_link | retire_link | set_prop

	// upsert_element:
	Kind  string            `json:"kind,omitempty"`
	Name  string            `json:"name,omitempty"`
	Props map[string]string `json:"props,omitempty"`

	// add_link / retire_link:
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	Link string `json:"link,omitempty"` // relationship kind, e.g. PART_OF

	// set_prop:
	Element string `json:"element,omitempty"`
	Key     string `json:"key,omitempty"`
	Value   string `json:"value,omitempty"`
}

// RefInput links a decision to an external system (ClickUp task, PR, doc).
type RefInput struct {
	System string `json:"system"`
	URL    string `json:"url"`
}

// IngestResult is returned (as JSON) after an ingest.
type IngestResult struct {
	DryRun    bool              `json:"dry_run"`
	Decisions []DecisionResult  `json:"decisions"`
	Elements  map[string]string `json:"elements"` // name → element id (resolution audit)
	Warnings  []string          `json:"warnings,omitempty"`
}

// DecisionResult reports one recorded decision.
type DecisionResult struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	EventHash  string   `json:"event_hash,omitempty"`
	Lamport    int64    `json:"lamport,omitempty"`
	Shapes     []string `json:"shapes"`
	Supersedes []string `json:"supersedes,omitempty"`
}
