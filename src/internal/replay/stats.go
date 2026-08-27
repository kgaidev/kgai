package replay

import (
	"sort"
	"time"
)

// Count is one row of a tally, sortable through SortCounts.
type Count struct {
	Key string
	N   int
}

// ElementCount tallies something per element, keeping id, name and kind for display.
type ElementCount struct {
	ID, Name, Kind string
	N              int
}

// SortCounts orders a tally by count descending, key ascending — the fixed order every
// list in the app uses, so a store always reads the same way.
func SortCounts(m map[string]int) []Count {
	out := make([]Count, 0, len(m))
	for k, n := range m {
		out = append(out, Count{k, n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func sortElementCounts(m map[string]int, p *Projection) []ElementCount {
	out := make([]ElementCount, 0, len(m))
	for id, n := range m {
		e := p.Elements[id]
		out = append(out, ElementCount{ID: id, Name: e.Name, Kind: e.Kind, N: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Shard is one install's slice of the log as seen from the projection.
type Shard struct {
	ID                     string
	Decisions              int
	MinLamport, MaxLamport int64
	Last                   time.Time
	Actors                 []Count
}

// Stats is the store at a glance — what `kg status` reports and the tallies behind it.
type Stats struct {
	Events, Elements, Decisions, Stubs, Links, Conflicts int
	RetiredLinks, SetProps, ProvenanceOnly               int
	First, Last                                          time.Time

	ElementsByKind, LinksByKind []Count
	DecisionsByMonth            []Count // "2006-01", ascending by month
	DecisionsByAuthor           []Count
	DecisionsByActor            []Count
	MostShaped                  []ElementCount
	Shards                      []Shard
}

// Month is the tally key for a time ("2006-01"); "" for the zero time.
func Month(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01")
}

// Stats tallies the projection.
func (p *Projection) Stats() Stats {
	st := Stats{Events: len(p.AppliedOrder), Elements: len(p.Elements), Links: len(p.Links),
		RetiredLinks: p.RetiredLinks, SetProps: p.SetProps}
	kinds, lkinds, months, authors, actors := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	shaped := map[string]int{}
	shards := map[string]*Shard{}
	shardActors := map[string]map[string]int{}
	for _, e := range p.Elements {
		kinds[e.Kind]++
	}
	for _, l := range p.Links {
		lkinds[l.Kind]++
	}
	for _, id := range SortedKeys(p.Decisions) {
		d := p.Decisions[id]
		if d.Stub {
			st.Stubs++
			continue
		}
		st.Decisions++
		if !p.HasAuthority(id) {
			st.ProvenanceOnly++
		}
		if m := Month(d.Time); m != "" {
			months[m]++
		}
		authors[d.Author]++
		actors[d.Actor]++
		if !d.Time.IsZero() {
			if st.First.IsZero() || d.Time.Before(st.First) {
				st.First = d.Time
			}
			if d.Time.After(st.Last) {
				st.Last = d.Time
			}
		}
		sh := shards[d.InstallID]
		if sh == nil {
			sh = &Shard{ID: d.InstallID, MinLamport: d.Lamport, MaxLamport: d.Lamport}
			shards[d.InstallID] = sh
			shardActors[d.InstallID] = map[string]int{}
		}
		sh.Decisions++
		if d.Lamport < sh.MinLamport {
			sh.MinLamport = d.Lamport
		}
		if d.Lamport > sh.MaxLamport {
			sh.MaxLamport = d.Lamport
		}
		if d.Time.After(sh.Last) {
			sh.Last = d.Time
		}
		shardActors[d.InstallID][d.Actor]++
	}
	for _, s := range p.Shapes {
		shaped[s.Element]++
	}
	st.Conflicts = len(p.Conflicts())
	st.ElementsByKind, st.LinksByKind = SortCounts(kinds), SortCounts(lkinds)
	st.DecisionsByAuthor, st.DecisionsByActor = SortCounts(authors), SortCounts(actors)
	st.DecisionsByMonth = SortCounts(months)
	sort.Slice(st.DecisionsByMonth, func(i, j int) bool { return st.DecisionsByMonth[i].Key < st.DecisionsByMonth[j].Key })
	st.MostShaped = sortElementCounts(shaped, p)
	for _, id := range SortedKeys(shards) {
		sh := shards[id]
		sh.Actors = SortCounts(shardActors[id])
		st.Shards = append(st.Shards, *sh)
	}
	return st
}

// ---- people -------------------------------------------------------------------------

// GroupBy chooses whose name a decision counts under.
type GroupBy int

const (
	// ByActor groups by the install identity that recorded the event — kg.config.json's
	// actor, one value per person per machine, the clean axis.
	ByActor GroupBy = iota
	// ByAuthor groups by the decision's credited author, a free-form field that
	// defaults to the actor but may carry anything ("alex + Claude", imports).
	ByAuthor
)

// Person is one actor's (or author's) footprint in the log.
type Person struct {
	Name string
	// Decisions counts their decisions; of those, Heads still govern at least one
	// element, Superseded governed something once and no longer do, ProvenanceOnly
	// never governed anything (notes, dead ends).
	Decisions, Heads, Superseded, ProvenanceOnly int
	Elements                                     int // distinct elements they shaped
	First, Last                                  time.Time
	ActiveDays                                   int
	Last30, Last90                               int // decisions in the last 30/90 days before `now`

	ByMonth      []Count // ascending by month
	Kinds        []Count // element kinds they shaped
	Mutations    []Count // mutation ops they used
	Shards       []Count // installs they recorded from
	Overrode     []Count // whose decisions theirs superseded (including their own)
	OverriddenBy []Count // who superseded theirs
	CreditedAs   []Count // the other axis: authors credited (ByActor) or actors recording (ByAuthor)
	Conflicts    int     // contested elements where one of the heads is theirs
	MostShaped   []ElementCount
	Recent       []*Decision // newest first, at most the `recent` asked for
}

// People tallies the log per person. `now` anchors the recency windows; `recent` caps
// the per-person list of latest decisions. Sorted by decisions descending, name ascending.
func (p *Projection) People(by GroupBy, now time.Time, recent int) []Person {
	name := func(d *Decision) string {
		if by == ByAuthor {
			return d.Author
		}
		return d.Actor
	}
	other := func(d *Decision) string {
		if by == ByAuthor {
			return d.Actor
		}
		return d.Author
	}
	type acc struct {
		Person
		months, kinds, muts, shards, overrode, overridden, credited, shaped map[string]int
		days                                                                map[string]bool
		elements                                                            map[string]bool
		conflicts                                                           map[string]bool
	}
	people := map[string]*acc{}
	get := func(n string) *acc {
		a := people[n]
		if a == nil {
			a = &acc{Person: Person{Name: n}, months: map[string]int{}, kinds: map[string]int{}, muts: map[string]int{},
				shards: map[string]int{}, overrode: map[string]int{}, overridden: map[string]int{}, credited: map[string]int{},
				shaped: map[string]int{}, days: map[string]bool{}, elements: map[string]bool{}, conflicts: map[string]bool{}}
			people[n] = a
		}
		return a
	}
	d30, d90 := now.Add(-30*24*time.Hour), now.Add(-90*24*time.Hour)
	for _, id := range SortedKeys(p.Decisions) {
		d := p.Decisions[id]
		if d.Stub {
			continue
		}
		a := get(name(d))
		a.Person.Decisions++
		switch {
		case !p.HasAuthority(id):
			a.Person.ProvenanceOnly++
		case p.HeadAnywhere(id):
			a.Person.Heads++
		default:
			a.Person.Superseded++
		}
		if !d.Time.IsZero() {
			if a.Person.First.IsZero() || d.Time.Before(a.Person.First) {
				a.Person.First = d.Time
			}
			if d.Time.After(a.Person.Last) {
				a.Person.Last = d.Time
			}
			a.months[Month(d.Time)]++
			a.days[d.Time.UTC().Format("2006-01-02")] = true
			if !d.Time.Before(d30) && !d.Time.After(now) {
				a.Person.Last30++
			}
			if !d.Time.Before(d90) && !d.Time.After(now) {
				a.Person.Last90++
			}
		}
		for _, m := range d.Mutations {
			a.muts[string(m.Op)]++
		}
		a.shards[d.InstallID]++
		a.credited[other(d)]++
		for _, el := range p.ElementsOf(id) {
			a.elements[el] = true
			a.shaped[el]++
			a.kinds[p.Elements[el].Kind]++
		}
		for _, prev := range d.Supersedes {
			if pd, ok := p.Decisions[prev]; ok && !pd.Stub {
				a.overrode[name(pd)]++
				get(name(pd)).overridden[name(d)]++
			}
		}
		a.Person.Recent = append(a.Person.Recent, d)
	}
	for _, c := range p.Conflicts() {
		for _, h := range c.Heads {
			get(name(p.Decisions[h])).conflicts[c.ElementID] = true
		}
	}
	var out []Person
	for _, n := range SortedKeys(people) {
		a := people[n]
		a.Person.Elements = len(a.elements)
		a.Person.ActiveDays = len(a.days)
		a.Person.ByMonth = SortCounts(a.months)
		sort.Slice(a.Person.ByMonth, func(i, j int) bool { return a.Person.ByMonth[i].Key < a.Person.ByMonth[j].Key })
		a.Person.Kinds, a.Person.Mutations, a.Person.Shards = SortCounts(a.kinds), SortCounts(a.muts), SortCounts(a.shards)
		a.Person.Overrode, a.Person.OverriddenBy, a.Person.CreditedAs = SortCounts(a.overrode), SortCounts(a.overridden), SortCounts(a.credited)
		a.Person.Conflicts = len(a.conflicts)
		a.Person.MostShaped = sortElementCounts(a.shaped, p)
		sort.SliceStable(a.Person.Recent, func(i, j int) bool {
			x, y := a.Person.Recent[i], a.Person.Recent[j]
			if x.RecordedAt != y.RecordedAt {
				return x.RecordedAt > y.RecordedAt
			}
			if x.Lamport != y.Lamport {
				return x.Lamport > y.Lamport
			}
			return x.ID < y.ID
		})
		if recent >= 0 && len(a.Person.Recent) > recent {
			a.Person.Recent = a.Person.Recent[:recent]
		}
		out = append(out, a.Person)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Decisions != out[j].Decisions {
			return out[i].Decisions > out[j].Decisions
		}
		return out[i].Name < out[j].Name
	})
	return out
}
