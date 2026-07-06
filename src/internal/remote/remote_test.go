package remote

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"kgai/internal/event"
	"kgai/internal/store"
)

// ---- segment plan math -------------------------------------------------------

func TestParseSegmentKey(t *testing.T) {
	seg, ok := ParseSegmentKey("team/kg/segments/i1234/000007-42.ndjson")
	if !ok || seg.Install != "i1234" || seg.Seq != 7 || seg.Count != 42 {
		t.Fatalf("bad parse: %+v ok=%v", seg, ok)
	}
	for _, bad := range []string{"segments/i1/00001-2.ndjson", "foo.txt", "segments/i1/000001-0.ndjson"} {
		if _, ok := ParseSegmentKey(bad); ok {
			t.Fatalf("should not parse: %s", bad)
		}
	}
}

func TestPlanPushAndPull(t *testing.T) {
	segs := []Segment{{Seq: 1, Count: 3}, {Seq: 2, Count: 2}} // remote has 5
	if p, err := PlanPush(5, segs); err != nil || p.Needed {
		t.Fatalf("in sync should be no-op: %+v %v", p, err)
	}
	p, err := PlanPush(8, segs)
	if err != nil || !p.Needed || p.FromIndex != 5 || p.Seq != 3 {
		t.Fatalf("push plan wrong: %+v %v", p, err)
	}
	if _, err := PlanPush(4, segs); err == nil {
		t.Fatal("remote ahead of local must refuse push")
	}
	// pull: have 4 of 5 → fetch only segment 2, skipping 1 event
	fs := PlanPull(4, segs)
	if len(fs) != 1 || fs[0].Segment.Seq != 2 || fs[0].SkipEvents != 1 {
		t.Fatalf("pull plan wrong: %+v", fs)
	}
	if fs := PlanPull(5, segs); len(fs) != 0 {
		t.Fatalf("nothing to pull, got %+v", fs)
	}
}

// ---- fake object store + full two-store sync ----------------------------------

type fakeStore struct {
	mu   sync.Mutex
	objs map[string][]byte
}

func newFake() *fakeStore { return &fakeStore{objs: map[string][]byte{}} }

func (f *fakeStore) List(prefix string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keys []string
	for k := range f.objs {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (f *fakeStore) Get(key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.objs[key]
	if !ok {
		return nil, fmt.Errorf("no such key %s", key)
	}
	return b, nil
}

func (f *fakeStore) Put(key string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.objs[key]; exists {
		return fmt.Errorf("key %s already exists", key)
	}
	f.objs[key] = data
	return nil
}

func mkStore(t *testing.T, actor string) *store.Store {
	t.Helper()
	s, err := store.Init(t.TempDir()+"/store", actor, "")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func addEvent(t *testing.T, s *store.Store, title string) {
	t.Helper()
	ev := event.Event{Op: event.OpAssert, Decision: &event.Decision{Title: title,
		Mutations: []event.Mutation{{Op: event.MutUpsertElement, ElementID: "el_x", Kind: "feature", Name: "X"}}}}
	ev.Decision.ID = event.DecisionID(*ev.Decision)
	lam, _ := s.NextLamport()
	ev.Lamport = lam
	if err := s.Append(&ev); err != nil {
		t.Fatal(err)
	}
}

func syncVia(t *testing.T, f *fakeStore, s *store.Store) SyncResult {
	t.Helper()
	r := &objectRemote{os: f, prefix: "team/kg", name: "fake"}
	res, err := r.Sync(s)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestObjectSyncTwoStores(t *testing.T) {
	f := newFake()
	a, b := mkStore(t, "alice"), mkStore(t, "bob")

	addEvent(t, a, "A1")
	addEvent(t, a, "A2")
	res := syncVia(t, f, a)
	if !res.Pushed || res.Pulled {
		t.Fatalf("A first sync should push only: %+v", res)
	}

	addEvent(t, b, "B1")
	res = syncVia(t, f, b)
	if !res.Pushed || !res.Pulled {
		t.Fatalf("B should push B1 and pull A's events: %+v", res)
	}
	if evs, _ := b.ReadAll(); len(evs) != 3 {
		t.Fatalf("B should hold 3 events, has %d", len(evs))
	}

	// A pulls B1; incremental push from A afterwards lands as a second segment.
	res = syncVia(t, f, a)
	if !res.Pulled {
		t.Fatalf("A should pull B1: %+v", res)
	}
	addEvent(t, a, "A3")
	res = syncVia(t, f, a)
	if !res.Pushed {
		t.Fatalf("A should push A3: %+v", res)
	}
	res = syncVia(t, f, b)
	if !res.Pulled {
		t.Fatalf("B should pull A3: %+v", res)
	}

	evA, _ := a.ReadAll()
	evB, _ := b.ReadAll()
	if len(evA) != 4 || len(evB) != 4 {
		t.Fatalf("both should hold 4 events: A=%d B=%d", len(evA), len(evB))
	}
	// Same event sets on both sides.
	seen := map[string]bool{}
	for _, e := range evA {
		seen[e.Hash] = true
	}
	for _, e := range evB {
		if !seen[e.Hash] {
			t.Fatalf("event %s on B missing on A", e.Hash)
		}
	}
	// Idempotency: another sync round changes nothing.
	if res := syncVia(t, f, a); res.Pushed || res.Pulled {
		t.Fatalf("steady state should be a no-op: %+v", res)
	}
}

func TestObjectSyncRejectsTamperedSegment(t *testing.T) {
	f := newFake()
	a, b := mkStore(t, "alice"), mkStore(t, "bob")
	addEvent(t, a, "A1")
	syncVia(t, f, a)

	// Tamper with A's segment in the bucket.
	keys, _ := f.List("team/kg")
	f.mu.Lock()
	for _, k := range keys {
		f.objs[k] = []byte(strings.Replace(string(f.objs[k]), "A1", "EVIL", 1))
	}
	f.mu.Unlock()

	res := syncVia(t, f, b)
	if res.Pulled {
		t.Fatalf("tampered segment must not be applied: %+v", res)
	}
	if !strings.Contains(res.Detail, "hash verification") {
		t.Fatalf("detail should mention hash verification: %q", res.Detail)
	}
	if evs, _ := b.ReadAll(); len(evs) != 0 {
		t.Fatalf("B must hold no events after rejecting the tampered segment, has %d", len(evs))
	}
}
