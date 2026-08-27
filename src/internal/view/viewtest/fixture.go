// Package viewtest writes a small store for tests of the read model and of the readers
// built on it: two people, one contested element, one note, one link.
package viewtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"kgai/internal/event"
)

// Invoice and Pricing are the fixture's two elements.
var Invoice, Pricing = event.ElementID("feature", "Invoice"), event.ElementID("feature", "Pricing")

// Isolate keeps the test away from the machine's own configuration.
func Isolate(t testing.TB) {
	t.Helper()
	t.Setenv("KGAI_STORE", "")
	t.Setenv("KGAI_PROJECT", "")
	t.Setenv("KGAI_HOME", t.TempDir())
}

// WriteStore creates a store at root (a config and one shard) holding Events().
func WriteStore(t testing.TB, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "log"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"install_id":"i-alice","actor":"alice","schema_version":1}` + "\n"
	if err := os.WriteFile(filepath.Join(root, "kg.config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	Append(t, root, Events())
}

// Append adds events to the fixture's shard.
func Append(t testing.TB, root string, evs []event.Event) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(root, "log", "i-alice.ndjson"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, e := range evs {
		b, _ := json.Marshal(e)
		if _, err := f.Write(append(b, '\n')); err != nil {
			t.Fatal(err)
		}
	}
}

// Event builds one assert event with its hash.
func Event(actor string, lamport int64, at string, d event.Decision) event.Event {
	if d.Author == "" {
		d.Author = actor
	}
	d.ID = event.DecisionID(d)
	e := event.Event{Op: event.OpAssert, Actor: actor, InstallID: "i-alice", Lamport: lamport, RecordedAt: at, Decision: &d}
	e.Hash = e.ComputeHash()
	return e
}

// Events are the fixture: alice defines Invoice as part of Pricing; alice and bob then
// change the same property concurrently (a conflict); bob adds a rejected idea (a note).
func Events() []event.Event {
	base := Event("alice", 1, "2026-01-01T10:00:00Z", event.Decision{Title: "Invoice exists, part of Pricing",
		Rationale: "Invoices are priced items.",
		Mutations: []event.Mutation{
			{Op: event.MutUpsertElement, ElementID: Invoice, Kind: "feature", Name: "Invoice", Props: map[string]string{"paths": "src/inv/*"}},
			{Op: event.MutUpsertElement, ElementID: Pricing, Kind: "feature", Name: "Pricing"},
			{Op: event.MutAddLink, ElementID: Invoice, FromID: Invoice, ToID: Pricing, LinkKind: "PART_OF"}},
		Shapes: []string{Invoice, Pricing}, Targets: []string{Invoice}})
	al := Event("alice", 2, "2026-01-02T10:00:00Z", event.Decision{Title: "Invoices hidden when draft",
		Mutations: []event.Mutation{{Op: event.MutSetProp, ElementID: Invoice, Key: "drafts", Value: "hidden"}},
		Shapes:    []string{Invoice}, Targets: []string{Invoice}, Supersedes: []string{base.Decision.ID}})
	bo := Event("bob", 2, "2026-01-02T11:00:00Z", event.Decision{Title: "Draft invoices stay visible", Author: "bob + Claude",
		Refs:      "github:https://github.com/kgaidev/kgai/pull/1",
		Mutations: []event.Mutation{{Op: event.MutSetProp, ElementID: Invoice, Key: "drafts", Value: "visible"}},
		Shapes:    []string{Invoice}, Targets: []string{Invoice}, Supersedes: []string{base.Decision.ID}})
	note := Event("bob", 3, "2026-01-03T10:00:00Z", event.Decision{Title: "Considered PDF-only invoices, rejected",
		Mutations: []event.Mutation{{Op: event.MutUpsertElement, ElementID: Invoice, Kind: "feature", Name: "Invoice"}},
		Shapes:    []string{Invoice}, ProvenanceOnly: true})
	return []event.Event{base, al, bo, note}
}

// More is one further decision, for tests that watch the log grow.
func More() []event.Event {
	return []event.Event{Event("alice", 3, "2026-01-04T10:00:00Z", event.Decision{Title: "Pricing is a standalone feature",
		Mutations: []event.Mutation{{Op: event.MutSetProp, ElementID: Pricing, Key: "display", Value: "standalone"}},
		Shapes:    []string{Pricing}, Targets: []string{Pricing}})}
}
