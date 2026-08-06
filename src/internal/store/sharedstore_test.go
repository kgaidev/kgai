package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The use case these tests protect: a company keeps the decision logs of the repos that
// belong together under one directory (/opt/kgai), and does NOT want each developer to
// export KGAI_STORE by hand in every shell. Committing one `store` setting in the repo
// has to be enough — and it must not disturb the projects that never configure
// anything, which keep their own log where they always had it.

// writeAndTrust writes a committed config and approves it — the state a developer is in
// after `kg trust`. Tests of the gate itself live in trust_test.go.
func writeAndTrust(t *testing.T, path, cfg string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Trust(path); err != nil {
		t.Fatal(err)
	}
}

func repoWithConfig(t *testing.T, cfg string) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if cfg != "" {
		writeAndTrust(t, filepath.Join(repo, ProjectConfigName), cfg)
	}
	t.Setenv("KGAI_PROJECT", repo)
	return repo
}

// The machine-wide layer still works — the case for a box that exists for one purpose
// (managed dev machine, devcontainer, CI), where every checkout belongs to one graph.
func TestGlobalStoreSettingRedirectsEveryProject(t *testing.T) {
	t.Setenv("KGAI_HOME", t.TempDir())
	shared := filepath.Join(t.TempDir(), "opt-kgai")
	if err := SaveGlobalConfig(Settings{StoreRoot: shared}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"shop-api", "billing"} {
		repo := t.TempDir()
		t.Setenv("KGAI_PROJECT", filepath.Join(repo, name))
		if got := DefaultRoot(); got != shared {
			t.Errorf("%s: got %q, want the shared store %q", name, got, shared)
		}
	}
}

// Committed in the repo: whoever clones it writes into the company graph without
// configuring anything at all.
func TestProjectStoreSettingBeatsGlobal(t *testing.T) {
	t.Setenv("KGAI_HOME", t.TempDir())
	if err := SaveGlobalConfig(Settings{StoreRoot: "/tmp/global-kgai"}); err != nil {
		t.Fatal(err)
	}
	repoWithConfig(t, `{"store":"/opt/kgai/team"}`)
	if got := DefaultRoot(); got != "/opt/kgai/team" {
		t.Errorf("got %q, want the repo's committed store to win over the machine default", got)
	}
}

// A committed absolute path cannot be right on every machine; ~ and ${VARS} are what
// make one committed value portable.
func TestStorePathExpandsHomeAndEnvironment(t *testing.T) {
	t.Setenv("KGAI_HOME", t.TempDir())
	t.Setenv("COMPANY_KG", "/srv/kg")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory in this environment")
	}
	repoWithConfig(t, `{"store":"~/kgai-shared"}`)
	if got := DefaultRoot(); got != filepath.Join(home, "kgai-shared") {
		t.Errorf("~ not expanded: got %q", got)
	}
	repoWithConfig(t, `{"store":"${COMPANY_KG}/logs"}`)
	if got := DefaultRoot(); got != "/srv/kg/logs" {
		t.Errorf("${VAR} not expanded: got %q", got)
	}
}

// A relative value resolves against the repository root, not the working directory, so
// `kg` answers the same from a subdirectory as from the top — and the path is cleaned.
func TestRelativeStorePathIsAnchoredToTheRepoRoot(t *testing.T) {
	t.Setenv("KGAI_HOME", t.TempDir())
	repo := newRepo(t) // a real repo: the anchor is git's top level, not the cwd
	writeAndTrust(t, filepath.Join(repo, ProjectConfigName), `{"store":"../shared-kg"}`)
	sub := filepath.Join(repo, "services", "api")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Dir(repo), "shared-kg")
	for _, dir := range []string{repo, sub} {
		t.Chdir(dir)
		if got := DefaultRoot(); got != want {
			t.Errorf("from %s: got %q, want the same store %q", dir, got, want)
		}
	}
}

// A one-off override still has to work — the environment beats configuration.
func TestEnvironmentStillOverridesTheStoreSetting(t *testing.T) {
	t.Setenv("KGAI_HOME", t.TempDir())
	repoWithConfig(t, `{"store":"/opt/kgai/team"}`)
	t.Setenv("KGAI_STORE", "/tmp/one-off")
	if got := DefaultRoot(); got != "/tmp/one-off" {
		t.Errorf("got %q, want KGAI_STORE to win over every layer", got)
	}
}

