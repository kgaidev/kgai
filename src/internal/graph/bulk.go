package graph

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"kgai/internal/event"
)

// BulkLoad projects a full, canonically-sorted event log into an EMPTY graph via
// Kuzu's COPY FROM bulk import: the events are replayed into an in-memory state that
// mirrors ApplyEvent's semantics exactly, then loaded as a handful of CSV COPYs
// instead of one MERGE statement per fact. On a 20k-decision log this turns a
// minutes-long rebuild into seconds. The caller guarantees the database is fresh
// (rebuild path) and events are verified and sorted.
func (g *Graph) BulkLoad(events []event.Event) (int, error) {
	st := newMemState()
	for _, ev := range events {
		st.apply(ev)
	}

	dir, err := os.MkdirTemp("", "kg-bulk-*")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)

	write := func(name string, header []string, rows [][]string) (string, int, error) {
		p := filepath.Join(dir, name)
		f, err := os.Create(p)
		if err != nil {
			return "", 0, err
		}
		w := csv.NewWriter(f)
		_ = w.Write(header)
		if err := w.WriteAll(rows); err != nil {
			f.Close()
			return "", 0, err
		}
		w.Flush()
		if err := f.Close(); err != nil {
			return "", 0, err
		}
		return p, len(rows), nil
	}

	copyIn := func(table, path string, n int) error {
		if n == 0 {
			return nil
		}
		// PARALLEL=FALSE: props/rationales legitimately contain newlines, and Kuzu's
		// parallel CSV reader cannot split multi-line quoted records.
		q := fmt.Sprintf(`COPY %s FROM '%s' (HEADER=true, PARALLEL=FALSE)`, table, escPath(path))
		return g.exec(q, nil)
	}

	// ---- nodes -----------------------------------------------------------------
	var rows [][]string
	for _, id := range sortedKeysOf(st.elements) {
		e := st.elements[id]
		rows = append(rows, []string{id, e.kind, e.name, e.props})
	}
	p, n, err := write("element.csv", []string{"id", "kind", "name", "props"}, rows)
	if err != nil {
		return 0, err
	}
	if err := copyIn("Element", p, n); err != nil {
		return 0, err
	}

	rows = nil
	for _, id := range sortedKeysOf(st.decisions) {
		d := st.decisions[id]
		rows = append(rows, []string{id, d.title, d.rationale, d.author, d.refs, d.summary,
			d.recordedAt, strconv.FormatInt(d.lamport, 10), d.installID, d.eventHash})
	}
	p, n, err = write("decision.csv", []string{"id", "title", "rationale", "author", "refs",
		"summary", "recorded_at", "lamport", "install_id", "event_hash"}, rows)
	if err != nil {
		return 0, err
	}
	if err := copyIn("Decision", p, n); err != nil {
		return 0, err
	}

	rows = nil
	for _, h := range st.appliedOrder {
		rows = append(rows, []string{h})
	}
	p, n, err = write("applied.csv", []string{"hash"}, rows)
	if err != nil {
		return 0, err
	}
	if err := copyIn("_Applied", p, n); err != nil {
		return 0, err
	}

	// ---- rels ------------------------------------------------------------------
	rows = nil
	for _, k := range sortedKeysOf(st.links) {
		l := st.links[k]
		rows = append(rows, []string{l.from, l.to, l.kind, l.createdBy})
	}
	p, n, err = write("link.csv", []string{"from", "to", "kind", "created_by"}, rows)
	if err != nil {
		return 0, err
	}
	if err := copyIn("LINK", p, n); err != nil {
		return 0, err
	}

	rows = nil
	for _, k := range sortedKeysOf(st.shapes) {
		s := st.shapes[k]
		rows = append(rows, []string{s.dec, s.el, strconv.FormatBool(s.authority)})
	}
	p, n, err = write("shapes.csv", []string{"from", "to", "authority"}, rows)
	if err != nil {
		return 0, err
	}
	if err := copyIn("SHAPES", p, n); err != nil {
		return 0, err
	}

	rows = nil
	for _, k := range sortedKeysOf(st.supersedes) {
		s := st.supersedes[k]
		rows = append(rows, []string{s.a, s.b})
	}
	p, n, err = write("supersedes.csv", []string{"from", "to"}, rows)
	if err != nil {
		return 0, err
	}
	if err := copyIn("SUPERSEDES", p, n); err != nil {
		return 0, err
	}

	// Kuzu's CSV reader turns empty fields into NULLs; the statement path stores empty
	// strings. Normalize so both paths yield byte-identical canonical exports.
	if err := g.exec(`MATCH (e:Element) SET e.kind=coalesce(e.kind,''),
		e.name=coalesce(e.name,''), e.props=coalesce(e.props,'')`, nil); err != nil {
		return 0, err
	}
	if err := g.exec(`MATCH (d:Decision) SET d.title=coalesce(d.title,''),
		d.rationale=coalesce(d.rationale,''), d.author=coalesce(d.author,''),
		d.refs=coalesce(d.refs,''), d.summary=coalesce(d.summary,''),
		d.install_id=coalesce(d.install_id,''), d.event_hash=coalesce(d.event_hash,'')`, nil); err != nil {
		return 0, err
	}

	return len(st.appliedOrder), nil
}

