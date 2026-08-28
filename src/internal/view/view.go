package view

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"kgai/internal/event"
	"kgai/internal/replay"
)

// The read model an editor extension shows: every screen of the store as plain data,
// in the reader's words (recorded by, credited to, install, head / superseded / note,
// defined element / set property / added link / removed link). One fixed order per
// list, exact filters, nothing ranked — the same answers as kg, without the engine.

// Count is one bar of a tally.
type Count struct {
	Key string `json:"key"`
	N   int    `json:"n"`
}

// ElementCount is a tally row that names an element.
type ElementCount struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	N    int    `json:"n"`
}

func counts(cs []replay.Count) []Count {
	out := make([]Count, 0, len(cs))
	for _, c := range cs {
		out = append(out, Count{Key: c.Key, N: c.N})
	}
	return out
}

func elementCounts(cs []replay.ElementCount) []ElementCount {
	out := make([]ElementCount, 0, len(cs))
	for _, c := range cs {
		name := c.Name
		if name == "" {
			name = c.ID
		}
		out = append(out, ElementCount{ID: c.ID, Kind: c.Kind, Name: name, N: c.N})
	}
	return out
}

// ---- the store itself -----------------------------------------------------------------

// Counts are the headline numbers.
type Counts struct {
	Decisions int `json:"decisions"`
	Elements  int `json:"elements"`
	Links     int `json:"links"`
	Conflicts int `json:"conflicts"`
	People    int `json:"people"`
	Installs  int `json:"installs"`
}

// Store says which store is shown, how it was chosen, and whether it could be read.
type Store struct {
	Dir     string `json:"dir"`
	Project string `json:"project"`
	Root    string `json:"root"`
	Rule    string `json:"rule"`   // the rule that chose Root, as a phrase
	Source  string `json:"source"` // the same in kg's words: default, .kgairc, config.json, KGAI_STORE
	Pending string `json:"pending,omitempty"`
	// Error is the problem that left the store unread; Hint says what to do about it.
	Error     string `json:"error,omitempty"`
	Hint      string `json:"hint,omitempty"`
	InstallID string `json:"installId,omitempty"`
	Actor     string `json:"actor,omitempty"`
	Remote    string `json:"remote,omitempty"`
	Transport string `json:"transport"`
	Sync      string `json:"sync"`
	Loaded    string `json:"loaded"`
	Counts    Counts `json:"counts"`
}

// Status describes the model's store.
func (m *Model) Status() Store {
	s := Store{Dir: m.Dir, Project: m.Project(), Root: m.Root, Rule: m.LocationRule(), Source: m.SourceLabel(),
		Pending: m.Pending, Transport: m.Transport, Sync: m.SyncLabel(), Loaded: m.Loaded.UTC().Format(time.RFC3339)}
	if m.Err != nil {
		s.Error = m.Err.Error()
		s.Hint = m.Explain()
	}
	if m.Store != nil {
		s.InstallID, s.Actor, s.Remote = m.Store.Config.InstallID, m.Store.Config.Actor, m.Remote
		st := m.Stats
		s.Counts = Counts{Decisions: st.Decisions, Elements: st.Elements, Links: st.Links, Conflicts: st.Conflicts,
			People: len(st.DecisionsByActor), Installs: len(st.Shards)}
	}
	return s
}

// Explain turns the store's error into a sentence with a way out.
func (m *Model) Explain() string {
	if m.Err == nil {
		return ""
	}
	msg := m.Err.Error()
	if strings.Contains(msg, "not initialized") {
		return fmt.Sprintf("No knowledge graph here yet: kg would use %s, but nothing was recorded. Open a project folder that has one, or run `kg init` in this one.", m.Root)
	}
	return "Cannot open the store: " + msg
}

// Install is one shard of the log.
type Install struct {
	ID        string  `json:"id"`
	Decisions int     `json:"decisions"`
	Last      string  `json:"last"`
	Actors    []Count `json:"actors"`
}

// Overview is the store at a glance.
type Overview struct {
	Store            Store          `json:"store"`
	First            string         `json:"first"`
	Last             string         `json:"last"`
	ElementsByKind   []Count        `json:"elementsByKind"`
	LinksByKind      []Count        `json:"linksByKind"`
	DecisionsByMonth []Count        `json:"decisionsByMonth"`
	DecisionsByActor []Count        `json:"decisionsByActor"`
	MostShaped       []ElementCount `json:"mostShaped"`
	Installs         []Install      `json:"installs"`
	Events           int            `json:"events"`
	SetProps         int            `json:"setProps"`
	RetiredLinks     int            `json:"retiredLinks"`
	Notes            int            `json:"notes"`
	Stubs            int            `json:"stubs"`
}

