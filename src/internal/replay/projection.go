// Package replay is the decision log's in-memory projection, with no database behind
// it: the same MERGE / ON CREATE semantics graph.ApplyEvent gives the Kuzu graph,
// applied to plain Go maps. It has two consumers that must never disagree:
//
//   - graph.BulkLoad, which replays a full log here first and then COPYs the result
//     into a fresh graph (the fast path for rebuilds and cold clones);
//   - kgview, the read-only preview app, which shows a store from its log alone —
//     it never opens graph.kuzu, so it can never collide with the engine's single
//     writer (Kuzu allows one read-write process OR read-only ones, not both).
//
// Because the log is the source of truth and this projection is deterministic,
// everything the app shows — heads, conflicts, history, statistics — is exactly what
// `kg` would answer from the graph. The equivalence is tested in internal/engine
// against a real Kuzu projection.
package replay

import (
	"sort"
	"strings"
	"time"

	"kgai/internal/event"
)

// Element is a live domain element: current kind/name and its props blob.
type Element struct {
	ID, Kind, Name string
	// Props is the newline-delimited "key=value" blob the graph stores, keys sorted
	// (see SetProp). ParseProps turns it into a map.
	Props string
}

// Decision is the immutable event that shaped the graph, with the projection-level
// fields the graph stores plus what the app needs from the enclosing event.
type Decision struct {
	ID, Title, Rationale, Author, Refs, Summary string
	// RecordedAt is the timestamp as the graph stores it ("2006-01-02 15:04:05" UTC);
	// Time is the same instant parsed (zero when the event carried no usable time).
	RecordedAt string
	Time       time.Time
	Lamport    int64
	InstallID  string
	EventHash  string
	// Actor is the identity of the install that recorded the event (event.actor). It
	// is not stored in the graph; Author (the credited author) defaults to it at ingest.
	Actor string

	Supersedes     []string
	Shapes         []string
	Targets        []string
	ProvenanceOnly bool
	Mutations      []event.Mutation

	// Stub marks a decision known only as the target of another's SUPERSEDES: its own
	// event has not arrived (a partial log). ON CREATE semantics keep it empty even
	// if the event replays later, exactly as the graph does.
	Stub bool
}

// Link is a current edge between two elements.
type Link struct{ From, To, Kind, CreatedBy string }

// Shape records that a decision touched an element; Authority is true when the
// decision governs that element (drives heads and conflicts).
type Shape struct {
	Decision, Element string
	Authority         bool
}

// Supersession is one SUPERSEDES edge: From replaces To.
type Supersession struct{ From, To string }

// Projection is the replayed state. Maps are keyed as the graph's primary keys are
// (LinkKey, ShapeKey, SupKey); iterate them through SortedKeys for a deterministic
// order.
type Projection struct {
	Elements     map[string]*Element
	Decisions    map[string]*Decision
	Links        map[string]Link
	Shapes       map[string]Shape
	Supersedes   map[string]Supersession
	Applied      map[string]bool
	AppliedOrder []string

	// What the mutations did, counted for statistics (not part of the graph).
	RetiredLinks int
	SetProps     int

	supBy      map[string][]string // decision → decisions that supersede it
	shapedBy   map[string][]string // element → decisions shaping it
	decisionEl map[string][]string // decision → elements it shapes
}

// New returns an empty projection.
func New() *Projection {
	return &Projection{
		Elements:   map[string]*Element{},
		Decisions:  map[string]*Decision{},
		Links:      map[string]Link{},
		Shapes:     map[string]Shape{},
		Supersedes: map[string]Supersession{},
		Applied:    map[string]bool{},
	}
}

// Build replays events in canonical (lamport, hash) order — the total order every
// store agrees on — into a fresh projection. The input slice is not modified.
func Build(evs []event.Event) *Projection {
	sorted := make([]event.Event, len(evs))
	copy(sorted, evs)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Lamport != sorted[j].Lamport {
			return sorted[i].Lamport < sorted[j].Lamport
		}
		return sorted[i].Hash < sorted[j].Hash
	})
	p := New()
	for _, ev := range sorted {
		p.Apply(ev)
	}
	return p
}

// LinkKey, ShapeKey and SupKey are the map keys, mirroring the graph's primary keys.
func LinkKey(from, kind, to string) string { return from + "\x00" + kind + "\x00" + to }
func ShapeKey(dec, el string) string       { return dec + "\x00" + el }
func SupKey(from, to string) string        { return from + "\x00" + to }

func (p *Projection) ensureElement(id, kind, name string) *Element {
	if id == "" {
		return nil
	}
	if e, ok := p.Elements[id]; ok {
		return e // ON CREATE semantics: existing kind/name are never overwritten
	}
	e := &Element{ID: id, Kind: kind, Name: name}
	p.Elements[id] = e
	return e
}

func (p *Projection) ensureDecision(id string) *Decision {
	if d, ok := p.Decisions[id]; ok {
		return d
	}
	d := &Decision{ID: id, Stub: true}
	p.Decisions[id] = d
	return d
}

