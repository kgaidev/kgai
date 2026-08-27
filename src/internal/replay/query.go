package replay

import "sort"

// ---- derived indexes (built lazily, deterministic) ------------------------------

func (p *Projection) index() {
	if p.supBy != nil {
		return
	}
	p.supBy = map[string][]string{}
	for _, k := range SortedKeys(p.Supersedes) {
		s := p.Supersedes[k]
		p.supBy[s.To] = append(p.supBy[s.To], s.From)
	}
	p.shapedBy = map[string][]string{}
	p.decisionEl = map[string][]string{}
	for _, k := range SortedKeys(p.Shapes) {
		s := p.Shapes[k]
		p.shapedBy[s.Element] = append(p.shapedBy[s.Element], s.Decision)
		p.decisionEl[s.Decision] = append(p.decisionEl[s.Decision], s.Element)
	}
}

// SupersededBy lists the decisions that supersede dec (ids ascending).
func (p *Projection) SupersededBy(dec string) []string {
	p.index()
	return p.supBy[dec]
}

// ShapedBy lists the decisions that shaped an element, ids ascending (provenance —
// authority or not). History orders them on the timeline.
func (p *Projection) ShapedBy(el string) []string {
	p.index()
	return p.shapedBy[el]
}

// ElementsOf lists the elements a decision shaped, ids ascending.
func (p *Projection) ElementsOf(dec string) []string {
	p.index()
	return p.decisionEl[dec]
}

// ---- heads and conflicts ------------------------------------------------------------

// IsHead reports whether dec currently governs el: it shapes el with authority and no
// decision that supersedes it shapes el with authority — the graph's definition, from
// engine.headDecisions.
func (p *Projection) IsHead(dec, el string) bool {
	s, ok := p.Shapes[ShapeKey(dec, el)]
	if !ok || !s.Authority {
		return false
	}
	for _, d2 := range p.SupersededBy(dec) {
		if s2, ok := p.Shapes[ShapeKey(d2, el)]; ok && s2.Authority {
			return false
		}
	}
	return true
}

// Heads returns the head decision(s) of an element, newest lamport first, ids
// ascending on a tie — the order `kg conflicts` reports the sides of a branch.
func (p *Projection) Heads(el string) []string {
	var out []string
	for _, dec := range p.ShapedBy(el) {
		if p.IsHead(dec, el) {
			out = append(out, dec)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := p.Decisions[out[i]], p.Decisions[out[j]]
		if a.Lamport != b.Lamport {
			return a.Lamport > b.Lamport
		}
		return out[i] < out[j]
	})
	return out
}

// HeadAnywhere reports whether a decision is still the head of at least one element.
func (p *Projection) HeadAnywhere(dec string) bool {
	for _, el := range p.ElementsOf(dec) {
		if p.IsHead(dec, el) {
			return true
		}
	}
	return false
}

// HasAuthority reports whether a decision governs at least one element (as opposed to
// a provenance-only note or dead end, which supersedes nothing and cannot conflict).
func (p *Projection) HasAuthority(dec string) bool {
	for _, el := range p.ElementsOf(dec) {
		if p.Shapes[ShapeKey(dec, el)].Authority {
			return true
		}
	}
	return false
}

// Conflict is an element with two or more competing head decisions — a branch.
type Conflict struct {
	ElementID, Name, Kind string
	Heads                 []string // newest lamport first
	Titles                []string
}

// Conflicts lists the contested elements, element id ascending, exactly as
// `kg conflicts` does.
func (p *Projection) Conflicts() []Conflict {
	var out []Conflict
	for _, el := range SortedKeys(p.Elements) {
		heads := p.Heads(el)
		if len(heads) < 2 {
			continue
		}
		e := p.Elements[el]
		c := Conflict{ElementID: el, Name: e.Name, Kind: e.Kind, Heads: heads}
		for _, h := range heads {
			c.Titles = append(c.Titles, p.Decisions[h].Title)
		}
		out = append(out, c)
	}
	return out
}

// ---- history ------------------------------------------------------------------------

// HistoryEntry is one decision on an element's timeline.
type HistoryEntry struct {
	Decision *Decision
	IsHead   bool
}

// History is the chain of decisions that shaped an element, oldest first. Ordered by
// recorded time (lamport, then id, break ties): a back-dated import — an old ADR given
// its real date — appears where it belongs on the timeline, as in `kg history`.
func (p *Projection) History(el string) []HistoryEntry {
	var out []HistoryEntry
	for _, dec := range p.ShapedBy(el) {
		d := p.Decisions[dec]
		out = append(out, HistoryEntry{Decision: d, IsHead: p.IsHead(dec, el)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Decision, out[j].Decision
		if a.RecordedAt != b.RecordedAt {
			return a.RecordedAt < b.RecordedAt
		}
		if a.Lamport != b.Lamport {
			return a.Lamport < b.Lamport
		}
		return a.ID < b.ID
	})
	return out
}

// LinksOf returns the current links out of and into an element, deterministically
// ordered (kind, other end).
func (p *Projection) LinksOf(el string) (out, in []Link) {
	for _, k := range SortedKeys(p.Links) {
		l := p.Links[k]
		if l.From == el {
			out = append(out, l)
		}
		if l.To == el {
			in = append(in, l)
		}
	}
	byKind := func(ls []Link, other func(Link) string) {
		sort.SliceStable(ls, func(i, j int) bool {
			if ls[i].Kind != ls[j].Kind {
				return ls[i].Kind < ls[j].Kind
			}
			return p.elementName(other(ls[i])) < p.elementName(other(ls[j]))
		})
	}
	byKind(out, func(l Link) string { return l.To })
	byKind(in, func(l Link) string { return l.From })
	return out, in
}

func (p *Projection) elementName(id string) string {
	if e, ok := p.Elements[id]; ok && e.Name != "" {
		return e.Name
	}
	return id
}

// ElementName is the display name of an element (its id when unknown or a stub).
func (p *Projection) ElementName(id string) string { return p.elementName(id) }
