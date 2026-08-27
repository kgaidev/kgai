package replay

import (
	"fmt"
	"testing"
	"time"

	"kgai/internal/event"
)

// ev builds one sealed event. Hashes are content hashes, so two identical decisions
// are one event — exactly the idempotency the projection has to honour.
func ev(actor, install string, lamport int64, at string, d event.Decision) event.Event {
	if d.Author == "" {
		d.Author = actor
	}
	if d.ID == "" {
		d.ID = event.DecisionID(d)
	}
	e := event.Event{Op: event.OpAssert, Actor: actor, InstallID: install, Lamport: lamport,
		RecordedAt: at, Decision: &d}
	e.Hash = e.ComputeHash()
	return e
}

func upsert(kind, name string, props map[string]string) event.Mutation {
	return event.Mutation{Op: event.MutUpsertElement, ElementID: event.ElementID(kind, name), Kind: kind, Name: name, Props: props}
}
func setProp(kind, name, k, v string) event.Mutation {
	return event.Mutation{Op: event.MutSetProp, ElementID: event.ElementID(kind, name), Key: k, Value: v}
}
func link(op event.MutOp, fk, fn, kind, tk, tn string) event.Mutation {
	return event.Mutation{Op: op, FromID: event.ElementID(fk, fn), ToID: event.ElementID(tk, tn), LinkKind: kind, ElementID: event.ElementID(fk, fn)}
}

var invoice, pricing = event.ElementID("feature", "Invoice"), event.ElementID("feature", "Pricing")

func TestApplyMirrorsMergeSemantics(t *testing.T) {
	p := New()
	e1 := ev("alice", "i1", 1, "2026-01-01T10:00:00Z", event.Decision{Title: "Invoice exists",
		Mutations: []event.Mutation{upsert("feature", "Invoice", map[string]string{"paths": "src/inv/*", "display": "list"})},
		Shapes:    []string{invoice}, Targets: []string{invoice}})
	p.Apply(e1)
	p.Apply(e1) // applied once
	if len(p.AppliedOrder) != 1 || len(p.Decisions) != 1 {
		t.Fatalf("event applied twice: %d applied, %d decisions", len(p.AppliedOrder), len(p.Decisions))
	}
	if got := p.Elements[invoice].Props; got != "display=list\npaths=src/inv/*" {
		t.Fatalf("props blob not sorted by key: %q", got)
	}

	// ON CREATE: a later upsert never renames; set_prop replaces one key and keeps the rest.
	p.Apply(ev("bob", "i2", 2, "2026-01-02T10:00:00Z", event.Decision{Title: "rename attempt",
		Mutations: []event.Mutation{{Op: event.MutUpsertElement, ElementID: invoice, Kind: "component", Name: "Bill"},
			setProp("feature", "Invoice", "display", "standalone")},
		Shapes: []string{invoice}, Targets: []string{invoice}}))
	e := p.Elements[invoice]
	if e.Kind != "feature" || e.Name != "Invoice" {
		t.Fatalf("upsert overwrote kind/name: %s:%s", e.Kind, e.Name)
	}
	if e.Props != "display=standalone\npaths=src/inv/*" {
		t.Fatalf("set_prop wrong: %q", e.Props)
	}
	if p.SetProps != 1 {
		t.Fatalf("set_prop counter = %d", p.SetProps)
	}

	// Links: add is idempotent, retire deletes, both ends are ensured as stubs.
	p.Apply(ev("alice", "i1", 3, "2026-01-03T10:00:00Z", event.Decision{Title: "part of pricing",
		Mutations: []event.Mutation{link(event.MutAddLink, "feature", "Invoice", "PART_OF", "feature", "Pricing"),
			link(event.MutAddLink, "feature", "Invoice", "PART_OF", "feature", "Pricing")},
		Shapes: []string{invoice, pricing}, Targets: []string{invoice}}))
	if len(p.Links) != 1 || p.Elements[pricing] == nil || p.Elements[pricing].Name != "" {
		t.Fatalf("add_link: %d links, pricing=%+v", len(p.Links), p.Elements[pricing])
	}
	p.Apply(ev("alice", "i1", 4, "2026-01-04T10:00:00Z", event.Decision{Title: "standalone",
		Mutations: []event.Mutation{link(event.MutRetireLink, "feature", "Invoice", "PART_OF", "feature", "Pricing")},
		Shapes:    []string{invoice, pricing}, Targets: []string{invoice}}))
	if len(p.Links) != 0 || p.RetiredLinks != 1 {
		t.Fatalf("retire_link: %d links, retired=%d", len(p.Links), p.RetiredLinks)
	}
	// Authority follows Targets: Pricing was only the `to` side → provenance only.
	if !p.Shapes[ShapeKey(p.AppliedOrder[2][:0]+lastDecision(p, 3), invoice)].Authority {
		t.Fatalf("from side must have authority")
	}
	if p.Shapes[ShapeKey(lastDecision(p, 3), pricing)].Authority {
		t.Fatalf("to side must be provenance only")
	}
}