// Apply projects one event. It must mirror graph.ApplyEvent exactly: every write is
// idempotent (an event is applied once; MERGE-style ON CREATE for nodes and edges),
// and the order of mutations inside a decision is preserved.
func (p *Projection) Apply(ev event.Event) {
	if p.Applied[ev.Hash] {
		return
	}
	p.Applied[ev.Hash] = true
	p.AppliedOrder = append(p.AppliedOrder, ev.Hash)
	if ev.Decision == nil {
		return
	}
	d := ev.Decision
	p.supBy, p.shapedBy, p.decisionEl = nil, nil, nil // derived indexes are rebuilt lazily

	// 1) Decision node (ON CREATE semantics).
	if _, exists := p.Decisions[d.ID]; !exists {
		p.Decisions[d.ID] = &Decision{
			ID: d.ID, Title: d.Title, Rationale: d.Rationale, Author: d.Author, Refs: d.Refs,
			Summary: d.Summary, RecordedAt: FormatRecordedAt(ev.RecordedAt), Time: ParseTime(ev.RecordedAt),
			Lamport: ev.Lamport, InstallID: ev.InstallID, EventHash: ev.Hash, Actor: ev.Actor,
			Supersedes: d.Supersedes, Shapes: d.Shapes, Targets: d.Targets,
			ProvenanceOnly: d.ProvenanceOnly, Mutations: d.Mutations,
		}
	}

	// 2) Mutations, in order.
	for _, m := range d.Mutations {
		switch m.Op {
		case event.MutUpsertElement:
			e := p.ensureElement(m.ElementID, m.Kind, m.Name)
			if e == nil {
				continue
			}
			for _, k := range SortedKeys(m.Props) {
				e.Props = SetProp(e.Props, k, m.Props[k])
			}
		case event.MutSetProp:
			if e := p.ensureElement(m.ElementID, "", ""); e != nil {
				e.Props = SetProp(e.Props, m.Key, m.Value)
			}
			p.SetProps++
		case event.MutAddLink:
			p.ensureElement(m.FromID, "", "")
			p.ensureElement(m.ToID, "", "")
			key := LinkKey(m.FromID, m.LinkKind, m.ToID)
			if _, ok := p.Links[key]; !ok {
				p.Links[key] = Link{From: m.FromID, To: m.ToID, Kind: m.LinkKind, CreatedBy: m.ElementID}
			}
		case event.MutRetireLink:
			delete(p.Links, LinkKey(m.FromID, m.LinkKind, m.ToID))
			p.RetiredLinks++
		}
	}

	// 3) Provenance (authority per Targets; legacy events without Targets → all true).
	targets := map[string]bool{}
	for _, t := range d.Targets {
		targets[t] = true
	}
	for _, eid := range d.Shapes {
		p.ensureElement(eid, "", "")
		key := ShapeKey(d.ID, eid)
		if _, ok := p.Shapes[key]; !ok {
			p.Shapes[key] = Shape{Decision: d.ID, Element: eid,
				Authority: targets[eid] || (len(d.Targets) == 0 && !d.ProvenanceOnly)}
		}
	}

	// 4) Supersession.
	for _, prev := range d.Supersedes {
		p.ensureDecision(prev)
		key := SupKey(d.ID, prev)
		if _, ok := p.Supersedes[key]; !ok {
			p.Supersedes[key] = Supersession{From: d.ID, To: prev}
		}
	}
}

// SortedKeys returns a map's keys in ascending order — the projection's one and only
// iteration order, so two machines replaying the same log describe it the same way.
func SortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// SetProp updates one key in a newline-delimited "key=value" props blob, preserving
// the other keys. The live graph is tiny, so a flat text blob is enough.
func SetProp(blob, key, value string) string {
	key = strings.TrimSpace(key)
	// The blob is newline-delimited "key=value"; neutralize separators in inputs so a
	// value can never forge another key (the key never legitimately contains '=').
	key = strings.NewReplacer("\n", " ", "\r", " ", "=", "-").Replace(key)
	value = strings.NewReplacer("\n", " ", "\r", " ").Replace(value)
	var out []string
	replaced := false
	for _, line := range strings.Split(blob, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if k := strings.SplitN(line, "=", 2)[0]; k == key {
			out = append(out, key+"="+value)
			replaced = true
		} else {
			out = append(out, line)
		}
	}
	if !replaced {
		out = append(out, key+"="+value)
	}
	// Keep lines sorted by key so the blob is independent of apply order — this keeps
	// the projection deterministic across machines (incremental vs full replay).
	sort.Slice(out, func(i, j int) bool {
		return strings.SplitN(out[i], "=", 2)[0] < strings.SplitN(out[j], "=", 2)[0]
	})
	return strings.Join(out, "\n")
}

// ParseProps turns a props blob back into a map (nil when empty).
func ParseProps(blob string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(blob, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		kv := strings.SplitN(line, "=", 2)
		if len(kv) == 2 {
			out[kv[0]] = kv[1]
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ParseTime reads an event timestamp (RFC3339, or a date-only import) as UTC; the
// zero time when it is empty or unreadable.
func ParseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// FormatRecordedAt renders an event timestamp exactly as the graph stores it (ParseTime →
// bound as TIMESTAMP), so the bulk and statement paths yield identical canonical exports.
func FormatRecordedAt(s string) string {
	return ParseTime(s).UTC().Format("2006-01-02 15:04:05")
}