// Overview gathers the tallies of the store.
func (m *Model) Overview() Overview {
	st := m.Stats
	o := Overview{Store: m.Status(), First: day(st.First), Last: day(st.Last),
		ElementsByKind: counts(st.ElementsByKind), LinksByKind: counts(st.LinksByKind),
		DecisionsByMonth: counts(st.DecisionsByMonth), DecisionsByActor: counts(st.DecisionsByActor),
		MostShaped: elementCounts(st.MostShaped), Installs: []Install{},
		Events: st.Events, SetProps: st.SetProps, RetiredLinks: st.RetiredLinks, Notes: st.ProvenanceOnly, Stubs: st.Stubs}
	for _, sh := range st.Shards {
		o.Installs = append(o.Installs, Install{ID: sh.ID, Decisions: sh.Decisions, Last: day(sh.Last), Actors: counts(sh.Actors)})
	}
	return o
}

// ---- decisions ------------------------------------------------------------------------

// Row is one decision in a list.
type Row struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Day    string `json:"day"`
	Time   string `json:"time"`
	Actor  string `json:"actor"`
	Author string `json:"author"`
	State  string `json:"state"` // head | superseded | note
}

// Decisions is a filtered list with its counts; Error reports a malformed expression.
type Decisions struct {
	Rows  []Row  `json:"rows"`
	Total int    `json:"total"`
	Shown int    `json:"shown"`
	Error string `json:"error,omitempty"`
}

func (m *Model) row(d *replay.Decision) Row {
	return Row{ID: d.ID, Title: d.Title, Day: day(d.Time), Time: stamp(d.Time), Actor: d.Actor, Author: d.Author, State: m.State(d.ID)}
}

// DecisionRows applies the filter.
func (m *Model) DecisionRows(f DecisionFilter) Decisions {
	out := Decisions{Rows: []Row{}, Total: m.Stats.Decisions}
	ds, err := m.Decisions(f)
	if err != nil {
		out.Error = "The regular expression is not valid: " + err.Error()
		return out
	}
	for _, d := range ds {
		out.Rows = append(out.Rows, m.row(d))
	}
	out.Shown = len(out.Rows)
	return out
}

// Ref is one reference of a decision ("system:url").
type Ref struct {
	System string `json:"system"`
	URL    string `json:"url,omitempty"`
	Text   string `json:"text"`
}

// Mutation is one structural change, in words.
type Mutation struct {
	Op    string `json:"op"`
	Label string `json:"label"` // defined element | set property | added link | removed link
	Text  string `json:"text"`
}

// Shaped is an element a decision touched and the decision's role on it.
type Shaped struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	Role string `json:"role"` // governs | superseded | mentioned
}

// DecisionRef points at another decision; InLog is false for one this log lacks.
type DecisionRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Day   string `json:"day"`
	Actor string `json:"actor"`
	InLog bool   `json:"inLog"`
}

// Decision is one decision in full.
type Decision struct {
	Row
	StateSentence string        `json:"stateSentence"`
	Rationale     string        `json:"rationale"`
	Summary       string        `json:"summary"`
	Refs          []Ref         `json:"refs"`
	Mutations     []Mutation    `json:"mutations"`
	Elements      []Shaped      `json:"elements"`
	Supersedes    []DecisionRef `json:"supersedes"`
	SupersededBy  []DecisionRef `json:"supersededBy"`
	InstallID     string        `json:"installId"`
	Lamport       int64         `json:"lamport"`
	EventHash     string        `json:"eventHash"`
}

// Decision returns one decision; false when the log has no such decision.
func (m *Model) Decision(id string) (*Decision, bool) {
	p := m.Proj
	d, ok := p.Decisions[id]
	if !ok || d.Stub {
		return nil, false
	}
	out := &Decision{Row: m.row(d), Rationale: d.Rationale, Summary: d.Summary, Refs: parseRefs(d.Refs),
		Mutations: []Mutation{}, Elements: []Shaped{}, Supersedes: []DecisionRef{}, SupersededBy: []DecisionRef{},
		InstallID: d.InstallID, Lamport: d.Lamport, EventHash: d.EventHash}
	out.StateSentence = m.stateSentence(id, out.State)
	for _, mu := range d.Mutations {
		out.Mutations = append(out.Mutations, mutation(p, mu))
	}
	for _, el := range p.ElementsOf(id) {
		e := p.Elements[el]
		role := "mentioned"
		if p.Shapes[replay.ShapeKey(id, el)].Authority {
			role = "superseded"
			if p.IsHead(id, el) {
				role = "governs"
			}
		}
		out.Elements = append(out.Elements, Shaped{ID: el, Kind: kindOf(e), Name: p.ElementName(el), Role: role})
	}
	for _, prev := range d.Supersedes {
		out.Supersedes = append(out.Supersedes, m.decisionRef(prev))
	}
	for _, next := range p.SupersededBy(id) {
		out.SupersededBy = append(out.SupersededBy, m.decisionRef(next))
	}
	return out, true
}

