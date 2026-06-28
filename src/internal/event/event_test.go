package event

import "testing"

func TestElementIDDeterministicAndNameInsensitive(t *testing.T) {
	a := ElementID("feature", "Invoice")
	b := ElementID("feature", "  invoice ")
	if a != b {
		t.Fatalf("normalization should make these equal: %s != %s", a, b)
	}
	if ElementID("business", "Invoice") == a {
		t.Fatal("different kinds must yield different ids")
	}
}

func TestDecisionIDContentAddressed(t *testing.T) {
	d := Decision{Title: "Invoice standalone", Rationale: "split from pricing",
		Mutations: []Mutation{{Op: MutRetireLink, FromID: "el_a", ToID: "el_b", LinkKind: "PART_OF"}}}
	if DecisionID(d) != DecisionID(d) {
		t.Fatal("id must be stable for identical content")
	}
	d2 := d
	d2.Title = "Invoice stays in pricing"
	if DecisionID(d) == DecisionID(d2) {
		t.Fatal("id must change when content changes")
	}
}

func TestEventHashVerify(t *testing.T) {
	e := Event{Op: OpAssert, Actor: "alice", Lamport: 1, RecordedAt: "2026-01-01T00:00:00Z",
		Decision: &Decision{ID: "d_x", Title: "t"}}
	e.ComputeHash()
	if !e.Verify() {
		t.Fatal("freshly hashed event must verify")
	}
	e.Lamport = 99
	if e.Verify() {
		t.Fatal("tampered event must not verify")
	}
}

func TestNormalize(t *testing.T) {
	if Normalize("  Foo   Bar ") != "foo bar" {
		t.Fatalf("got %q", Normalize("  Foo   Bar "))
	}
}
