package engine

import (
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
