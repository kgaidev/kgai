package remote

import (
	"fmt"
	"os/exec"
	"strings"

	"kgai/internal/store"
)

// gitRemote syncs the store directory as a git repo: commit → fetch → union merge →
// push. Per-install shards are distinct files, so merges are conflict-free unions;
// never a rebase, never a force-push. With no remote configured it just commits
// locally (free local history). The url is the RESOLVED remote (local config or the
// global default) — never read from s.Config here.
type gitRemote struct{ url string }

func (g *gitRemote) Sync(s *store.Store) (SyncResult, error) {
	res := SyncResult{Remote: g.url}
	if err := g.commit(s, "kg sync"); err != nil {
		return res, err
	}
	if g.url == "" {
		res.Detail = "no remote configured; committed locally only"
		return res, nil
	}
	// Make sure 'origin' points at the configured remote.
	if _, err := git(s.Root, "remote", "get-url", "origin"); err != nil {
		if _, err := git(s.Root, "remote", "add", "origin", g.url); err != nil {
			return res, err
		}
	} else {
		_, _ = git(s.Root, "remote", "set-url", "origin", g.url)
	}
	branch := "main"
	if _, err := git(s.Root, "fetch", "-q", "origin"); err != nil {
		res.Detail = "fetch failed (offline?): " + err.Error()
		return res, nil // offline is not fatal — the local log is intact
	}
	// Merge the remote branch if it exists. --allow-unrelated-histories: each install
	// inits its own repo, so the first cross-install merge joins two roots.
	if _, err := git(s.Root, "rev-parse", "--verify", "-q", "origin/"+branch); err == nil {
		before, _ := git(s.Root, "rev-parse", "HEAD")
		if out, err := git(s.Root, "merge", "--no-edit", "--allow-unrelated-histories", "-m", "kg sync merge", "origin/"+branch); err != nil {
			return res, fmt.Errorf("merge conflict (unexpected for per-install shards): %s", out)
		}
		after, _ := git(s.Root, "rev-parse", "HEAD")
		res.Pulled = strings.TrimSpace(before) != strings.TrimSpace(after)
	}
	_, _ = git(s.Root, "branch", "-M", branch)
	if out, err := git(s.Root, "push", "-q", "-u", "origin", branch); err != nil {
		res.Detail = "push failed: " + strings.TrimSpace(out)
		return res, nil
	}
	res.Pushed = true
	return res, nil
}

// CommitsStoreDirectory: this transport commits the store directory, so its .gitignore
// is what keeps kg.config.json (the cloud token) out of what is sent.
func (g *gitRemote) CommitsStoreDirectory() bool { return true }

// commit stages and commits the log if there is anything to commit.
func (g *gitRemote) commit(s *store.Store, msg string) error {
	if _, err := git(s.Root, "add", "-A"); err != nil {
		return err
	}
	// Belt and braces on top of .gitignore: unstage the install-local files whatever the
	// ignore rules say. kg.config.json carries the cloud token, and a store whose ignore
	// file was replaced or mangled by a merge would otherwise commit it to a repo the
	// whole team pulls. --ignore-unmatch makes this a no-op when they are absent.
	if _, err := git(s.Root, "rm", "--cached", "-q", "--ignore-unmatch", "--",
		"kg.config.json", ".kg.lock", "last-autosync.json", ".autosync-stamp"); err != nil {
		return err
	}
	if out, _ := git(s.Root, "status", "--porcelain"); strings.TrimSpace(out) == "" {
		return nil // nothing staged
	}
	// Ensure an identity for the commit even on bare machines.
	actor := strings.TrimSpace(s.Config.Actor)
	if actor == "" {
		actor = "kgai"
	}
	_, _ = git(s.Root, "config", "user.name", actor)
	_, _ = git(s.Root, "config", "user.email", "kgai@local")
	_, err := git(s.Root, "commit", "-q", "-m", msg)
	return err
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