// lastDecision finds the decision id recorded at a lamport (tests only).
func lastDecision(p *Projection, lamport int64) string {
	for id, d := range p.Decisions {
		if d.Lamport == lamport {
			return id
		}
	}
	return ""
}

func TestBuildReplaysInCanonicalOrder(t *testing.T) {
	a := ev("alice", "i1", 1, "2026-01-01T10:00:00Z", event.Decision{Title: "v1",
		Mutations: []event.Mutation{setProp("feature", "Invoice", "display", "list")}, Shapes: []string{invoice}, Targets: []string{invoice}})
	b := ev("bob", "i2", 5, "2026-01-02T10:00:00Z", event.Decision{Title: "v2",
		Mutations: []event.Mutation{setProp("feature", "Invoice", "display", "standalone")}, Shapes: []string{invoice}, Targets: []string{invoice}})
	// Given newest first, Build must still let lamport 5 win.
	p := Build([]event.Event{b, a})
	if got := p.Elements[invoice].Props; got != "display=standalone" {
		t.Fatalf("canonical order not applied: %q", got)
	}
	if p.AppliedOrder[0] != a.Hash {
		t.Fatalf("applied order must be (lamport, hash)")
	}
}

func TestHeadsConflictsAndResolution(t *testing.T) {
	base := ev("alice", "i1", 1, "2026-01-01T10:00:00Z", event.Decision{Title: "Invoice exists",
		Mutations: []event.Mutation{upsert("feature", "Invoice", nil)}, Shapes: []string{invoice}, Targets: []string{invoice}})
	// Alice and Bob change Invoice concurrently, both superseding the base.
	al := ev("alice", "i1", 2, "2026-01-02T10:00:00Z", event.Decision{Title: "hidden when draft",
		Mutations: []event.Mutation{setProp("feature", "Invoice", "drafts", "hidden")},
		Shapes:    []string{invoice}, Targets: []string{invoice}, Supersedes: []string{base.Decision.ID}})
	bo := ev("bob", "i2", 2, "2026-01-02T11:00:00Z", event.Decision{Title: "drafts visible",
		Mutations: []event.Mutation{setProp("feature", "Invoice", "drafts", "visible")},
		Shapes:    []string{invoice}, Targets: []string{invoice}, Supersedes: []string{base.Decision.ID}})
	// A note on Invoice: provenance only, never a head, never a conflict side.
	note := ev("carol", "i3", 3, "2026-01-03T10:00:00Z", event.Decision{Title: "considered and rejected: PDF-only",
		Mutations: []event.Mutation{upsert("feature", "Invoice", nil)}, Shapes: []string{invoice}, ProvenanceOnly: true})

	p := Build([]event.Event{base, al, bo, note})
	heads := p.Heads(invoice)
	if len(heads) != 2 {
		t.Fatalf("expected 2 heads, got %v", heads)
	}
	// Same lamport → ids ascending, the order kg conflicts reports.
	if heads[0] > heads[1] {
		t.Fatalf("heads not ordered by id on a lamport tie: %v", heads)
	}
	cs := p.Conflicts()
	if len(cs) != 1 || cs[0].ElementID != invoice || len(cs[0].Titles) != 2 {
		t.Fatalf("conflicts = %+v", cs)
	}
	if p.IsHead(note.Decision.ID, invoice) || p.HasAuthority(note.Decision.ID) {
		t.Fatalf("a provenance-only decision must not be a head")
	}
	if !p.IsHead(base.Decision.ID, invoice) == false {
		t.Fatalf("superseded base must not be a head")
	}

	// One decision taking authority again supersedes both heads → resolved.
	res := ev("alice", "i1", 4, "2026-01-04T10:00:00Z", event.Decision{Title: "drafts visible to owners",
		Mutations: []event.Mutation{setProp("feature", "Invoice", "drafts", "owners")},
		Shapes:    []string{invoice}, Targets: []string{invoice}, Supersedes: []string{al.Decision.ID, bo.Decision.ID}})
	p.Apply(res)
	if hs := p.Heads(invoice); len(hs) != 1 || hs[0] != res.Decision.ID {
		t.Fatalf("resolution must leave one head: %v", hs)
	}
	if len(p.Conflicts()) != 0 {
		t.Fatalf("conflict must be gone after resolution")
	}
	if got := p.SupersededBy(al.Decision.ID); len(got) != 1 || got[0] != res.Decision.ID {
		t.Fatalf("SupersededBy = %v", got)
	}
}