func (m *Model) decisionRef(id string) DecisionRef {
	d := m.Proj.Decisions[id]
	if d == nil || d.Stub {
		return DecisionRef{ID: id}
	}
	return DecisionRef{ID: id, Title: d.Title, Day: day(d.Time), Actor: d.Actor, InLog: true}
}

// stateSentence spells out what the state means for this decision.
func (m *Model) stateSentence(id, state string) string {
	p := m.Proj
	switch state {
	case "head":
		n := 0
		for _, el := range p.ElementsOf(id) {
			if p.IsHead(id, el) {
				n++
			}
		}
		return fmt.Sprintf("still governs %s", plural(n, "element", "elements"))
	case "superseded":
		return fmt.Sprintf("replaced by %s", plural(len(p.SupersededBy(id)), "later decision", "later decisions"))
	}
	return "context only, governs nothing"
}

func parseRefs(refs string) []Ref {
	out := []Ref{}
	for _, r := range strings.Split(refs, ",") {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		system, target := r, ""
		if i := strings.Index(r, ":"); i > 0 {
			system, target = r[:i], strings.TrimSpace(r[i+1:])
		}
		ref := Ref{System: system, Text: r}
		if u, err := url.Parse(target); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
			ref.URL, ref.Text = target, target
		}
		out = append(out, ref)
	}
	return out
}

func kindOf(e *replay.Element) string {
	if e == nil || e.Kind == "" {
		return "?"
	}
	return e.Kind
}

func elementLabel(p *replay.Projection, id string) string {
	if e := p.Elements[id]; e != nil && e.Kind != "" {
		return e.Kind + ": " + p.ElementName(id)
	}
	return p.ElementName(id)
}

func mutationLabel(op event.MutOp) string {
	switch op {
	case event.MutUpsertElement:
		return "defined element"
	case event.MutSetProp:
		return "set property"
	case event.MutAddLink:
		return "added link"
	case event.MutRetireLink:
		return "removed link"
	}
	return string(op)
}

func mutation(p *replay.Projection, mu event.Mutation) Mutation {
	var text string
	switch mu.Op {
	case event.MutUpsertElement:
		text = fmt.Sprintf("%s: %s", mu.Kind, mu.Name)
		for _, k := range replay.SortedKeys(mu.Props) {
			text += fmt.Sprintf("\n    %s = %s", k, mu.Props[k])
		}
	case event.MutSetProp:
		text = fmt.Sprintf("%s    %s = %s", elementLabel(p, mu.ElementID), mu.Key, mu.Value)
	case event.MutAddLink, event.MutRetireLink:
		text = fmt.Sprintf("%s  -%s->  %s", elementLabel(p, mu.FromID), mu.LinkKind, elementLabel(p, mu.ToID))
	}
	return Mutation{Op: string(mu.Op), Label: mutationLabel(mu.Op), Text: text}
}

// ---- elements -------------------------------------------------------------------------

// ElementRow is one element in a list: how many decisions shaped it, whether it is contested.
type ElementRow struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Decisions int    `json:"decisions"`
	Conflict  bool   `json:"conflict"`
}

// ElementGroup is one kind with its elements, name order.
type ElementGroup struct {
	Kind     string       `json:"kind"`
	Count    int          `json:"count"`
	Elements []ElementRow `json:"elements"`
}

