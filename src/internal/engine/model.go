package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// FlexString accepts any JSON scalar — string, number, boolean or null — and stores
// its canonical string form. Agents naturally write `"value": 3` or `"value": true`;
// rejecting those with a Go unmarshal error would fail the ingest over a formality
// (the log stores all prop values as strings anyway).
type FlexString string

func (f *FlexString) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = FlexString(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err == nil {
		*f = FlexString(n.String())
		return nil
	}
	var t bool
	if err := json.Unmarshal(b, &t); err == nil {
		*f = FlexString(fmt.Sprintf("%t", t))
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		*f = ""
		return nil
	}
	return fmt.Errorf("must be a scalar (string, number, boolean or null), got %s", b)
}

func toStringMap(in map[string]FlexString) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = string(v)
	}
	return out
}

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
	Date         string          `json:"date,omitempty"` // back-date a decision (YYYY-MM-DD or RFC3339); for importing real history
	Refs         []RefInput      `json:"refs,omitempty"`
	SupersedesOn []string        `json:"supersedes_on,omitempty"` // extra element refs this decision is the new authority on
	Mutations    []MutationInput `json:"mutations"`
}

// MutationInput is one structural change. Element references use "kind:name" or just
// "name"; the engine resolves them to deterministic ids against the existing graph —
// a bare name binds to the one element already carrying it (kind "concept" only when
// the name is new), and a kind that contradicts an existing same-named element is
// refused unless the upsert carries `new_element` (see refResolver.resolve).
type MutationInput struct {
	Op string `json:"op"` // upsert_element | add_link | retire_link | set_prop

	// upsert_element:
	Kind  string                `json:"kind,omitempty"`
	Name  string                `json:"name,omitempty"`
	Props map[string]FlexString `json:"props,omitempty"`
	// NewElement declares "same name, deliberately distinct element" (a facet, e.g.
	// concept:LakeFS next to service:LakeFS). Without it, an upsert whose kind
	// disagrees with an existing same-named element is refused instead of silently
	// forking that element's history.
	NewElement bool `json:"new_element,omitempty"`

	// add_link / retire_link:
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	Link string `json:"link,omitempty"` // relationship kind, e.g. PART_OF

	// set_prop:
	Element string     `json:"element,omitempty"`
	Key     string     `json:"key,omitempty"`
	Value   FlexString `json:"value,omitempty"`
}

// RefInput links a decision to an external system (ClickUp task, PR, doc).
type RefInput struct {
	System string `json:"system"`
	URL    string `json:"url"`
}

// IngestResult is returned (as JSON) after an ingest.
type IngestResult struct {
	Ok        bool              `json:"ok"`
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