func TestHistoryFollowsRecordedTimeNotLamport(t *testing.T) {
	newer := ev("alice", "i1", 1, "2026-06-01T10:00:00Z", event.Decision{Title: "today",
		Mutations: []event.Mutation{setProp("feature", "Invoice", "a", "1")}, Shapes: []string{invoice}, Targets: []string{invoice}})
	// An imported ADR: recorded later (higher lamport) but dated years back.
	old := ev("alice", "i1", 2, "2020-01-01", event.Decision{Title: "ADR-1 from 2020",
		Mutations: []event.Mutation{upsert("feature", "Invoice", nil)}, Shapes: []string{invoice}, ProvenanceOnly: true})
	p := Build([]event.Event{newer, old})
	h := p.History(invoice)
	if len(h) != 2 || h[0].Decision.Title != "ADR-1 from 2020" || h[1].Decision.Title != "today" {
		t.Fatalf("history order wrong: %+v", h)
	}
	if !h[1].IsHead || h[0].IsHead {
		t.Fatalf("head flags wrong: %+v", h)
	}
	if h[0].Decision.RecordedAt != "2020-01-01 00:00:00" {
		t.Fatalf("date-only import must render as the graph stores it: %q", h[0].Decision.RecordedAt)
	}
}

func TestStubDecisionStaysEmpty(t *testing.T) {
	// A decision that supersedes one whose event never arrived (partial log): the
	// target exists as a stub so the SUPERSEDES edge has somewhere to attach.
	d := ev("alice", "i1", 7, "2026-01-01T10:00:00Z", event.Decision{Title: "later",
		Mutations: []event.Mutation{setProp("feature", "Invoice", "a", "1")}, Shapes: []string{invoice},
		Targets: []string{invoice}, Supersedes: []string{"d_missing"}})
	p := Build([]event.Event{d})
	if s := p.Decisions["d_missing"]; s == nil || !s.Stub || s.Title != "" {
		t.Fatalf("stub = %+v", s)
	}
	st := p.Stats()
	if st.Decisions != 1 || st.Stubs != 1 {
		t.Fatalf("stats must not count the stub as a decision: %+v", st)
	}
}