// ElementGroups lists the elements grouped by kind, narrowed to one kind and/or a
// name substring (case-insensitive; an id substring matches too).
func (m *Model) ElementGroups(kind, text string) (groups []ElementGroup, shown int) {
	groups = []ElementGroup{}
	text = strings.ToLower(strings.TrimSpace(text))
	for _, e := range m.Elements() {
		if kind != "" && e.Kind != kind {
			continue
		}
		if text != "" && !strings.Contains(strings.ToLower(e.Name), text) && !strings.Contains(e.ID, text) {
			continue
		}
		label := e.Kind
		if label == "" {
			label = "(no kind)"
		}
		if len(groups) == 0 || groups[len(groups)-1].Kind != label {
			groups = append(groups, ElementGroup{Kind: label, Elements: []ElementRow{}})
		}
		g := &groups[len(groups)-1]
		g.Elements = append(g.Elements, ElementRow{ID: e.ID, Kind: e.Kind, Name: m.Proj.ElementName(e.ID),
			Decisions: len(m.Proj.ShapedBy(e.ID)), Conflict: len(m.Proj.Heads(e.ID)) > 1})
		g.Count++
		shown++
	}
	return groups, shown
}

// KV is one property.
type KV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// ElementRef names an element.
type ElementRef struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// ElementLink is one live edge seen from an element.
type ElementLink struct {
	Kind      string     `json:"kind"`
	Direction string     `json:"direction"` // out | in
	Other     ElementRef `json:"other"`
}

// HistoryRow is one decision that shaped an element.
type HistoryRow struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Day   string `json:"day"`
	Actor string `json:"actor"`
	State string `json:"state"` // head | superseded | note, for this element
}

// Element is one element in full.
type Element struct {
	ID       string        `json:"id"`
	Kind     string        `json:"kind"`
	Name     string        `json:"name"`
	Standing string        `json:"standing"`
	Heads    int           `json:"heads"`
	ShapedBy int           `json:"shapedBy"`
	Props    []KV          `json:"props"`
	Links    []ElementLink `json:"links"`
	History  []HistoryRow  `json:"history"` // oldest first
}

// Element returns one element; false when there is none with that id.
func (m *Model) Element(id string) (*Element, bool) {
	p := m.Proj
	e, ok := p.Elements[id]
	if !ok {
		return nil, false
	}
	heads := p.Heads(id)
	out := &Element{ID: id, Kind: kindOf(e), Name: p.ElementName(id), Heads: len(heads), ShapedBy: len(p.ShapedBy(id)),
		Props: []KV{}, Links: []ElementLink{}, History: []HistoryRow{}}
	switch {
	case len(heads) > 1:
		out.Standing = fmt.Sprintf("Contested: %d decisions govern it at once", len(heads))
	case len(heads) == 1:
		out.Standing = "Governed by 1 current decision"
	default:
		out.Standing = "Governed by no decision"
	}
	props := replay.ParseProps(e.Props)
	for _, k := range replay.SortedKeys(props) {
		out.Props = append(out.Props, KV{Key: k, Value: props[k]})
	}
	links, in := p.LinksOf(id)
	for _, l := range links {
		out.Links = append(out.Links, ElementLink{Kind: l.Kind, Direction: "out", Other: m.elementRef(l.To)})
	}
	for _, l := range in {
		out.Links = append(out.Links, ElementLink{Kind: l.Kind, Direction: "in", Other: m.elementRef(l.From)})
	}
	for _, h := range p.History(id) {
		d := h.Decision
		state := "superseded"
		if h.IsHead {
			state = "head"
		} else if !p.Shapes[replay.ShapeKey(d.ID, id)].Authority {
			state = "note"
		}
		out.History = append(out.History, HistoryRow{ID: d.ID, Title: d.Title, Day: day(d.Time), Actor: d.Actor, State: state})
	}
	return out, true
}

func (m *Model) elementRef(id string) ElementRef {
	return ElementRef{ID: id, Kind: kindOf(m.Proj.Elements[id]), Name: m.Proj.ElementName(id)}
}

// ---- conflicts ------------------------------------------------------------------------

// ConflictHead is one of the competing decisions on a contested element.
type ConflictHead struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Time      string     `json:"time"`
	Day       string     `json:"day"`
	Actor     string     `json:"actor"`
	Rationale string     `json:"rationale"`
	Mutations []Mutation `json:"mutations"`
}

// Conflict is a contested element with its heads, newest first.
type Conflict struct {
	ElementID string         `json:"elementId"`
	Kind      string         `json:"kind"`
	Name      string         `json:"name"`
	Heads     []ConflictHead `json:"heads"`
}