func escPath(p string) string { return p } // MkdirTemp paths contain no quotes

func sortedKeysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- in-memory projection (must mirror ApplyEvent exactly) ---------------------

type memElement struct{ kind, name, props string }

type memDecision struct {
	title, rationale, author, refs, summary string
	recordedAt                              string
	lamport                                 int64
	installID, eventHash                    string
}

type memLink struct{ from, to, kind, createdBy string }
type memShape struct {
	dec, el   string
	authority bool
}
type memSup struct{ a, b string }

type memState struct {
	elements     map[string]*memElement
	decisions    map[string]*memDecision
	links        map[string]memLink
	shapes       map[string]memShape
	supersedes   map[string]memSup
	applied      map[string]bool
	appliedOrder []string
}

func newMemState() *memState {
	return &memState{
		elements:   map[string]*memElement{},
		decisions:  map[string]*memDecision{},
		links:      map[string]memLink{},
		shapes:     map[string]memShape{},
		supersedes: map[string]memSup{},
		applied:    map[string]bool{},
	}
}

func (st *memState) ensureElement(id, kind, name string) *memElement {
	if id == "" {
		return nil
	}
	if e, ok := st.elements[id]; ok {
		return e // ON CREATE semantics: existing kind/name are never overwritten
	}
	e := &memElement{kind: kind, name: name}
	st.elements[id] = e
	return e
}

func (st *memState) ensureDecision(id string) *memDecision {
	if d, ok := st.decisions[id]; ok {
		return d
	}
	d := &memDecision{}
	st.decisions[id] = d
	return d
}

func (st *memState) apply(ev event.Event) {
	if st.applied[ev.Hash] {
		return
	}
	st.applied[ev.Hash] = true
	st.appliedOrder = append(st.appliedOrder, ev.Hash)
	if ev.Decision == nil {
		return
	}
	d := ev.Decision

	// 1) Decision node (ON CREATE semantics).
	if _, exists := st.decisions[d.ID]; !exists {
		st.decisions[d.ID] = &memDecision{
			title: d.Title, rationale: d.Rationale, author: d.Author, refs: d.Refs,
			summary: d.Summary, recordedAt: fmtBulkTime(ev.RecordedAt),
			lamport: ev.Lamport, installID: ev.InstallID, eventHash: ev.Hash,
		}
	}

	// 2) Mutations, in order.
	for _, m := range d.Mutations {
		switch m.Op {
		case event.MutUpsertElement:
			e := st.ensureElement(m.ElementID, m.Kind, m.Name)
			for _, k := range sortedKeysOf(m.Props) {
				e.props = setProp(e.props, k, m.Props[k])
			}
		case event.MutSetProp:
			e := st.ensureElement(m.ElementID, "", "")
			e.props = setProp(e.props, m.Key, m.Value)
		case event.MutAddLink:
			st.ensureElement(m.FromID, "", "")
			st.ensureElement(m.ToID, "", "")
			key := m.FromID + "\x00" + m.LinkKind + "\x00" + m.ToID
			if _, ok := st.links[key]; !ok {
				st.links[key] = memLink{from: m.FromID, to: m.ToID, kind: m.LinkKind, createdBy: m.ElementID}
			}
		case event.MutRetireLink:
			delete(st.links, m.FromID+"\x00"+m.LinkKind+"\x00"+m.ToID)
		}
	}

	// 3) Provenance (authority per Targets; legacy events without Targets → all true).
	targets := map[string]bool{}
	for _, t := range d.Targets {
		targets[t] = true
	}
	for _, eid := range d.Shapes {
		st.ensureElement(eid, "", "")
		key := d.ID + "\x00" + eid
		if _, ok := st.shapes[key]; !ok {
			st.shapes[key] = memShape{dec: d.ID, el: eid, authority: targets[eid] || len(d.Targets) == 0}
		}
	}

	// 4) Supersession.
	for _, prev := range d.Supersedes {
		st.ensureDecision(prev)
		key := d.ID + "\x00" + prev
		if _, ok := st.supersedes[key]; !ok {
			st.supersedes[key] = memSup{a: d.ID, b: prev}
		}
	}
}

// fmtBulkTime renders RecordedAt exactly as the statement path stores it (parseTime →
// time.Time bound as TIMESTAMP), so both paths yield identical canonical exports.
func fmtBulkTime(s string) string {
	t := parseTime(s)
	return t.UTC().Format("2006-01-02 15:04:05")
}

var _ = time.Now // keep time import if parseTime moves
