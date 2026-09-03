package engine

import (
	"path/filepath"
	"strings"
	"testing"

	"kgai/internal/event"
	"kgai/internal/store"
)

func newTestEngine(t *testing.T, actor string) *Engine {
	t.Helper()
	s, err := store.Init(filepath.Join(t.TempDir(), actor), actor, "")
	if err != nil {
		t.Fatal(err)
	}
	return New(s)
}

func ingestErr(t *testing.T, e *Engine, title string, muts ...MutationInput) error {
	t.Helper()
	_, err := e.Ingest(IngestInput{Decisions: []DecisionInput{{
		Title: title, Rationale: "because " + title, Mutations: muts,
	}}}, false)
	return err
}

func elementCount(t *testing.T, e *Engine, id string) int {
	t.Helper()
	rows, err := e.Query(`MATCH (n:Element {id:'` + id + `'}) RETURN n.id`)
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

// The split-history bug: recording the same name under a contradicting kind used to
// silently mint a twin element, and every decision written to the twin became
// unreachable from the real element's history. The engine must refuse instead and
// name both the existing element and the escape hatch.
func TestWrongKindIsRefusedNotForked(t *testing.T) {
	e := newTestEngine(t, "alice")
	ingest(t, e, "Chat is a feature",
		MutationInput{Op: "upsert_element", Kind: "feature", Name: "Chat"})

	err := ingestErr(t, e, "Chat persists transcripts",
		MutationInput{Op: "upsert_element", Kind: "service", Name: "Chat"})
	if err == nil {
		t.Fatal("upserting service:Chat next to feature:Chat must be refused")
	}
	for _, want := range []string{"feature:Chat", "new_element"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal must mention %q, got: %v", want, err)
		}
	}
	// A link reference forks through ensureUpsert, so it must be gated the same way.
	err = ingestErr(t, e, "Chat depends on storage",
		MutationInput{Op: "add_link", From: "service:Chat", Link: "DEPENDS_ON", To: "service:Storage"})
	if err == nil || !strings.Contains(err.Error(), "feature:Chat") {
		t.Fatalf("link ref service:Chat must be refused with the existing element named, got: %v", err)
	}
	if n := elementCount(t, e, event.ElementID("service", "Chat")); n != 0 {
		t.Fatalf("refused ingest must not create service:Chat, found %d node(s)", n)
	}
}

// A bare name must bind to the element already carrying it — not silently default to
// kind "concept" and fork.
func TestBareNameBindsToExistingElement(t *testing.T) {
	e := newTestEngine(t, "alice")
	ingest(t, e, "Chat is a feature",
		MutationInput{Op: "upsert_element", Kind: "feature", Name: "Chat"})
	ingest(t, e, "Chat renders inline",
		MutationInput{Op: "set_prop", Element: "Chat", Key: "display", Value: "inline"})

	if n := elementCount(t, e, event.ElementID("concept", "Chat")); n != 0 {
		t.Fatalf("bare ref must not fork concept:Chat, found %d node(s)", n)
	}
	res, err := e.History("feature:Chat")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Decisions) != 2 {
		t.Fatalf("both decisions must land on feature:Chat, got %d", len(res.Decisions))
	}
}

// new_element is the sanctioned way to found a same-named facet — and once two
// elements carry the name, a bare reference is ambiguous and must say so.
func TestNewElementCreatesFacetAndBareNameTurnsAmbiguous(t *testing.T) {
	e := newTestEngine(t, "alice")
	ingest(t, e, "LakeFS as a concept",
		MutationInput{Op: "upsert_element", Kind: "concept", Name: "LakeFS"})
	ingest(t, e, "LakeFS runs as a service",
		MutationInput{Op: "upsert_element", Kind: "service", Name: "LakeFS", NewElement: true})

	if n := elementCount(t, e, event.ElementID("service", "LakeFS")); n != 1 {
		t.Fatalf("new_element must create the facet, found %d node(s)", n)
	}
	err := ingestErr(t, e, "LakeFS gets a note",
		MutationInput{Op: "set_prop", Element: "LakeFS", Key: "note", Value: "x"})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("bare ref over two same-named elements must be ambiguous, got: %v", err)
	}
	res, err := e.History("LakeFS")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Candidates) != 2 {
		t.Fatalf("bare-name history must offer both candidates, got %+v", res)
	}
}

// Mutation order must not matter: a link may reference the declared-new element
// before its upsert appears in the list.
func TestLinkMayPrecedeItsNewElementUpsert(t *testing.T) {
	e := newTestEngine(t, "alice")
	ingest(t, e, "Chat is a feature",
		MutationInput{Op: "upsert_element", Kind: "feature", Name: "Chat"})
	if _, err := e.Ingest(IngestInput{Decision: &DecisionInput{
		Title: "service:Chat is a distinct runtime facet", Rationale: "r",
		Mutations: []MutationInput{
			{Op: "add_link", From: "service:Chat", Link: "ALIAS_OF", To: "feature:Chat"},
			{Op: "upsert_element", Kind: "service", Name: "Chat", NewElement: true},
		},
	}}, false); err != nil {
		t.Fatalf("declared-new element must resolve regardless of mutation order: %v", err)
	}
}

