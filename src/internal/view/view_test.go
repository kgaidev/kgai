package view

import (
	"path/filepath"
	"strings"
	"testing"

	"kgai/internal/view/viewtest"
)

func load(t *testing.T) (*Model, string) {
	t.Helper()
	viewtest.Isolate(t)
	dir := t.TempDir()
	viewtest.WriteStore(t, filepath.Join(dir, ".kgai", "store"))
	m := Load(dir)
	if m.Err != nil {
		t.Fatal(m.Err)
	}
	return m, dir
}

func titles(rows []Row) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Title)
	}
	return out
}

func TestStatusAndOverview(t *testing.T) {
	m, _ := load(t)
	s := m.Status()
	if s.Counts != (Counts{Decisions: 4, Elements: 2, Links: 1, Conflicts: 1, People: 2, Installs: 1}) {
		t.Fatalf("counts = %+v", s.Counts)
	}
	if s.Rule != "default location" || s.Source != "default" || s.Actor != "alice" || s.InstallID != "i-alice" || s.Error != "" {
		t.Fatalf("status = %+v", s)
	}
	o := m.Overview()
	if o.First != "2026-01-01" || o.Last != "2026-01-03" || o.Notes != 1 || o.Events != 4 {
		t.Fatalf("overview = %+v", o)
	}
	if len(o.Installs) != 1 || o.Installs[0].ID != "i-alice" || o.Installs[0].Decisions != 4 {
		t.Fatalf("installs = %+v", o.Installs)
	}
	if len(o.ElementsByKind) != 1 || o.ElementsByKind[0] != (Count{Key: "feature", N: 2}) {
		t.Fatalf("elements by kind = %+v", o.ElementsByKind)
	}
	if len(o.MostShaped) != 2 || o.MostShaped[0].Name != "Invoice" || o.MostShaped[0].N != 4 {
		t.Fatalf("most shaped = %+v", o.MostShaped)
	}
}

func TestDecisionRows(t *testing.T) {
	m, _ := load(t)
	all := m.DecisionRows(DecisionFilter{})
	if all.Total != 4 || all.Shown != 4 || all.Rows[0].Title != "Considered PDF-only invoices, rejected" || all.Rows[0].State != "note" {
		t.Fatalf("newest first: %+v", titles(all.Rows))
	}
	cases := []struct {
		name  string
		f     DecisionFilter
		want  []string
		regex bool
	}{
		{"text", DecisionFilter{Text: "draft"}, []string{"Draft invoices stay visible", "Invoices hidden when draft"}, false},
		{"note", DecisionFilter{State: "note"}, []string{"Considered PDF-only invoices, rejected"}, false},
		{"actor+head", DecisionFilter{Actor: "bob", State: "head"}, []string{"Draft invoices stay visible"}, false},
		{"superseded", DecisionFilter{State: "superseded"}, []string{"Invoice exists, part of Pricing"}, false},
		{"regex", DecisionFilter{Text: "^invoice", Regex: true}, []string{"Invoices hidden when draft", "Invoice exists, part of Pricing"}, true},
		{"dates", DecisionFilter{From: "2026-01-02", To: "2026-01-02"}, []string{"Draft invoices stay visible", "Invoices hidden when draft"}, false},
		{"element id", DecisionFilter{ElementID: viewtest.Pricing}, []string{"Invoice exists, part of Pricing"}, false},
		{"element name", DecisionFilter{Element: "pric"}, []string{"Invoice exists, part of Pricing"}, false},
		{"author", DecisionFilter{Author: "bob + Claude"}, []string{"Draft invoices stay visible"}, false},
		{"install", DecisionFilter{Shard: "i-alice", Kind: "feature"}, []string{"Considered PDF-only invoices, rejected", "Draft invoices stay visible", "Invoices hidden when draft", "Invoice exists, part of Pricing"}, false},
		{"oldest", DecisionFilter{Oldest: true}, []string{"Invoice exists, part of Pricing", "Invoices hidden when draft", "Draft invoices stay visible", "Considered PDF-only invoices, rejected"}, false},
	}
	for _, c := range cases {
		got := m.DecisionRows(c.f)
		if got.Error != "" || strings.Join(titles(got.Rows), "|") != strings.Join(c.want, "|") || got.Shown != len(c.want) {
			t.Errorf("%s: got %v (%q), want %v", c.name, titles(got.Rows), got.Error, c.want)
		}
	}
	if bad := m.DecisionRows(DecisionFilter{Text: "(", Regex: true}); bad.Error == "" || bad.Shown != 0 {
		t.Fatalf("bad regex: %+v", bad)
	}
	if !(DecisionFilter{State: "head"}).Active() || (DecisionFilter{Oldest: true}).Active() {
		t.Fatal("Active: state narrows, order does not")
	}
}