// The session config lives INSIDE the store, so pointing at another store from there
// could never take effect. Refuse the write instead of accepting a no-op.
func TestStoreCannotBeSetInTheSessionLayer(t *testing.T) {
	if err := ValidateLayerKey(LayerSession, "store"); err == nil {
		t.Fatal("setting `store` in the session layer must be refused")
	}
	for _, l := range []string{LayerProject, LayerGlobal} {
		if err := ValidateLayerKey(l, "store"); err != nil {
			t.Errorf("%s layer must accept `store`: %v", l, err)
		}
	}
}

// ---- backward compatibility with stores and configs written by earlier versions ----

// The default has to stay exactly where every existing install already keeps it.
func TestUnconfiguredProjectKeepsTheOldStorePath(t *testing.T) {
	t.Setenv("KGAI_HOME", t.TempDir())
	repo := repoWithConfig(t, "")
	if got := DefaultRoot(); got != filepath.Join(repo, ".kgai", "store") {
		t.Errorf("got %q, want the unchanged per-project default", got)
	}
}

// A kg.config.json written before the layered config existed: every field must survive
// a read/modify/write, above all the cloud token, which is not a layered key.
func TestLegacySessionConfigRoundTrips(t *testing.T) {
	legacy := []byte(`{
  "install_id": "inst-1",
  "actor": "alex",
  "remote": "s3://legacy/bucket",
  "cloud_url": "https://api.example.com",
  "cloud_token": "secret",
  "schema_version": 1,
  "machine": "m1",
  "retired_installs": ["inst-0"]
}`)
	var c Config
	if err := json.Unmarshal(legacy, &c); err != nil {
		t.Fatalf("a config from the previous version must load: %v", err)
	}
	if c.Remote != "s3://legacy/bucket" || c.CloudURL != "https://api.example.com" {
		t.Errorf("layered keys did not load from the flat legacy shape: %+v", c)
	}
	if c.CloudToken != "secret" || c.InstallID != "inst-1" || c.Machine != "m1" || len(c.RetiredInstalls) != 1 {
		t.Errorf("identity fields lost: %+v", c)
	}
	// Writing a new key must not drop what the old version wrote.
	if err := c.Settings.Set("prompt", "house rules"); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var back Config
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if back.CloudToken != "secret" || back.Prompt != "house rules" || back.Remote != "s3://legacy/bucket" {
		t.Errorf("round trip lost data: %s", out)
	}
	// The token is install-local and must never be reachable as a layered setting.
	if _, err := back.Settings.Get("cloud_token"); err == nil {
		t.Error("cloud_token must not be a layered key")
	}
}

