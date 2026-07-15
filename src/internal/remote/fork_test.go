package remote

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kgai/internal/store"
)

// copyStoreDir simulates copying a project (rsync/zip/backup restore): the config
// and the log shards travel; the graph cache and lock do not matter.
func copyStoreDir(t *testing.T, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "copy")
	if err := os.MkdirAll(filepath.Join(dst, "log"), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(src, "kg.config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "kg.config.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(src, "log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(src, "log", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, "log", e.Name()), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

func decisionIDs(t *testing.T, s *store.Store) map[string]bool {
	t.Helper()
	evs, err := s.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, e := range evs {
		if e.Decision != nil {
			out[e.Decision.ID] = true
		}
	}
	return out
}

// A store copied WITHIN one machine shares both installId and machine fingerprint:
// only the remote-side fork check can catch it. Sync must fail loudly (pointing at
// `kg rotate`), and after rotation everything must converge with nothing lost.
func TestSameMachineCopyForksLoudlyAndRotateHeals(t *testing.T) {
	f := newFake()
	a := mkStore(t, "alice")
	addEvent(t, a, "base")
	syncVia(t, f, a)

	copyDir := copyStoreDir(t, a.Root)
	cp, err := store.Open(copyDir)
	if err != nil {
		t.Fatal(err)
	}
	if cp.Config.InstallID != a.Config.InstallID {
		t.Fatalf("copy must share the install id for this scenario")
	}

	// Both sides record different decisions; the original syncs first (wins).
	addEvent(t, a, "from-original")
	syncVia(t, f, a)
	addEvent(t, cp, "from-copy")

	r := &objectRemote{os: f, prefix: "team/kg", name: "fake"}
	_, err = r.Sync(cp)
	if err == nil {
		t.Fatalf("copy's sync must fail with a fork error, got success")
	}
	if !strings.Contains(err.Error(), "kg rotate") {
		t.Fatalf("fork error must point at `kg rotate`, got: %v", err)
	}

	// Remedy: rotate, then sync — the diverged decision is re-recorded and pushed.
	if _, _, err := cp.RotateInstall(); err != nil {
		t.Fatal(err)
	}
	res, err := r.Sync(cp)
	if err != nil {
		t.Fatalf("post-rotate sync must succeed: %v", err)
	}
	if !res.Pushed || !res.RebuildNeeded {
		t.Fatalf("post-rotate sync should push the re-emitted tail and demand a rebuild: %+v", res)
	}

	// The original pulls the copy's decision; both converge on the same decision set.
	syncVia(t, f, a)
	if _, err := r.Sync(cp); err != nil {
		t.Fatal(err)
	}
	idsA, idsC := decisionIDs(t, a), decisionIDs(t, cp)
	if len(idsA) != 3 || len(idsC) != 3 {
		t.Fatalf("both must hold 3 decisions (base, from-original, from-copy): A=%d C=%d", len(idsA), len(idsC))
	}
	for id := range idsA {
		if !idsC[id] {
			t.Fatalf("decision %s on A missing on copy", id)
		}
	}
}

// A store copied to ANOTHER machine must rotate its identity automatically (machine
// binding) and reconcile without any error or manual step.
func TestMachineChangeRotatesAndReconciles(t *testing.T) {
	t.Setenv("KGAI_MACHINE", "laptop-A")
	f := newFake()
	a := mkStore(t, "alice") // binds to laptop-A
	addEvent(t, a, "base")
	addEvent(t, a, "from-original")
	syncVia(t, f, a)

	copyDir := copyStoreDir(t, a.Root)

	// The copy wakes up on a different machine with an unpushed divergent tail.
	t.Setenv("KGAI_MACHINE", "laptop-B")
	cp, err := store.Open(copyDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := cp.Lock(); err != nil { // Lock is the rotation hook
		t.Fatal(err)
	}
	rotatedID := cp.Config.InstallID
	if rotatedID == a.Config.InstallID {
		t.Fatalf("machine change must rotate the install id")
	}
	if len(cp.Config.RetiredInstalls) != 1 || cp.Config.RetiredInstalls[0] != a.Config.InstallID {
		t.Fatalf("old id must be retired: %+v", cp.Config.RetiredInstalls)
	}
	addEvent(t, cp, "from-copy")
	cp.Unlock()

	r := &objectRemote{os: f, prefix: "team/kg", name: "fake"}
	res, err := r.Sync(cp)
	if err != nil {
		t.Fatalf("machine-rotated copy must sync cleanly: %v", err)
	}
	if !res.Pushed {
		t.Fatalf("copy should push its decision under the new id: %+v", res)
	}

	// Original keeps working on its machine and pulls the copy's decision.
	t.Setenv("KGAI_MACHINE", "laptop-A")
	syncVia(t, f, a)
	idsA, idsC := decisionIDs(t, a), decisionIDs(t, cp)
	if len(idsA) != 3 || len(idsC) != 3 {
		t.Fatalf("both must hold 3 decisions: A=%d C=%d", len(idsA), len(idsC))
	}
	for id := range idsA {
		if !idsC[id] {
			t.Fatalf("decision %s on A missing on copy", id)
		}
	}
}

// Migration case: the copy carries an UNPUSHED tail and the original never syncs
// again (dead laptop). The tail must still reach the team via re-emit.
func TestMigrationReemitsUnpushedTail(t *testing.T) {
	t.Setenv("KGAI_MACHINE", "old-laptop")
	f := newFake()
	a := mkStore(t, "alice")
	addEvent(t, a, "pushed-decision")
	syncVia(t, f, a)
	addEvent(t, a, "unpushed-decision") // recorded but never synced on the old laptop

	copyDir := copyStoreDir(t, a.Root)

	t.Setenv("KGAI_MACHINE", "new-laptop")
	cp, err := store.Open(copyDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := cp.Lock(); err != nil {
		t.Fatal(err)
	}
	cp.Unlock()

	r := &objectRemote{os: f, prefix: "team/kg", name: "fake"}
	res, err := r.Sync(cp)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pushed {
		t.Fatalf("the unpushed tail must be re-emitted and pushed: %+v", res)
	}

	// A fresh teammate sees BOTH decisions.
	t.Setenv("KGAI_MACHINE", "teammate")
	b := mkStore(t, "bob")
	syncVia(t, f, b)
	if ids := decisionIDs(t, b); len(ids) != 2 {
		t.Fatalf("teammate must see 2 decisions, got %d", len(ids))
	}
}
