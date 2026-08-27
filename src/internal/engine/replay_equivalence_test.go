package engine

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"kgai/internal/replay"
	"kgai/internal/store"
)

// The kgview preview app answers from internal/replay alone — it never opens the
// graph. That is only honest if the in-memory projection and the Kuzu projection are
// the same thing. This test builds a store the way a team does (two writers, concurrent
// edits, a resolution, retired links, props, a dead end) and checks that replay
// describes it exactly as the engine does: every element, link, decision and shape of
// the canonical export, the heads, the conflicts and the history order.
func TestReplayMatchesKuzuProjection(t *testing.T) {
	root := t.TempDir()
	newStore := func(name, actor string) (*store.Store, *Engine) {
		s, err := store.Init(filepath.Join(root, name), actor, "")
		if err != nil {
			t.Fatal(err)
		}
		return s, New(s)
	}
	alice, ea := newStore("alice", "alice")
	bob, eb := newStore("bob", "bob")

	ingest(t, ea, "Invoice exists",
		MutationInput{Op: "upsert_element", Kind: "feature", Name: "Invoice", Props: map[string]FlexString{"paths": "src/inv/*", "display": "list"}},
		MutationInput{Op: "upsert_element", Kind: "feature", Name: "Pricing"},
		MutationInput{Op: "add_link", From: "feature:Invoice", Link: "PART_OF", To: "feature:Pricing"})
	ingest(t, ea, "Invoice renders standalone",
		MutationInput{Op: "retire_link", From: "feature:Invoice", Link: "PART_OF", To: "feature:Pricing"},
		MutationInput{Op: "set_prop", Element: "feature:Invoice", Key: "display", Value: "standalone"})
	// Bob, unaware, decides Invoice too → a conflict once the shards meet.
	ingest(t, eb, "Invoice hidden while draft",
		MutationInput{Op: "set_prop", Element: "feature:Invoice", Key: "drafts", Value: "hidden"})
	// A dead end: provenance only, never a head.
	ingest(t, eb, "Considered PDF-only invoices, rejected",
		MutationInput{Op: "upsert_element", Kind: "feature", Name: "Invoice"})
	ingest(t, eb, "Checkout depends on Pricing",
		MutationInput{Op: "upsert_element", Kind: "service", Name: "Checkout"},
		MutationInput{Op: "add_link", From: "service:Checkout", Link: "DEPENDS_ON", To: "feature:Pricing"})

	merged, em := newStore("merged", "merged")
	mergeShards(t, merged, alice, bob)
	if _, err := em.Rebuild(); err != nil {
		t.Fatal(err)
	}

	evs, err := merged.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	p := replay.Build(evs)
	exp, err := em.Export(true)
	if err != nil {
		t.Fatal(err)
	}

	// ---- nodes and edges, as the canonical export lists them ----
	var got, want []string
	for _, id := range replay.SortedKeys(p.Elements) {
		e := p.Elements[id]
		got = append(got, strings.Join([]string{id, e.Kind, e.Name, e.Props}, "|"))
	}
	for _, r := range exp.Elements {
		want = append(want, strings.Join([]string{asStr(r["id"]), asStr(r["kind"]), asStr(r["name"]), asStr(r["props"])}, "|"))
	}
	same(t, "elements", got, want)

	got, want = nil, nil
	for _, k := range replay.SortedKeys(p.Links) {
		l := p.Links[k]
		got = append(got, l.From+"|"+l.Kind+"|"+l.To)
	}
	for _, r := range exp.Links {
		want = append(want, asStr(r["f"])+"|"+asStr(r["k"])+"|"+asStr(r["t"]))
	}
	same(t, "links", got, want)

	got, want = nil, nil
	for _, id := range replay.SortedKeys(p.Decisions) {
		d := p.Decisions[id]
		got = append(got, id+"|"+d.Title+"|"+strconv.FormatInt(d.Lamport, 10))
	}
	for _, r := range exp.Decisions {
		want = append(want, asStr(r["id"])+"|"+asStr(r["title"])+"|"+strconv.FormatInt(asInt(r["lamport"]), 10))
	}
	same(t, "decisions", got, want)

	got, want = nil, nil
	for _, k := range replay.SortedKeys(p.Shapes) {
		s := p.Shapes[k]
		got = append(got, fmt.Sprintf("%s|%s|%v", s.Decision, s.Element, s.Authority))
	}
	for _, r := range exp.Shapes {
		want = append(want, fmt.Sprintf("%s|%s|%v", asStr(r["decision"]), asStr(r["element"]), r["authority"]))
	}
	same(t, "shapes", got, want)

	// ---- what the reads derive from them ----
	kc, err := em.Conflicts("")
	if err != nil {
		t.Fatal(err)
	}
	rc := p.Conflicts()
	if len(kc) != 1 || len(rc) != 1 {
		t.Fatalf("expected exactly one conflict: kg=%d replay=%d", len(kc), len(rc))
	}
	if kc[0].ElementID != rc[0].ElementID || strings.Join(kc[0].Heads, ",") != strings.Join(rc[0].Heads, ",") {
		t.Fatalf("conflict differs:\n kg     %+v\n replay %+v", kc[0], rc[0])
	}

	for _, name := range []string{"feature:Invoice", "feature:Pricing", "service:Checkout"} {
		kh, err := em.History(name)
		if err != nil {
			t.Fatal(err)
		}
		rh := p.History(kh.ElementID)
		got, want = nil, nil
		for _, e := range rh {
			got = append(got, fmt.Sprintf("%s|%v", e.Decision.ID, e.IsHead))
		}
		for _, d := range kh.Decisions {
			want = append(want, fmt.Sprintf("%s|%v", d.ID, d.IsHead))
		}
		same(t, "history of "+name, got, want)
	}

	// Resolve the branch: one decision that takes authority again supersedes both heads.
	ingest(t, em, "Drafts visible to owners",
		MutationInput{Op: "set_prop", Element: "feature:Invoice", Key: "drafts", Value: "owners"})
	evs, _ = merged.ReadAll()
	p = replay.Build(evs)
	kc, _ = em.Conflicts("")
	if len(kc) != 0 || len(p.Conflicts()) != 0 {
		t.Fatalf("resolution must clear the conflict on both sides: kg=%d replay=%d", len(kc), len(p.Conflicts()))
	}
	st, _ := em.Status()
	rs := p.Stats()
	if st.Events != rs.Events || st.Elements != rs.Elements || st.Decisions != rs.Decisions || st.Conflicts != rs.Conflicts {
		t.Fatalf("status differs: kg=%+v replay=%+v", st, rs)
	}
}

func same(t *testing.T, what string, got, want []string) {
	t.Helper()
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%s differ\n--- replay ---\n%s\n--- kg ---\n%s", what, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
