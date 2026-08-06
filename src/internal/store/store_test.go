package store

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// ProjectRoot decides WHERE a project's knowledge graph lives, so getting it wrong does
// not fail loudly — it silently hands back an empty graph. The worktree cases matter
// most: `git worktree add` is how people run several branches at once, and the KG is
// branch-agnostic by design, so every worktree of a project must resolve to one store.

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Keep the test independent of the developer's own git identity/config.
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return string(out)
}

// newRepo creates a git repo with one commit and returns its symlink-resolved root
// (macOS /tmp is a symlink; git reports the resolved path, so expectations must match).
func newRepo(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git(t, root, "init", "-q", "-b", "main", root)
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "init")
	return root
}

func TestProjectRootOrdinaryRepo(t *testing.T) {
	root := newRepo(t)
	sub := filepath.Join(root, "pkg", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{root, sub} {
		t.Chdir(dir)
		if got := ProjectRoot(); got != root {
			t.Errorf("ProjectRoot() from %s = %q, want repo root %q", dir, got, root)
		}
	}
}

// The regression this guards: a linked worktree used to resolve to itself, giving it a
// brand-new empty store instead of the project's graph.
func TestProjectRootLinkedWorktreeResolvesToMainWorktree(t *testing.T) {
	root := newRepo(t)
	wt := filepath.Join(filepath.Dir(root), "wt")
	git(t, root, "worktree", "add", "-q", "-b", "feature", wt)
	t.Cleanup(func() { git(t, root, "worktree", "remove", "--force", wt) })

	sub := filepath.Join(wt, "pkg", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{wt, sub} {
		t.Chdir(dir)
		if got := ProjectRoot(); got != root {
			t.Errorf("ProjectRoot() from worktree %s = %q, want main worktree %q", dir, got, root)
		}
	}
}

// Same store means the same default store path — that is what actually shares the graph.
func TestDefaultRootSharedAcrossWorktrees(t *testing.T) {
	// Ignore any store override in the developer's own environment or machine config —
	// both can now redirect DefaultRoot, which would make this assertion vacuous.
	t.Setenv("KGAI_STORE", "")
	t.Setenv("KGAI_HOME", t.TempDir())
	root := newRepo(t)
	wt := filepath.Join(filepath.Dir(root), "wt-store")
	git(t, root, "worktree", "add", "-q", "-b", "feature-store", wt)
	t.Cleanup(func() { git(t, root, "worktree", "remove", "--force", wt) })

	t.Chdir(root)
	want := DefaultRoot()
	t.Chdir(wt)
	if got := DefaultRoot(); got != want {
		t.Errorf("DefaultRoot() in worktree = %q, want %q (one store per project, not per worktree)", got, want)
	}
}

// A submodule is its own project, not a worktree of its superproject: its git dir lives
// under <super>/.git/modules/..., whose parent is not a project root.
func TestProjectRootSubmoduleKeepsItsOwnRoot(t *testing.T) {
	super, inner := newRepo(t), newRepo(t)
	out, err := exec.Command("git", "-C", super, "-c", "protocol.file.allow=always",
		"submodule", "add", "-q", inner, "vendor/inner").CombinedOutput()
	if err != nil {
		t.Skipf("submodule add unsupported here: %v\n%s", err, out)
	}
	nested := filepath.Join(super, "vendor", "inner")
	t.Chdir(nested)
	if got := ProjectRoot(); got != nested {
		t.Errorf("ProjectRoot() in submodule = %q, want the submodule root %q", got, nested)
	}
}

func TestProjectRootOutsideGitIsWorkingDir(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	// t.TempDir() can sit inside a repo on some machines; only assert when it does not.
	if exec.Command("git", "rev-parse", "--show-toplevel").Run() == nil {
		t.Skip("temp dir is inside a git repo")
	}
	if got := ProjectRoot(); got != dir {
		t.Errorf("ProjectRoot() outside git = %q, want working dir %q", got, dir)
	}
}

func TestProjectRootEnvOverrideWins(t *testing.T) {
	root := newRepo(t)
	t.Chdir(root)
	t.Setenv("KGAI_PROJECT", "/somewhere/else")
	if got := ProjectRoot(); got != "/somewhere/else" {
		t.Errorf("ProjectRoot() = %q, want the KGAI_PROJECT override", got)
	}
}