// Conflicts lists the contested elements.
func (m *Model) Conflicts() []Conflict {
	p := m.Proj
	out := []Conflict{}
	for _, c := range p.Conflicts() {
		kind := c.Kind
		if kind == "" {
			kind = "?"
		}
		cf := Conflict{ElementID: c.ElementID, Kind: kind, Name: p.ElementName(c.ElementID), Heads: []ConflictHead{}}
		for _, id := range c.Heads {
			d := p.Decisions[id]
			h := ConflictHead{ID: id, Title: d.Title, Time: stamp(d.Time), Day: day(d.Time), Actor: d.Actor,
				Rationale: d.Rationale, Mutations: []Mutation{}}
			for _, mu := range d.Mutations {
				h.Mutations = append(h.Mutations, mutation(p, mu))
			}
			cf.Heads = append(cf.Heads, h)
		}
		out = append(out, cf)
	}
	return out
}

// ---- people ---------------------------------------------------------------------------

// PersonRow is one person in the comparison table.
type PersonRow struct {
	Name       string `json:"name"`
	Decisions  int    `json:"decisions"`
	Heads      int    `json:"heads"`
	Superseded int    `json:"superseded"`
	Notes      int    `json:"notes"`
	Elements   int    `json:"elements"`
	Last       string `json:"last"`
	Last30     int    `json:"last30"`
}

// groupBy reads the axis: "author" credits the author field, anything else the actor.
func groupBy(by string) replay.GroupBy {
	if by == "author" {
		return replay.ByAuthor
	}
	return replay.ByActor
}

func personRow(p replay.Person) PersonRow {
	name := p.Name
	if name == "" {
		name = "(none)"
	}
	return PersonRow{Name: name, Decisions: p.Decisions, Heads: p.Heads, Superseded: p.Superseded, Notes: p.ProvenanceOnly,
		Elements: p.Elements, Last: day(p.Last), Last30: p.Last30}
}

// PersonRows tallies the log per person, by "actor" (who recorded) or "author" (who is credited).
func (m *Model) PersonRows(by string) []PersonRow {
	out := []PersonRow{}
	for _, p := range m.People(groupBy(by)) {
		out = append(out, personRow(p))
	}
	return out
}

// Recent is one of a person's latest decisions with the elements it touched.
type Recent struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Day      string   `json:"day"`
	Elements []string `json:"elements"`
}

// Person is one person in full.
type Person struct {
	PersonRow
	Share        float64        `json:"share"` // percent of the log
	First        string         `json:"first"`
	ActiveDays   int            `json:"activeDays"`
	Last90       int            `json:"last90"`
	Conflicts    int            `json:"conflicts"`
	ByMonth      []Count        `json:"byMonth"`
	Kinds        []Count        `json:"kinds"`
	Changes      []Count        `json:"changes"` // kinds of change, labelled
	Shards       []Count        `json:"installs"`
	Overrode     []Count        `json:"overrode"`
	OverriddenBy []Count        `json:"overriddenBy"`
	CreditedAs   []Count        `json:"creditedAs"`
	MostShaped   []ElementCount `json:"mostShaped"`
	Recent       []Recent       `json:"recent"`
}

// Person returns one person's footprint; false when nobody of that name recorded (or
// is credited with) a decision. The name "" (or "(none)") is the empty author.
func (m *Model) Person(name, by string) (*Person, bool) {
	if name == "(none)" {
		name = ""
	}
	for _, p := range m.People(groupBy(by)) {
		if p.Name != name {
			continue
		}
		share := 0.0
		if m.Stats.Decisions > 0 {
			share = 100 * float64(p.Decisions) / float64(m.Stats.Decisions)
		}
		out := &Person{PersonRow: personRow(p), Share: share, First: day(p.First), ActiveDays: p.ActiveDays, Last90: p.Last90,
			Conflicts: p.Conflicts, ByMonth: counts(p.ByMonth), Kinds: counts(p.Kinds), Changes: []Count{}, Shards: counts(p.Shards),
			Overrode: counts(p.Overrode), OverriddenBy: counts(p.OverriddenBy), CreditedAs: counts(p.CreditedAs),
			MostShaped: elementCounts(p.MostShaped), Recent: []Recent{}}
		for _, c := range p.Mutations {
			out.Changes = append(out.Changes, Count{Key: mutationLabel(event.MutOp(c.Key)), N: c.N})
		}
		for _, d := range p.Recent {
			r := Recent{ID: d.ID, Title: d.Title, Day: day(d.Time), Elements: []string{}}
			for _, el := range m.Proj.ElementsOf(d.ID) {
				r.Elements = append(r.Elements, m.Proj.ElementName(el))
			}
			out.Recent = append(out.Recent, r)
		}
		return out, true
	}
	return nil, false
}

// ---- filters --------------------------------------------------------------------------