// Same-name twins are surfaced on every history read, and an explicit ALIAS_OF link —
// and only that — folds them into one merged timeline.
func TestHistorySurfacesTwinsAndMergesAliases(t *testing.T) {
	e := newTestEngine(t, "alice")
	ingest(t, e, "Chat is a feature",
		MutationInput{Op: "upsert_element", Kind: "feature", Name: "Chat"})
	ingest(t, e, "Chat stores transcripts",
		MutationInput{Op: "upsert_element", Kind: "service", Name: "Chat", NewElement: true,
			Props: map[string]FlexString{"storage": "s3"}})

	res, err := e.History("feature:Chat")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.SameName) != 1 || res.SameName[0].Ref != "service:Chat" || res.SameName[0].Decisions != 1 {
		t.Fatalf("history must surface the unmerged twin with its decision count, got %+v", res.SameName)
	}
	if len(res.Decisions) != 1 {
		t.Fatalf("unmerged twin must NOT leak decisions into this history, got %d", len(res.Decisions))
	}

	ingest(t, e, "service:Chat was feature:Chat founded twice",
		MutationInput{Op: "add_link", From: "service:Chat", Link: "ALIAS_OF", To: "feature:Chat"})

	res, err = e.History("feature:Chat")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Merged) != 1 || res.Merged[0] != "service:Chat" {
		t.Fatalf("ALIAS_OF must merge the twin, got merged=%v", res.Merged)
	}
	if len(res.SameName) != 0 {
		t.Fatalf("a merged twin must leave same_name, got %+v", res.SameName)
	}
	if len(res.Decisions) != 3 {
		t.Fatalf("merged history must carry both elements' decisions, got %d", len(res.Decisions))
	}
	sawTwin := false
	for _, d := range res.Decisions {
		if d.On == "" {
			t.Fatalf("merged history must say which element each decision is on: %+v", d)
		}
		if d.On == "service:Chat" {
			sawTwin = true
		}
	}
	if !sawTwin {
		t.Fatal("the twin's decision must appear in the merged history")
	}
}

// The write gate cannot see another writer's unsynced shard: twins born from a race
// still appear. History must make them visible after the shards merge.
func TestRaceTwinsSurfaceAfterSync(t *testing.T) {
	root := t.TempDir()
	newStore := func(name string) (*store.Store, *Engine) {
		s, err := store.Init(filepath.Join(root, name), name, "")
		if err != nil {
			t.Fatal(err)
		}
		return s, New(s)
	}
	alice, ea := newStore("alice")
	ingest(t, ea, "Chat is a feature",
		MutationInput{Op: "upsert_element", Kind: "feature", Name: "Chat"})
	bob, eb := newStore("bob")
	ingest(t, eb, "Chat is a service",
		MutationInput{Op: "upsert_element", Kind: "service", Name: "Chat"})

	merged, em := newStore("merged")
	mergeShards(t, merged, alice, bob)
	if _, err := em.Rebuild(); err != nil {
		t.Fatal(err)
	}
	res, err := em.History("feature:Chat")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.SameName) != 1 || res.SameName[0].Ref != "service:Chat" {
		t.Fatalf("race twin must surface in same_name, got %+v", res.SameName)
	}
	if res, err := em.History("Chat"); err != nil || len(res.Candidates) != 2 {
		t.Fatalf("bare name over race twins must list candidates, got %+v (%v)", res, err)
	}
}

// Asking for a kind that doesn't exist while the name lives elsewhere must say where.
func TestHistoryMissingKindHintsAtSameName(t *testing.T) {
	e := newTestEngine(t, "alice")
	ingest(t, e, "Chat is a feature",
		MutationInput{Op: "upsert_element", Kind: "feature", Name: "Chat"})
	_, err := e.History("domain:Chat")
	if err == nil || !strings.Contains(err.Error(), "feature:Chat") {
		t.Fatalf("history of a missing kind must hint at the existing element, got: %v", err)
	}
}

// kg resolve is the pre-flight check: it must bind bare names the way ingest would
// and warn when a qualified ref is about to be refused.
func TestResolveNameSeesSameNameElements(t *testing.T) {
	e := newTestEngine(t, "alice")
	ingest(t, e, "Chat is a feature",
		MutationInput{Op: "upsert_element", Kind: "feature", Name: "Chat"})

	res, err := e.ResolveName("Chat")
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != "feature" || !res.Existed {
		t.Fatalf("bare resolve must bind to the existing element, got %+v", res)
	}
	res, err = e.ResolveName("service:Chat")
	if err != nil {
		t.Fatal(err)
	}
	if res.Existed {
		t.Fatalf("service:Chat must not exist, got %+v", res)
	}
	if len(res.SameName) != 1 || res.SameName[0].Ref != "feature:Chat" || res.Note == "" {
		t.Fatalf("resolve must warn about the same-named element, got %+v", res)
	}
}