func TestDecisionDetail(t *testing.T) {
	m, _ := load(t)
	bob := m.DecisionRows(DecisionFilter{Actor: "bob", State: "head"}).Rows[0]
	d, ok := m.Decision(bob.ID)
	if !ok {
		t.Fatal("bob's decision not found")
	}
	if d.State != "head" || d.StateSentence != "still governs 1 element" || d.Author != "bob + Claude" {
		t.Fatalf("detail = %+v", d.Row)
	}
	if len(d.Refs) != 1 || d.Refs[0].System != "github" || !strings.HasPrefix(d.Refs[0].URL, "https://github.com/") {
		t.Fatalf("refs = %+v", d.Refs)
	}
	if len(d.Mutations) != 1 || d.Mutations[0].Label != "set property" || d.Mutations[0].Text != "feature: Invoice    drafts = visible" {
		t.Fatalf("mutations = %+v", d.Mutations)
	}
	if len(d.Elements) != 1 || d.Elements[0].Role != "governs" || d.Elements[0].Name != "Invoice" || d.Elements[0].Kind != "feature" {
		t.Fatalf("elements = %+v", d.Elements)
	}
	if len(d.Supersedes) != 1 || d.Supersedes[0].Title != "Invoice exists, part of Pricing" || !d.Supersedes[0].InLog {
		t.Fatalf("supersedes = %+v", d.Supersedes)
	}
	if d.InstallID != "i-alice" || d.Lamport != 2 || !strings.HasPrefix(d.EventHash, "sha256:") {
		t.Fatalf("technical = %s %d %s", d.InstallID, d.Lamport, d.EventHash)
	}

	base, _ := m.Decision(d.Supersedes[0].ID)
	if base.State != "superseded" || base.StateSentence != "replaced by 2 later decisions" || len(base.SupersededBy) != 2 {
		t.Fatalf("base = %+v", base)
	}
	if len(base.Mutations) != 3 || base.Mutations[0].Label != "defined element" || !strings.Contains(base.Mutations[0].Text, "paths = src/inv/*") ||
		base.Mutations[2].Label != "added link" || base.Mutations[2].Text != "feature: Invoice  -PART_OF->  feature: Pricing" {
		t.Fatalf("base mutations = %+v", base.Mutations)
	}
	roles := map[string]string{}
	for _, e := range base.Elements {
		roles[e.Name] = e.Role
	}
	if roles["Invoice"] != "superseded" || roles["Pricing"] != "mentioned" {
		t.Fatalf("roles = %v", roles)
	}

	note := m.DecisionRows(DecisionFilter{State: "note"}).Rows[0]
	n, _ := m.Decision(note.ID)
	if n.StateSentence != "context only, governs nothing" || n.Elements[0].Role != "mentioned" {
		t.Fatalf("note = %+v", n)
	}
	if _, ok := m.Decision("d_nothing"); ok {
		t.Fatal("unknown id found")
	}
}

func TestElements(t *testing.T) {
	m, _ := load(t)
	groups, shown := m.ElementGroups("", "")
	if shown != 2 || len(groups) != 1 || groups[0].Kind != "feature" || groups[0].Count != 2 {
		t.Fatalf("groups = %+v", groups)
	}
	inv, pr := groups[0].Elements[0], groups[0].Elements[1]
	if inv.Name != "Invoice" || inv.Decisions != 4 || !inv.Conflict || pr.Name != "Pricing" || pr.Decisions != 1 || pr.Conflict {
		t.Fatalf("rows = %+v %+v", inv, pr)
	}
	if _, shown := m.ElementGroups("", "pric"); shown != 1 {
		t.Fatalf("name filter: shown %d", shown)
	}
	if _, shown := m.ElementGroups("service", ""); shown != 0 {
		t.Fatalf("kind filter: shown %d", shown)
	}

	e, ok := m.Element(viewtest.Invoice)
	if !ok || !strings.HasPrefix(e.Standing, "Contested: 2") || e.Heads != 2 || e.ShapedBy != 4 {
		t.Fatalf("invoice = %+v", e)
	}
	// Two set_prop decisions wrote "drafts"; the canonical order picks one value, and
	// paths comes from the definition — sorted by key.
	if len(e.Props) != 2 || e.Props[0].Key != "drafts" || e.Props[1] != (KV{Key: "paths", Value: "src/inv/*"}) {
		t.Fatalf("props = %+v", e.Props)
	}
	if len(e.Links) != 1 || e.Links[0].Kind != "PART_OF" || e.Links[0].Direction != "out" || e.Links[0].Other.Name != "Pricing" {
		t.Fatalf("links = %+v", e.Links)
	}
	var hist []string
	for _, h := range e.History {
		hist = append(hist, h.Day+" "+h.State)
	}
	if strings.Join(hist, ",") != "2026-01-01 superseded,2026-01-02 head,2026-01-02 head,2026-01-03 note" {
		t.Fatalf("history = %v", hist)
	}
	p, _ := m.Element(viewtest.Pricing)
	if p.Standing != "Governed by no decision" || len(p.Links) != 1 || p.Links[0].Direction != "in" || p.Links[0].Other.Name != "Invoice" {
		t.Fatalf("pricing = %+v", p)
	}
	if _, ok := m.Element("el_nothing"); ok {
		t.Fatal("unknown element found")
	}
}

