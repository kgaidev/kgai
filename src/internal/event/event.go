// Package event defines the immutable, content-addressed facts that make up the
// knowledge-graph log. The log is the single source of truth; the LadybugDB/Kuzu
// graph is a derived, rebuildable projection of these events.
//
// Model: the live graph is a small, stable set of DOMAIN ELEMENTS (application and
// business things) connected by relationships. A DECISION is an immutable event that
// MUTATES that graph (creates elements, adds/retires links, sets properties) and
// carries who/why/when. The sequence of decisions IS the history of the graph.
//
// Determinism:
//   - Element identity is a hash of (kind, normalized name) → independent recorders
//     converge on one node (automatic dedup).
//   - A Decision id is a content hash of its fields → re-recording identical content
//     is idempotent; any change is a new immutable decision.
//   - An event's hash is the sha256 of its canonical JSON (with hash field empty).
package event

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// Op is the kind of an event. Decisions are asserts; the structural effect lives in
// the decision's mutations (which themselves may add or retire links).
type Op string

const OpAssert Op = "assert"

// Event is one immutable line in a per-install shard: exactly one Decision, applied
// atomically. All ids inside are resolved to final values at write time, so replay is
// a pure, deterministic projection.
type Event struct {
	Hash       string    `json:"hash"`
	Op         Op        `json:"op"`
	Actor      string    `json:"actor"`
	InstallID  string    `json:"install_id"`
	Lamport    int64     `json:"lamport"`
	RecordedAt string    `json:"recorded_at"` // RFC3339 UTC
	Parents    []string  `json:"parents"`     // prior event hashes in THIS shard (hash-chain)
	Decision   *Decision `json:"decision"`
}

// Decision is an immutable change to the element graph, with provenance.
type Decision struct {
	ID         string     `json:"id"`
	Title      string     `json:"title"`
	Rationale  string     `json:"rationale,omitempty"`
	Author     string     `json:"author,omitempty"`
	Refs       string     `json:"refs,omitempty"` // joined "system:url" list
	Summary    string     `json:"summary,omitempty"`
	Supersedes []string   `json:"supersedes,omitempty"` // decision ids this one replaces
	Shapes     []string   `json:"shapes,omitempty"`     // element ids touched (provenance)
	Targets    []string   `json:"targets,omitempty"`    // subset of Shapes this decision is the new AUTHORITY on (drives heads/conflicts)
	Mutations  []Mutation `json:"mutations"`
}

// MutOp enumerates the structural operations a decision can apply.
type MutOp string

const (
	MutUpsertElement MutOp = "upsert_element"
	MutAddLink       MutOp = "add_link"
	MutRetireLink    MutOp = "retire_link"
	MutSetProp       MutOp = "set_prop"
)

// Mutation is one structural change. Element refs are already resolved to ids.
type Mutation struct {
	Op MutOp `json:"op"`
	// upsert_element / set_prop target:
	ElementID string            `json:"element_id,omitempty"`
	Kind      string            `json:"kind,omitempty"`
	Name      string            `json:"name,omitempty"`
	Props     map[string]string `json:"props,omitempty"`
	// add_link / retire_link:
	FromID   string `json:"from_id,omitempty"`
	ToID     string `json:"to_id,omitempty"`
	LinkKind string `json:"link_kind,omitempty"`
	// set_prop:
	Key   string `json:"key,omitempty"`
	Value string `json:"value,omitempty"`
}

func (e Event) canonicalForHash() ([]byte, error) {
	c := e
	c.Hash = ""
	return json.Marshal(c)
}

// ComputeHash sets and returns the content hash of the event.
func (e *Event) ComputeHash() string {
	b, err := e.canonicalForHash()
	if err != nil {
		panic("event: canonical marshal failed: " + err.Error())
	}
	sum := sha256.Sum256(b)
	e.Hash = "sha256:" + hex.EncodeToString(sum[:])
	return e.Hash
}

// Verify recomputes the hash and reports whether it matches the stored one.
func (e Event) Verify() bool {
	want := e.Hash
	b, err := e.canonicalForHash()
	if err != nil {
		return false
	}
	sum := sha256.Sum256(b)
	return want == "sha256:"+hex.EncodeToString(sum[:])
}

// Normalize canonicalizes a name for identity resolution: lowercase, trim, collapse
// internal whitespace.
func Normalize(name string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(name))), " ")
}

func shortHash(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:])
}

// ElementID is the deterministic identity for a domain element (kind, name).
// Independent of who records it or when, so concurrent recorders converge.
func ElementID(kind, name string) string {
	return "el_" + shortHash(Normalize(kind), Normalize(name))[:20]
}

// DecisionID is the content hash of a decision's INTENT: title, rationale, author and
// mutations. It deliberately excludes Supersedes — supersession is a function of graph
// state at ingest time, not of the decision's identity — so re-recording byte-identical
// content is idempotent (same id) regardless of what it currently supersedes.
func DecisionID(d Decision) string {
	muts, _ := json.Marshal(d.Mutations) // map keys are sorted by encoding/json
	return "d_" + shortHash(
		Normalize(d.Title), d.Rationale, Normalize(d.Author), string(muts),
	)[:24]
}