// Filters are the values the decision filter can take, for pickers.
type Filters struct {
	Actors   []Count  `json:"actors"`
	Authors  []Count  `json:"authors"`
	Kinds    []Count  `json:"kinds"`
	Installs []Count  `json:"installs"`
	States   []string `json:"states"`
}

// FilterValues lists what the log contains.
func (m *Model) FilterValues() Filters {
	f := Filters{Actors: counts(m.Stats.DecisionsByActor), Authors: counts(m.Stats.DecisionsByAuthor),
		Kinds: counts(m.Stats.ElementsByKind), Installs: []Count{}, States: []string{"head", "superseded", "note"}}
	for _, sh := range m.Stats.Shards {
		f.Installs = append(f.Installs, Count{Key: sh.ID, N: sh.Decisions})
	}
	return f
}

// ---- graph ----------------------------------------------------------------------------

// GraphNode is one element as the graph view draws it.
type GraphNode struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Decisions int    `json:"decisions"` // how many decisions shaped it: its size
	Heads     int    `json:"heads"`     // more than one: contested
}

// GraphLink is one live link between two elements.
type GraphLink struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

// GraphDecision is one decision with what it touched, for the graph's decision layer.
type GraphDecision struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Day        string   `json:"day"`
	Actor      string   `json:"actor"`
	State      string   `json:"state"`      // head | superseded | note
	Elements   []string `json:"elements"`   // every element it shaped
	Governs    []string `json:"governs"`    // the ones it took authority over
	Supersedes []string `json:"supersedes"` // the decisions it replaced, when they are in this log
}

// Graph is the live graph and the decisions behind it: what the graph view draws.
type Graph struct {
	Nodes     []GraphNode     `json:"nodes"`
	Links     []GraphLink     `json:"links"`
	Decisions []GraphDecision `json:"decisions"`
	Kinds     []Count         `json:"kinds"` // elements per kind, largest first
}

// Graph returns every element, every live link between two known elements, and every
// decision with the elements it shaped. Elements come kind, name, id (the sidebar's
// order); links in key order; decisions newest first. Lists are never null.
func (m *Model) Graph() Graph {
	p := m.Proj
	g := Graph{Nodes: []GraphNode{}, Links: []GraphLink{}, Decisions: []GraphDecision{}, Kinds: []Count{}}
	byKind := map[string]int{}
	for _, e := range m.Elements() {
		kind := kindOf(e)
		byKind[kind]++
		g.Nodes = append(g.Nodes, GraphNode{ID: e.ID, Kind: kind, Name: p.ElementName(e.ID),
			Decisions: len(p.ShapedBy(e.ID)), Heads: len(p.Heads(e.ID))})
	}
	for _, k := range replay.SortedKeys(byKind) {
		g.Kinds = append(g.Kinds, Count{Key: k, N: byKind[k]})
	}
	sort.SliceStable(g.Kinds, func(i, j int) bool { return g.Kinds[i].N > g.Kinds[j].N })
	for _, k := range replay.SortedKeys(p.Links) {
		l := p.Links[k]
		if _, ok := p.Elements[l.From]; !ok {
			continue
		}
		if _, ok := p.Elements[l.To]; !ok {
			continue
		}
		g.Links = append(g.Links, GraphLink{From: l.From, To: l.To, Kind: l.Kind})
	}
	shaped := map[string][]replay.Shape{}
	for _, k := range replay.SortedKeys(p.Shapes) {
		s := p.Shapes[k]
		shaped[s.Decision] = append(shaped[s.Decision], s)
	}
	sup := map[string][]string{}
	for _, k := range replay.SortedKeys(p.Supersedes) {
		s := p.Supersedes[k]
		if _, ok := p.Decisions[s.To]; ok {
			sup[s.From] = append(sup[s.From], s.To)
		}
	}
	ds, _ := m.Decisions(DecisionFilter{})
	for _, d := range ds {
		gd := GraphDecision{ID: d.ID, Title: d.Title, Day: day(d.Time), Actor: d.Actor, State: m.State(d.ID),
			Elements: []string{}, Governs: []string{}, Supersedes: []string{}}
		for _, s := range shaped[d.ID] {
			if _, ok := p.Elements[s.Element]; !ok {
				continue
			}
			gd.Elements = append(gd.Elements, s.Element)
			if s.Authority {
				gd.Governs = append(gd.Governs, s.Element)
			}
		}
		if v := sup[d.ID]; v != nil {
			gd.Supersedes = v
		}
		g.Decisions = append(g.Decisions, gd)
	}
	return g
}