func TestConflicts(t *testing.T) {
	m, _ := load(t)
	cs := m.Conflicts()
	if len(cs) != 1 || cs[0].Name != "Invoice" || cs[0].Kind != "feature" || len(cs[0].Heads) != 2 {
		t.Fatalf("conflicts = %+v", cs)
	}
	seen := map[string]bool{}
	for _, h := range cs[0].Heads {
		seen[h.Title] = true
		if len(h.Mutations) != 1 || h.Mutations[0].Label != "set property" {
			t.Fatalf("head = %+v", h)
		}
	}
	if !seen["Invoices hidden when draft"] || !seen["Draft invoices stay visible"] {
		t.Fatalf("heads = %v", seen)
	}
}

func TestPeople(t *testing.T) {
	m, _ := load(t)
	rows := m.PersonRows("actor")
	if len(rows) != 2 || rows[0].Name != "alice" || rows[1].Name != "bob" {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0] != (PersonRow{Name: "alice", Decisions: 2, Heads: 1, Superseded: 1, Notes: 0, Elements: 2, Last: "2026-01-02"}) {
		t.Fatalf("alice = %+v", rows[0])
	}
	if rows[1].Heads != 1 || rows[1].Notes != 1 {
		t.Fatalf("bob = %+v", rows[1])
	}
	authors := m.PersonRows("author")
	if len(authors) != 3 {
		t.Fatalf("by author = %+v", authors)
	}
	bob, ok := m.Person("bob", "actor")
	if !ok || bob.Share != 50 || bob.Conflicts != 1 || len(bob.Recent) != 2 || bob.First != "2026-01-02" {
		t.Fatalf("bob = %+v", bob)
	}
	changes := map[string]int{}
	for _, c := range bob.Changes {
		changes[c.Key] = c.N
	}
	if changes["set property"] != 1 || changes["defined element"] != 1 {
		t.Fatalf("changes = %v", changes)
	}
	if len(bob.CreditedAs) != 2 { // bob, and "bob + Claude"
		t.Fatalf("credited as = %+v", bob.CreditedAs)
	}
	if bob.Recent[0].Title != "Considered PDF-only invoices, rejected" || bob.Recent[0].Elements[0] != "Invoice" {
		t.Fatalf("recent = %+v", bob.Recent)
	}
	if _, ok := m.Person("carol", "actor"); ok {
		t.Fatal("unknown person found")
	}
}

func TestFilterValues(t *testing.T) {
	m, _ := load(t)
	f := m.FilterValues()
	if len(f.Actors) != 2 || len(f.Authors) != 3 || len(f.Kinds) != 1 || len(f.Installs) != 1 || f.Installs[0].N != 4 || len(f.States) != 3 {
		t.Fatalf("filters = %+v", f)
	}
}

func TestEmptyFolder(t *testing.T) {
	viewtest.Isolate(t)
	m := Load(t.TempDir())
	if m.Err == nil {
		t.Fatal("an empty folder loaded")
	}
	s := m.Status()
	if s.Error == "" || !strings.Contains(s.Hint, "No knowledge graph here yet") || s.Counts.Decisions != 0 {
		t.Fatalf("status = %+v", s)
	}
	// Nothing to show, but nothing to panic on either.
	if m.DecisionRows(DecisionFilter{}).Shown != 0 || len(m.Conflicts()) != 0 || len(m.PersonRows("actor")) != 0 {
		t.Fatal("an empty model answered with rows")
	}
}

func TestChangedAfterAppend(t *testing.T) {
	m, dir := load(t)
	if m.Changed() {
		t.Fatal("changed before anything happened")
	}
	viewtest.Append(t, filepath.Join(dir, ".kgai", "store"), viewtest.More())
	if !m.Changed() {
		t.Fatal("an appended event went unnoticed")
	}
	if n := Load(dir); n.Stats.Decisions != 5 || n.Status().Counts.Conflicts != 1 {
		t.Fatalf("reloaded = %+v", n.Status())
	}
}
