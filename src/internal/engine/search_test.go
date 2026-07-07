package engine

import "testing"

func TestTokenize(t *testing.T) {
	got := tokenize("How is InvoiceRendering structured in the billing-module?")
	want := map[string]bool{"invoice": true, "rendering": true, "structured": true, "billing": true, "module": true}
	for _, tok := range got {
		if !want[tok] {
			t.Fatalf("unexpected token %q in %v", tok, got)
		}
		delete(want, tok)
	}
	if len(want) != 0 {
		t.Fatalf("missing tokens %v (got %v)", want, got)
	}
}

func TestFuzzyMatch(t *testing.T) {
	if matchStrength("invoice", "invoice") != 1.0 {
		t.Fatal("exact must be 1.0")
	}
	if s := matchStrength("invo", "invoice"); s < 0.5 {
		t.Fatalf("prefix should score, got %v", s)
	}
	if s := matchStrength("invoicing", "invoice"); s < 0.5 {
		t.Fatalf("morphological variant should score, got %v", s)
	}
	if s := matchStrength("rendering", "render"); s < 0.5 {
		t.Fatalf("morphological variant should score, got %v", s)
	}
	if s := matchStrength("invoce", "invoice"); s < 0.4 { // single-typo
		t.Fatalf("1-edit typo should score, got %v", s)
	}
	if matchStrength("xyz", "invoice") != 0 {
		t.Fatal("unrelated must be 0")
	}
	if s := matchStrength("inventory", "invoice"); s != 0 { // lcp 3, ratio 0.33
		t.Fatalf("inventory vs invoice must not match, got %v", s)
	}
}

func TestScoreDocsRanksByRelevance(t *testing.T) {
	docs := []searchDoc{
		elementDoc("el_1", "feature", "Invoice", "paths=src/billing/*", "feature"),
		elementDoc("el_2", "feature", "Pricing", "paths=src/pricing/*", "feature"),
		elementDoc("el_3", "service", "Checkout", "", "service"),
		{
			hit: SearchHit{Kind: "decision", ID: "d_1", Name: "Invoice is standalone, independent of Pricing"},
			fields: []searchField{
				{3.0, tokenize("Invoice is standalone, independent of Pricing")},
				{1.0, tokenize("billing is its own domain")},
			},
		},
	}
	hits := scoreDocs("how is invoice rendering handled in billing", docs)
	if len(hits) < 2 {
		t.Fatalf("expected hits, got %v", hits)
	}
	if hits[0].ID != "el_1" && hits[0].ID != "d_1" {
		t.Fatalf("invoice-related docs must rank first, got %+v", hits[0])
	}
	for _, h := range hits {
		if h.ID == "el_3" {
			t.Fatalf("Checkout must not match an invoice/billing query")
		}
	}
}

func TestAboutScore(t *testing.T) {
	if aboutScore("billing invoices", "feature", "Invoice", "paths=src/billing/*") <= 0 {
		t.Fatal("contextual about must match Invoice")
	}
	if aboutScore("frontend css theming", "feature", "Invoice", "") != 0 {
		t.Fatal("unrelated about must be 0")
	}
}
