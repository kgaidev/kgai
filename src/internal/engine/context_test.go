package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kgai/internal/store"
)

// Context is the recall path agents read before changing code — it must serve
// only current head decisions. Superseded decisions live in the log and are
// reachable via History, never pinned into context by default.
func TestContextReturnsOnlyHeadDecisions(t *testing.T) {
	s, err := store.Init(t.TempDir()+"/store", "test", "")
	if err != nil {
		t.Fatal(err)
	}
	e := New(s)

	_, err = e.Ingest(IngestInput{Decisions: []DecisionInput{{
		Title:     "Search hides sold-out products",
		Rationale: "Sold-out items clutter the results.",
		Mutations: []MutationInput{
			{Op: "upsert_element", Kind: "feature", Name: "product-search"},
			{Op: "set_prop", Element: "feature:product-search", Key: "show_sold_out", Value: "false"},
		},
	}}}, false)
	if err != nil {
		t.Fatal(err)
	}
	// Touching the same element auto-supersedes the prior head decision.
	_, err = e.Ingest(IngestInput{Decisions: []DecisionInput{{
		Title:     "Sold-out products stay visible in search",
		Rationale: "Hiding them dropped organic traffic.",
		Mutations: []MutationInput{
			{Op: "set_prop", Element: "feature:product-search", Key: "show_sold_out", Value: "true"},
		},
	}}}, false)
	if err != nil {
		t.Fatal(err)
	}

	res, err := e.Context(ContextQuery{About: "product-search", Max: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) == 0 {
		t.Fatal("expected the element in context")
	}
	why := res.Items[0].Why
	if len(why) != 1 {
		t.Fatalf("context must return only the head decision, got %d entries: %+v", len(why), why)
	}
	if why[0].Title != "Sold-out products stay visible in search" {
		t.Fatalf("wrong head decision in context: %q", why[0].Title)
	}
	if !why[0].IsHead {
		t.Fatal("head decision must be marked is_head")
	}

	// The dead end must still be reachable on demand.
	hist, err := e.History("feature:product-search")
	if err != nil {
		t.Fatal(err)
	}
	if len(hist.Decisions) != 2 {
		t.Fatalf("history must keep the superseded decision, got %d", len(hist.Decisions))
	}
}

// A question phrased in the words of what was DECIDED — sharing no token with the
// element's name — must still surface that element. The decision texts carry the
// vocabulary ("hide", "drafts"); the element is just "Invoice". Purely lexical:
// this must work with no embeddings and no LLM in the engine.
func TestContextAboutMatchesDecisionText(t *testing.T) {
	s, err := store.Init(t.TempDir()+"/store", "test", "")
	if err != nil {
		t.Fatal(err)
	}
	e := New(s)

	if _, err := e.Ingest(IngestInput{Decisions: []DecisionInput{{
		Title:     "Draft invoices stay visible",
		Rationale: "Hiding drafts lost users.",
		Mutations: []MutationInput{{Op: "upsert_element", Kind: "feature", Name: "Invoice"}},
	}, {
		Title:     "Sessions owned by the auth service",
		Rationale: "The user service should not own login state.",
		Mutations: []MutationInput{{Op: "upsert_element", Kind: "feature", Name: "Session"}},
	}}}, false); err != nil {
		t.Fatal(err)
	}

	res, err := e.Context(ContextQuery{About: "should I hide drafts from the list", Max: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || res.Items[0].Name != "Invoice" {
		t.Fatalf("decision-text vocabulary must surface the shaped element, got %+v", res.Items)
	}

	// Naming the element directly must stay the stronger signal than matching its
	// decisions' words — an exact name hit may not rank below a phrasing hit.
	direct, err := e.Context(ContextQuery{About: "Invoice", Max: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(direct.Items) == 0 || direct.Items[0].Name != "Invoice" {
		t.Fatalf("direct name query regressed: %+v", direct.Items)
	}
	if direct.Items[0].Score <= res.Items[0].Score {
		t.Fatalf("direct-name score (%v) must beat decision-text score (%v)",
			direct.Items[0].Score, res.Items[0].Score)
	}
}

// The head decisions are fetched only for the elements that survive ranking and
// truncation, so this guards the two things that decoupling can break: every item
// must carry ITS OWN decisions (not a neighbour's), and the recency tiebreak — now
// fed by a plain max(lamport) aggregation rather than by the head query — must still
// order elements newest first.
func TestContextAttachesWhyToTheRightElementsAfterTruncation(t *testing.T) {
	s, err := store.Init(t.TempDir()+"/store", "test", "")
	if err != nil {
		t.Fatal(err)
	}
	e := New(s)

	names := []string{"alpha", "bravo", "charlie", "delta", "echo"}
	for _, n := range names {
		if _, err := e.Ingest(IngestInput{Decisions: []DecisionInput{{
			Title:     "decided " + n,
			Rationale: "because of " + n,
			Mutations: []MutationInput{{Op: "upsert_element", Kind: "feature", Name: n}},
		}}}, false); err != nil {
			t.Fatal(err)
		}
	}
	// Supersede the oldest element so a stale decision exists to leak into context.
	if _, err := e.Ingest(IngestInput{Decisions: []DecisionInput{{
		Title:     "revised alpha",
		Rationale: "alpha again",
		Mutations: []MutationInput{{Op: "set_prop", Element: "feature:alpha", Key: "k", Value: "v"}},
	}}}, false); err != nil {
		t.Fatal(err)
	}

	// Unfiltered: every element qualifies, so ranking is the recency tiebreak alone.
	// "alpha" was just re-decided, so it now sorts newest, ahead of "echo".
	res, err := e.Context(ContextQuery{Max: 2})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != len(names) || res.Shown != 2 || res.Omitted != len(names)-2 {
		t.Fatalf("truncation accounting wrong: total=%d shown=%d omitted=%d", res.Total, res.Shown, res.Omitted)
	}
	want := []struct{ name, why string }{
		{"alpha", "revised alpha"},
		{"echo", "decided echo"},
	}
	for i, w := range want {
		got := res.Items[i]
		if got.Name != w.name {
			t.Fatalf("item %d: recency order broken, want %q got %q", i, w.name, got.Name)
		}
		if len(got.Why) != 1 {
			t.Fatalf("item %d (%s): want exactly the head decision, got %d: %+v", i, got.Name, len(got.Why), got.Why)
		}
		if got.Why[0].Title != w.why {
			t.Fatalf("item %d (%s): decisions attached to the wrong element, got %q want %q",
				i, got.Name, got.Why[0].Title, w.why)
		}
	}
}

// Export is the replay-determinism check, and `shapes` is the part of it that records
// which decision shaped which element. A Cypher alias that shadowed the node variables
// made the query fail silently, so every export carried "shapes": null and the digest
// could not detect a divergence in decision→element attribution at all.
func TestExportIncludesShapes(t *testing.T) {
	s, err := store.Init(t.TempDir()+"/store", "test", "")
	if err != nil {
		t.Fatal(err)
	}
	e := New(s)
	if _, err := e.Ingest(IngestInput{Decisions: []DecisionInput{{
		Title: "Payments split out",
		Mutations: []MutationInput{
			{Op: "upsert_element", Kind: "feature", Name: "Payments", Props: map[string]FlexString{"paths": "src/pay/*"}},
		},
	}}}, false); err != nil {
		t.Fatal(err)
	}
	out, err2 := e.Export(true)
	if err2 != nil {
		t.Fatal(err2)
	}
	if len(out.Shapes) == 0 {
		t.Fatal("export carries no shapes — the decision→element edges are missing from the determinism check")
	}
	row := out.Shapes[0]
	if row["decision"] == nil || row["element"] == nil {
		t.Errorf("shape row = %v, want a decision and an element id", row)
	}
	if out.Digest == "" {
		t.Error("canonical export must carry a digest")
	}
}

// Two things at once, because they failed together before: the CALLER must act on the
// scaffold guard's error — the guard had tests and the caller did not, which is how the
// token leak shipped green — and the refusal must be scoped to the transport that can
// actually leak. The object
// transport builds its payload from shard events and never reads the store directory, so
// blocking an S3 team over a .gitignore is stopping a supported setup for a file that
// cannot affect it — while a git remote must still refuse.
func TestScaffoldFailureBlocksGitButNotObjectSync(t *testing.T) {
	for _, c := range []struct {
		remote  string
		blocked bool
	}{
		{"/tmp/some-team-repo.git", true},
		{"s3://team-bucket/kg", false},
	} {
		dir := t.TempDir() + "/store"
		s, err := store.Init(dir, "test", c.remote)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules/\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, _, _, err = New(s).Sync()
		refused := err != nil && strings.Contains(err.Error(), "refusing to sync")
		if refused != c.blocked {
			t.Errorf("remote %q: refused=%v, want %v (err=%v)", c.remote, refused, c.blocked, err)
		}
	}
}
