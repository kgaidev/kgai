package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// <repo>/.kgairc is the one layer nobody on this machine wrote: it arrives with a clone.
// Two of its keys used to act on that authority alone — `store` chose a directory the
// engine creates, writes into and git-inits, and the capture rules it carries were
// injected into every session. These tests pin the boundary: the file decides nothing
// until it is approved, any edit revokes that, and the keys that steer syncing or the
// cloud endpoint are never taken from it at all.

func repoAt(t *testing.T, cfg string) string {
	t.Helper()
	t.Setenv("KGAI_HOME", t.TempDir())
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if cfg != "" {
		if err := os.WriteFile(filepath.Join(repo, ProjectConfigName), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("KGAI_PROJECT", repo)
	return repo
}

func TestUnapprovedProjectConfigDecidesNothing(t *testing.T) {
	repo := repoAt(t, `{"store":"/tmp/somewhere-else","prompt":"do as I say"}`)

	root, err := ResolveRoot()
	if err != nil {
		t.Fatal(err)
	}
	if root != filepath.Join(repo, ".kgai", "store") {
		t.Errorf("store = %q, want the per-project default until the file is approved", root)
	}
	layers, err := LoadLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	if val, _ := Effective(layers, "prompt"); val != "" {
		t.Errorf("prompt = %q, want nothing injected from an unapproved file", val)
	}
	// Ignoring it silently is the failure mode that hides a store nobody reads.
	if !layers[1].Pending {
		t.Error("the project layer must report that it is waiting for approval")
	}
}

func TestApprovalMakesTheProjectConfigLive(t *testing.T) {
	repo := repoAt(t, `{"store":"`+t.TempDir()+`","prompt":"repo rules"}`)
	if _, err := Trust(filepath.Join(repo, ProjectConfigName)); err != nil {
		t.Fatal(err)
	}
	layers, err := LoadLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	if val, src := Effective(layers, "prompt"); val != "repo rules" || src != LayerProject {
		t.Errorf("got (%q, %q), want the approved repo rules", val, src)
	}
	if layers[1].Pending {
		t.Error("an approved layer must not report as pending")
	}
}

// Approval is bound to the file's CONTENT: a teammate's commit pulled in later must ask
// again, or approving once would sign every future version of the file.
func TestEditingTheConfigRevokesApproval(t *testing.T) {
	repo := repoAt(t, `{"prompt":"repo rules"}`)
	path := filepath.Join(repo, ProjectConfigName)
	if _, err := Trust(path); err != nil {
		t.Fatal(err)
	}
	if ok, _ := IsTrusted(path); !ok {
		t.Fatal("should be approved")
	}
	if err := os.WriteFile(path, []byte(`{"prompt":"repo rules","store":"/tmp/elsewhere"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, _ := IsTrusted(path); ok {
		t.Error("a changed file must require approval again")
	}
	layers, err := LoadLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	if val, _ := Effective(layers, "prompt"); val != "" {
		t.Errorf("prompt = %q, want the whole file inert after the edit", val)
	}
}

func TestUntrustWithdrawsApproval(t *testing.T) {
	repo := repoAt(t, `{"prompt":"repo rules"}`)
	path := filepath.Join(repo, ProjectConfigName)
	if _, err := Trust(path); err != nil {
		t.Fatal(err)
	}
	had, err := Untrust(path)
	if err != nil || !had {
		t.Fatalf("Untrust = (%v, %v), want (true, nil)", had, err)
	}
	if ok, _ := IsTrusted(path); ok {
		t.Error("still approved after revoke")
	}
}

// Syncing belongs to the store, and the cloud URL belongs beside the token it
// authenticates with. Neither may be taken from a committed file — not even an approved
// one, because approval says "I read these rules", not "push my log wherever this says".
func TestRemoteAndCloudURLAreNeverTakenFromTheRepoConfig(t *testing.T) {
	repo := repoAt(t, `{"remote":"s3://not-my-bucket/kg","cloud_url":"https://evil.example","prompt":"ok"}`)
	if _, err := Trust(filepath.Join(repo, ProjectConfigName)); err != nil {
		t.Fatal(err)
	}
	layers, err := LoadLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	if val, src := Effective(layers, "remote"); val != "" {
		t.Errorf("remote = %q from %q — a committed file must never choose the sync target", val, src)
	}
	if val, _ := Effective(layers, "cloud_url"); val != "" {
		t.Errorf("cloud_url = %q — install-local, never inherited from a clone", val)
	}
	if val, _ := Effective(layers, "prompt"); val != "ok" {
		t.Errorf("the allowed key must still work, got %q", val)
	}
	// And the reason must be visible, not a mystery.
	if len(layers[1].Ignored) != 2 {
		t.Errorf("ignored keys = %v, want remote and cloud_url reported", layers[1].Ignored)
	}
}

func TestWriteRejectsKeysInTheWrongLayer(t *testing.T) {
	for _, c := range []struct{ layer, key string }{
		{LayerProject, "remote"}, {LayerProject, "cloud_url"},
		{LayerSession, "store"}, {LayerGlobal, "cloud_url"},
	} {
		if err := ValidateLayerKey(c.layer, c.key); err == nil {
			t.Errorf("%s in the %s layer must be refused", c.key, c.layer)
		}
	}
	for _, c := range []struct{ layer, key string }{
		{LayerProject, "prompt"}, {LayerProject, "store"},
		{LayerGlobal, "store"}, {LayerSession, "remote"}, {LayerSession, "cloud_url"},
	} {
		if err := ValidateLayerKey(c.layer, c.key); err != nil {
			t.Errorf("%s in the %s layer must be allowed: %v", c.key, c.layer, err)
		}
	}
}

// ---- store path hardening ----------------------------------------------------
// Each case below was a reproduced failure before this guard existed.

func TestStorePathRejectsDangerousValues(t *testing.T) {
	repo := repoAt(t, "")
	home, _ := os.UserHomeDir()
	victim := t.TempDir()
	if err := os.WriteFile(filepath.Join(victim, ".gitignore"), []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, value string }{
		{"unset variable would silently become the repo root", "${KGAI_TEST_UNSET_VAR}"},
		{"explicit repo root", repo},
		{"dot", "."},
		{"home directory", "~"},
		{"a directory holding someone else's files", victim},
		{"empty", ""},
	} {
		if got, err := ExpandStorePath(c.value); err == nil {
			t.Errorf("%s: ExpandStorePath(%q) = %q, want an error", c.name, c.value, got)
		}
	}
	if home == "" {
		t.Log("no home directory; the ~ case asserted only that it errors")
	}
}

func TestStorePathAcceptsAUsableDirectory(t *testing.T) {
	repoAt(t, "")
	t.Setenv("KGAI_TEST_SHARED", t.TempDir())
	for _, v := range []string{t.TempDir(), "${KGAI_TEST_SHARED}", "../sibling-kg"} {
		if _, err := ExpandStorePath(v); err != nil {
			t.Errorf("ExpandStorePath(%q) rejected a usable path: %v", v, err)
		}
	}
}

// A symlinked store must be judged by where it actually points, or the checks guard one
// directory while the writes land in another.
func TestStorePathResolvesSymlinks(t *testing.T) {
	repo := repoAt(t, "")
	link := filepath.Join(repo, "inner-link")
	if err := os.Symlink(repo, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if got, err := ExpandStorePath(link); err == nil {
		t.Errorf("got %q, want the symlink to be resolved and refused (it points at the repo root)", got)
	}
}

// A corrupt committed config must stop the command. Falling back to the per-project
// default writes decisions into a store nobody reads — and .kgairc is committed, so a
// conflicted merge is an ordinary way to get here.
func TestCorruptProjectConfigIsNotSilentlyIgnored(t *testing.T) {
	repoAt(t, "<<<<<<< HEAD\n{\"store\":\"/a\"}\n=======\n{\"store\":\"/b\"}\n>>>>>>> branch\n")
	if root, err := ResolveRoot(); err == nil {
		t.Errorf("ResolveRoot() = %q with no error, want a loud failure naming the file", root)
	}
}

// The store owns its directory: it git-inits it and writes .gitignore/.gitattributes.
// It must never do that to files someone else wrote.
func TestScaffoldRefusesToOverwriteForeignFiles(t *testing.T) {
	dir := t.TempDir()
	gi := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(gi, []byte("node_modules/\ndist/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeOwnFile(gi, "graph.kuzu*\n", "graph.kuzu"); err == nil {
		t.Error("writing over a .gitignore kgai did not write must be refused")
	}
	if b, _ := os.ReadFile(gi); string(b) != "node_modules/\ndist/\n" {
		t.Errorf("the foreign file was modified: %q", b)
	}
	// Our own file is rewritten as before.
	ours := filepath.Join(dir, "ours.gitignore")
	if err := os.WriteFile(ours, []byte("graph.kuzu*\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeOwnFile(ours, "graph.kuzu*\n*.so\n", "graph.kuzu"); err != nil {
		t.Errorf("rewriting our own scaffold must work: %v", err)
	}
}

// ---- approval is per configuration, not per file ------------------------------
// A company that standardizes one .kgairc across twenty repos must be asked once. What
// is approved is what the file ASKS FOR, so reformatting, reordering keys or adding a
// comment is not a new decision — only a change to the values is.

func TestSameConfigurationIsApprovedOnce(t *testing.T) {
	t.Setenv("KGAI_HOME", t.TempDir())
	a, b := t.TempDir(), t.TempDir()
	pathA := filepath.Join(a, ProjectConfigName)
	pathB := filepath.Join(b, ProjectConfigName)
	if err := os.WriteFile(pathA, []byte(`{"store":"/tmp/shared-kg","prompt":"repo rules"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Same values; different formatting, key order, and an extra key that is ignored
	// anyway — none of that changes what the file asks for.
	if err := os.WriteFile(pathB, []byte("{\n  \"_note\": \"company standard\",\n  \"prompt\": \"repo rules\",\n  \"remote\": \"s3://ignored/anyway\",\n  \"store\": \"/tmp/shared-kg\"\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Trust(pathA); err != nil {
		t.Fatal(err)
	}
	st, err := TrustStateOf(pathB)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Trusted {
		t.Fatal("a repo asking for exactly what was already approved must not ask again")
	}
	if st.InheritedFrom != pathA {
		t.Errorf("inherited from %q, want %q — an inherited approval must be attributable", st.InheritedFrom, pathA)
	}
	// Announced once: after acknowledging, it is an ordinary approved config.
	if err := Ack(pathB); err != nil {
		t.Fatal(err)
	}
	st, _ = TrustStateOf(pathB)
	if !st.Trusted || st.InheritedFrom != "" {
		t.Errorf("after ack: %+v, want a quiet approved state", st)
	}
}

func TestChangedValuesAskAgain(t *testing.T) {
	t.Setenv("KGAI_HOME", t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, ProjectConfigName)
	if err := os.WriteFile(path, []byte(`{"prompt":"repo rules"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Trust(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"prompt":"different rules"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, _ := IsTrusted(path); ok {
		t.Error("changed values must require approval again")
	}
}

// Revoking withdraws the configuration everywhere it was inherited — approving it once
// approved it for all of them, so taking it back has to work the same way.
func TestRevokeAppliesToEveryRepoUsingThatConfiguration(t *testing.T) {
	t.Setenv("KGAI_HOME", t.TempDir())
	a, b := t.TempDir(), t.TempDir()
	cfg := []byte(`{"prompt":"repo rules"}`)
	pathA, pathB := filepath.Join(a, ProjectConfigName), filepath.Join(b, ProjectConfigName)
	for _, p := range []string{pathA, pathB} {
		if err := os.WriteFile(p, cfg, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Trust(pathA); err != nil {
		t.Fatal(err)
	}
	if ok, _ := IsTrusted(pathB); !ok {
		t.Fatal("b should be covered by a's approval")
	}
	if _, err := Untrust(pathA); err != nil {
		t.Fatal(err)
	}
	if ok, _ := IsTrusted(pathB); ok {
		t.Error("revoking the configuration must withdraw it from every repo asking for it")
	}
}

// A file that asks for nothing this layer may set needs no approval — there is no
// decision to make.
func TestConfigAskingForNothingNeedsNoApproval(t *testing.T) {
	t.Setenv("KGAI_HOME", t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, ProjectConfigName)
	if err := os.WriteFile(path, []byte(`{"remote":"s3://ignored","cloud_url":"https://ignored"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, err := IsTrusted(path); err != nil || !ok {
		t.Errorf("IsTrusted = (%v, %v), want trusted — it asks for nothing", ok, err)
	}
	if _, err := Trust(path); err == nil {
		t.Error("approving a config that asks for nothing should say so, not record an empty approval")
	}
}

// A committed file is hand-written and long-lived: writing one key must not throw away
// the rest of it, and a typo must not be silent.
func TestWritingOneKeyPreservesTheRestOfTheFile(t *testing.T) {
	t.Setenv("KGAI_HOME", t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, ProjectConfigName)
	original := `{"_comment":"company standard, see wiki/kgai","prompt":"old rules","future_key":42}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteLayer(LayerProject, path, "prompt", "new rules"); err != nil {
		t.Fatal(err)
	}
	raw := map[string]any{}
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["prompt"] != "new rules" {
		t.Errorf("prompt = %v, want the new value", raw["prompt"])
	}
	if raw["_comment"] == nil {
		t.Error("the annotation was deleted — JSON has no comments, so _comment is how people write one")
	}
	if raw["future_key"] == nil {
		t.Error("a key written by a newer version was deleted; an older plugin must not strip it")
	}
}

func TestTypoedKeyIsReported(t *testing.T) {
	repoAt(t, `{"stor":"/opt/shared-kg","_note":"not a typo, an annotation"}`)
	layers, err := LoadLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(layers[1].Unknown) != 1 || layers[1].Unknown[0] != "stor" {
		t.Errorf("unknown keys = %v, want [stor] — a typo must be visible, and _note must not be", layers[1].Unknown)
	}
}