// A global config.json written by the previous version holds only `remote`; it must
// keep resolving, and must not be read as "store is configured".
func TestLegacyGlobalConfigStillResolves(t *testing.T) {
	t.Setenv("KGAI_HOME", t.TempDir())
	if err := os.WriteFile(filepath.Join(KgaiHome(), "config.json"),
		[]byte(`{"remote":"s3://team-bucket/kg/{project}"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := repoWithConfig(t, "")
	s := &Store{Root: filepath.Join(repo, ".kgai", "store")}
	url, source := s.EffectiveRemote()
	if url != "s3://team-bucket/kg/"+filepath.Base(repo) || source != LayerGlobal {
		t.Errorf("got (%q, %q), want the pre-existing global remote to keep working", url, source)
	}
	if got := DefaultRoot(); got != filepath.Join(repo, ".kgai", "store") {
		t.Errorf("got %q, want the store untouched by a config that never mentions it", got)
	}
}

// ---- worktrees ---------------------------------------------------------------
// A worktree is one project's branch checked out somewhere else, and the graph is
// branch-agnostic: every worktree must resolve to ONE store and ONE configuration.
// Reading .kgairc from the checked-out tree would break that — a branch editing `store`
// would silently give that worktree a different graph.

// worktreeOf adds a linked worktree at a sibling path and returns it.
func worktreeOf(t *testing.T, root, name string) string {
	t.Helper()
	wt := filepath.Join(filepath.Dir(root), name)
	git(t, root, "worktree", "add", "-q", "-b", name, wt)
	t.Cleanup(func() { git(t, root, "worktree", "remove", "--force", wt) })
	return wt
}

func TestWorktreeReadsTheProjectConfigOfTheMainWorktree(t *testing.T) {
	t.Setenv("KGAI_HOME", t.TempDir())
	t.Setenv("KGAI_STORE", "")
	root := newRepo(t)
	writeAndTrust(t, filepath.Join(root, ProjectConfigName), `{"prompt":"repo rules","store":"../company-kg"}`)
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "kgai config")
	wt := worktreeOf(t, root, "wt-config")

	want := filepath.Join(filepath.Dir(root), "company-kg")
	for _, dir := range []string{root, wt} {
		t.Chdir(dir)
		if got := ProjectConfigPath(); got != filepath.Join(root, ProjectConfigName) {
			t.Errorf("from %s: config path %q, want the main worktree's %q", dir, got, filepath.Join(root, ProjectConfigName))
		}
		if got := DefaultRoot(); got != want {
			t.Errorf("from %s: store %q, want the one shared store %q", dir, got, want)
		}
		layers, err := LoadLayers(nil)
		if err != nil {
			t.Fatal(err)
		}
		if val, src := Effective(layers, "prompt"); val != "repo rules" || src != LayerProject {
			t.Errorf("from %s: prompt (%q, %q), want the repo's rules", dir, val, src)
		}
	}
}

// The regression this guards: a branch that edits .kgairc in its own worktree must not
// move that worktree onto a different graph. Changes take effect once merged into the
// main worktree, exactly like the store location itself.
// A worktree created INSIDE the repo (<repo>/.worktrees/x — a common convention) is
// still a worktree: an earlier guard only asked whether the working directory was under
// the repo root, which this layout satisfies, so the branch's own .kgairc governed.
func TestWorktreeNestedInsideTheRepoStillFollowsTheMainWorktree(t *testing.T) {
	t.Setenv("KGAI_HOME", t.TempDir())
	t.Setenv("KGAI_STORE", "")
	root := newRepo(t)
	shared := t.TempDir()
	writeAndTrust(t, filepath.Join(root, ProjectConfigName), `{"store":"`+shared+`","prompt":"main rules"}`)
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "kgai config")

	wt := filepath.Join(root, ".worktrees", "feature")
	git(t, root, "worktree", "add", "-q", "-b", "nested", wt)
	t.Cleanup(func() { git(t, root, "worktree", "remove", "--force", wt) })
	// The branch rewrites the config in its own checkout.
	writeAndTrust(t, filepath.Join(wt, ProjectConfigName), `{"store":"/tmp/nested-only-kg","prompt":"branch rules"}`)

	t.Chdir(wt)
	if got := ProjectConfigPath(); got != filepath.Join(root, ProjectConfigName) {
		t.Errorf("config = %q, want the main worktree's %q", got, filepath.Join(root, ProjectConfigName))
	}
	if got := DefaultRoot(); got != shared {
		t.Errorf("store = %q, want the one shared store %q", got, shared)
	}
	layers, err := LoadLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	if val, _ := Effective(layers, "prompt"); val != "main rules" {
		t.Errorf("prompt = %q, want the main worktree's rules", val)
	}
}

func TestBranchLocalConfigDoesNotSplitTheGraph(t *testing.T) {
	t.Setenv("KGAI_HOME", t.TempDir())
	t.Setenv("KGAI_STORE", "")
	root := newRepo(t)
	writeAndTrust(t, filepath.Join(root, ProjectConfigName), `{"prompt":"repo rules"}`)
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "kgai config")
	wt := worktreeOf(t, root, "wt-branch")

	t.Chdir(root)
	wantStore := DefaultRoot()

	// The branch rewrites the config in its own checkout.
	writeAndTrust(t, filepath.Join(wt, ProjectConfigName), `{"prompt":"branch experiment","store":"/tmp/branch-only-kg"}`)
	t.Chdir(wt)
	if got := DefaultRoot(); got != wantStore {
		t.Errorf("store from the worktree = %q, want %q — a branch must not repoint the graph", got, wantStore)
	}
	layers, err := LoadLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	if val, _ := Effective(layers, "prompt"); val != "repo rules" {
		t.Errorf("prompt from the worktree = %q, want the main worktree's %q", val, "repo rules")
	}
}

// An enrolled repo whose store cannot be reached here — a shared volume that is not
// mounted, a path that does not exist on this machine — must not read as "the team has
// no history". Writes already fail loudly; the empty READ is the dangerous one, because
// the agent reports it as fact.
func TestConfiguredStoreNamesItselfInTheEmptyAnswer(t *testing.T) {
	t.Setenv("KGAI_HOME", t.TempDir())
	t.Setenv("KGAI_STORE", "")
	repoWithConfig(t, `{"store":"/nonexistent-root/kgai"}`)
	root, source, err := ResolveRootWithSource()
	if err != nil {
		t.Fatal(err)
	}
	if root != "/nonexistent-root/kgai" || source != LayerProject {
		t.Errorf("got (%q, %q), want the configured path and the layer that set it", root, source)
	}
	// And an unconfigured project must keep saying nothing special.
	repoWithConfig(t, "")
	if _, source, _ := ResolveRootWithSource(); source != "" {
		t.Errorf("source = %q for a default store, want empty", source)
	}
}