func TestStatsAndPeople(t *testing.T) {
	var evs []event.Event
	at := func(day int) string { return fmt.Sprintf("2026-03-%02dT12:00:00Z", day) }
	evs = append(evs, ev("alice", "i1", 1, at(1), event.Decision{Title: "invoice",
		Mutations: []event.Mutation{upsert("feature", "Invoice", nil), upsert("feature", "Pricing", nil), link(event.MutAddLink, "feature", "Invoice", "PART_OF", "feature", "Pricing")},
		Shapes:    []string{invoice, pricing}, Targets: []string{invoice}}))
	evs = append(evs, ev("bob", "i2", 2, at(2), event.Decision{Title: "pricing", Author: "bob + Claude",
		Mutations: []event.Mutation{upsert("feature", "Pricing", map[string]string{"x": "1"})},
		Shapes:    []string{pricing}, Targets: []string{pricing}}))
	first := evs[0].Decision.ID
	evs = append(evs, ev("bob", "i2", 3, at(10), event.Decision{Title: "invoice standalone",
		Mutations: []event.Mutation{link(event.MutRetireLink, "feature", "Invoice", "PART_OF", "feature", "Pricing")},
		Shapes:    []string{invoice, pricing}, Targets: []string{invoice}, Supersedes: []string{first}}))
	p := Build(evs)

	st := p.Stats()
	if st.Decisions != 3 || st.Elements != 2 || st.Links != 0 || st.RetiredLinks != 1 {
		t.Fatalf("stats = %+v", st)
	}
	if st.DecisionsByActor[0].Key != "bob" || st.DecisionsByActor[0].N != 2 {
		t.Fatalf("by actor = %+v", st.DecisionsByActor)
	}
	if len(st.DecisionsByAuthor) != 3 { // alice, bob, "bob + Claude"
		t.Fatalf("by author = %+v", st.DecisionsByAuthor)
	}
	if st.DecisionsByMonth[0].Key != "2026-03" || st.DecisionsByMonth[0].N != 3 {
		t.Fatalf("by month = %+v", st.DecisionsByMonth)
	}
	if len(st.Shards) != 2 || st.Shards[1].ID != "i2" || st.Shards[1].MinLamport != 2 || st.Shards[1].MaxLamport != 3 {
		t.Fatalf("shards = %+v", st.Shards)
	}

	now := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	people := p.People(ByActor, now, 10)
	if len(people) != 2 || people[0].Name != "bob" || people[1].Name != "alice" {
		t.Fatalf("people order = %+v", people)
	}
	bob, alice := people[0], people[1]
	if bob.Decisions != 2 || bob.Heads != 2 || alice.Superseded != 1 || alice.Heads != 0 {
		t.Fatalf("head/superseded counts: bob=%+v alice=%+v", bob, alice)
	}
	if len(bob.Overrode) != 1 || bob.Overrode[0].Key != "alice" || len(alice.OverriddenBy) != 1 || alice.OverriddenBy[0].Key != "bob" {
		t.Fatalf("supersession matrix: bob.Overrode=%v alice.OverriddenBy=%v", bob.Overrode, alice.OverriddenBy)
	}
	if len(bob.CreditedAs) != 2 { // "bob" and "bob + Claude"
		t.Fatalf("credited as = %v", bob.CreditedAs)
	}
	if bob.Recent[0].Title != "invoice standalone" || bob.Last30 != 2 || bob.ActiveDays != 2 {
		t.Fatalf("recent/recency wrong: %+v", bob)
	}
	if alice.Elements != 2 || alice.Kinds[0].Key != "feature" {
		t.Fatalf("elements/kinds wrong: %+v", alice)
	}
	byAuthor := p.People(ByAuthor, now, 1)
	if len(byAuthor) != 3 || len(byAuthor[0].Recent) != 1 {
		t.Fatalf("by author = %+v", byAuthor)
	}
}

func TestSetPropNeutralizesSeparators(t *testing.T) {
	got := SetProp("", "a=b\nc", "x\ny")
	if got != "a-b c=x y" {
		t.Fatalf("SetProp = %q", got)
	}
	if m := ParseProps("b=2\na=1"); m["a"] != "1" || m["b"] != "2" || ParseProps("") != nil {
		t.Fatalf("ParseProps = %v", m)
	}
}

// A link's far end that no decision ever upserted stays a nameless element: the graph
// MERGEs it by id (ON CREATE sets nothing), and a later upsert never renames. The app
// must show such an element by id rather than invent a name.
func TestBareLinkTargetStaysNameless(t *testing.T) {
	p := Build([]event.Event{ev("alice", "i1", 1, "2026-01-01T10:00:00Z", event.Decision{Title: "link only",
		Mutations: []event.Mutation{upsert("feature", "Invoice", nil), link(event.MutAddLink, "feature", "Invoice", "PART_OF", "feature", "Pricing")},
		Shapes:    []string{invoice, pricing}, Targets: []string{invoice}})})
	p.Apply(ev("bob", "i2", 2, "2026-01-02T10:00:00Z", event.Decision{Title: "late upsert",
		Mutations: []event.Mutation{upsert("feature", "Pricing", nil)}, Shapes: []string{pricing}, ProvenanceOnly: true}))
	if e := p.Elements[pricing]; e.Kind != "" || e.Name != "" {
		t.Fatalf("ON CREATE semantics broken: %+v", e)
	}
	if p.ElementName(pricing) != pricing {
		t.Fatalf("nameless element must display as its id")
	}
}
