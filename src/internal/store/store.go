// Package store owns the on-disk KG store: the per-install append-only log shards,
// the config, write serialization (flock), and the git remote sync plumbing.
//
// Layout (rooted at the store dir):
//
//	.git/                       dedicated KG repo (its own push cycle)
//	log/<installId>.ndjson      this install's append-only shard (one writer)
//	kg.config.json              installId, actor, remote
//	.gitattributes              *.ndjson merge=union (belt-and-suspenders)
//	.gitignore                  graph.kuzu/ (derived cache, never committed)
//	graph.kuzu/                 materialized Kuzu/LadybugDB read-model (gitignored)
package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"kgai/internal/event"
)

// Config is persisted as kg.config.json.
type Config struct {
	InstallID    string `json:"install_id"`
	Actor        string `json:"actor"`
	Remote       string `json:"remote,omitempty"`
	SchemaVer    int    `json:"schema_version"`
}

// Store is a handle to an initialized KG store on disk.
type Store struct {
	Root   string
	Config Config
	lock   *os.File
}

const SchemaVersion = 1

// DefaultRoot resolves the store root. KGAI_STORE overrides; otherwise the store
// lives under the stable kgai home ($KGAI_HOME, default ~/.kgai), which is
// independent of plugin env vars (those are not reliably present in the Bash tool).
func DefaultRoot() string {
	if v := os.Getenv("KGAI_STORE"); v != "" {
		return v
	}
	return filepath.Join(KgaiHome(), "store")
}

// KgaiHome is the stable runtime/store home for kgai.
func KgaiHome() string {
	if v := os.Getenv("KGAI_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".kgai")
}

func (s *Store) logDir() string      { return filepath.Join(s.Root, "log") }
func (s *Store) shardPath() string   { return filepath.Join(s.logDir(), s.Config.InstallID+".ndjson") }
func (s *Store) configPath() string  { return filepath.Join(s.Root, "kg.config.json") }
func (s *Store) GraphPath() string   { return filepath.Join(s.Root, "graph.kuzu") }
func (s *Store) lockPath() string    { return filepath.Join(s.Root, ".kg.lock") }

// Init creates a new store (idempotent if already initialized).
func Init(root, actor, remote string) (*Store, error) {
	if root == "" {
		root = DefaultRoot()
	}
	s := &Store{Root: root}
	if err := os.MkdirAll(filepath.Join(root, "log"), 0o755); err != nil {
		return nil, err
	}
	// Load or create config.
	if b, err := os.ReadFile(filepath.Join(root, "kg.config.json")); err == nil {
		if err := json.Unmarshal(b, &s.Config); err != nil {
			return nil, fmt.Errorf("corrupt kg.config.json: %w", err)
		}
	} else {
		s.Config = Config{
			InstallID: "i" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16],
			Actor:     actor,
			SchemaVer: SchemaVersion,
		}
	}
	if actor != "" {
		s.Config.Actor = actor
	}
	if s.Config.Actor == "" {
		s.Config.Actor = guessActor()
	}
	if remote != "" {
		s.Config.Remote = remote
	}
	if s.Config.SchemaVer == 0 {
		s.Config.SchemaVer = SchemaVersion
	}
	if err := s.saveConfig(); err != nil {
		return nil, err
	}
	if err := s.ensureGitScaffold(); err != nil {
		return nil, err
	}
	return s, nil
}

// Open loads an existing store. Returns an error if not initialized.
func Open(root string) (*Store, error) {
	if root == "" {
		root = DefaultRoot()
	}
	s := &Store{Root: root}
	b, err := os.ReadFile(filepath.Join(root, "kg.config.json"))
	if err != nil {
		return nil, fmt.Errorf("store not initialized at %s (run `kg init`): %w", root, err)
	}
	if err := json.Unmarshal(b, &s.Config); err != nil {
		return nil, fmt.Errorf("corrupt kg.config.json: %w", err)
	}
	return s, nil
}

func (s *Store) saveConfig() error {
	b, _ := json.MarshalIndent(s.Config, "", "  ")
	return os.WriteFile(s.configPath(), append(b, '\n'), 0o644)
}

func (s *Store) ensureGitScaffold() error {
	// .gitignore: derived graph cache, native libs, lock, and the install-local
	// config (its installId/actor differ per install and must NOT be shared, or
	// clones would conflict on it during merge).
	// graph.kuzu may be a file or a directory depending on engine version; match
	// both plus its wal/shadow files. *.so = downloaded native libs.
	gi := "graph.kuzu*\n*.so\n.kg.lock\nkg.config.json\n"
	if err := os.WriteFile(filepath.Join(s.Root, ".gitignore"), []byte(gi), 0o644); err != nil {
		return err
	}
	// union merge driver for ndjson as a safety net (shards are per-install anyway).
	ga := "*.ndjson merge=union\n"
	if err := os.WriteFile(filepath.Join(s.Root, ".gitattributes"), []byte(ga), 0o644); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(s.Root, ".git")); errors.Is(err, os.ErrNotExist) {
		if _, err := s.git("init", "-q"); err != nil {
			return fmt.Errorf("git init: %w", err)
		}
		// union merge needs to be enabled in the repo's git config too.
		_, _ = s.git("config", "merge.union.driver", "git merge-file --union %A %O %B")
	}
	return nil
}

// Lock acquires an exclusive advisory lock serializing all writers (record/sync/
// rebuild) so concurrent sessions never corrupt the shard or the graph cache.
func (s *Store) Lock() error {
	f, err := os.OpenFile(s.lockPath(), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return err
	}
	s.lock = f
	return nil
}

// Unlock releases the write lock.
func (s *Store) Unlock() {
	if s.lock != nil {
		_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		_ = s.lock.Close()
		s.lock = nil
	}
}

// ReadAll reads every event from every shard, unsorted.
func (s *Store) ReadAll() ([]event.Event, error) {
	entries, err := os.ReadDir(s.logDir())
	if err != nil {
		return nil, err
	}
	var out []event.Event
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ndjson") {
			continue
		}
		evs, err := readShard(filepath.Join(s.logDir(), e.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading shard %s: %w", e.Name(), err)
		}
		out = append(out, evs...)
	}
	return out, nil
}

func readShard(path string) ([]event.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []event.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev event.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			// One malformed line (e.g. a corrupt line in a teammate's synced shard)
			// must not break every read and rebuild for everyone. Skip it; `kg doctor`
			// surfaces hash problems, and replay re-verifies each event anyway.
			continue
		}
		out = append(out, ev)
	}
	return out, sc.Err()
}

// MyShard reads only this install's shard (for hash-chain checks and parents).
func (s *Store) MyShard() ([]event.Event, error) {
	evs, err := readShard(s.shardPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return evs, err
}

// NextLamport returns one past the maximum lamport observed across all shards.
func (s *Store) NextLamport() (int64, error) {
	all, err := s.ReadAll()
	if err != nil {
		return 0, err
	}
	var maxLam int64
	for _, e := range all {
		if e.Lamport > maxLam {
			maxLam = e.Lamport
		}
	}
	return maxLam + 1, nil
}

// Append finalizes (parents + hash) and appends an event to this install's shard.
// The caller must hold the write lock.
func (s *Store) Append(ev *event.Event) error {
	mine, err := s.MyShard()
	if err != nil {
		return err
	}
	ev.InstallID = s.Config.InstallID
	ev.Actor = s.Config.Actor
	if len(mine) > 0 {
		ev.Parents = []string{mine[len(mine)-1].Hash}
	}
	if ev.RecordedAt == "" {
		ev.RecordedAt = time.Now().UTC().Format(time.RFC3339)
	}
	ev.ComputeHash()
	line, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.shardPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	return nil
}

// SortEvents orders events into the canonical deterministic total order used for
// replay: by lamport, then by hash as a stable tie-break (hash embeds the actor).
func SortEvents(evs []event.Event) {
	sort.SliceStable(evs, func(i, j int) bool {
		if evs[i].Lamport != evs[j].Lamport {
			return evs[i].Lamport < evs[j].Lamport
		}
		return evs[i].Hash < evs[j].Hash
	})
}

func guessActor() string {
	if v := os.Getenv("KGAI_ACTOR"); v != "" {
		return v
	}
	if out, err := exec.Command("git", "config", "user.name").Output(); err == nil {
		if n := strings.TrimSpace(string(out)); n != "" {
			return n
		}
	}
	if v := os.Getenv("USER"); v != "" {
		return v
	}
	return "unknown"
}

// ---- git -------------------------------------------------------------------

func (s *Store) git(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = s.Root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Commit stages and commits the log if there is anything to commit.
func (s *Store) Commit(msg string) error {
	if _, err := s.git("add", "-A"); err != nil {
		return err
	}
	// Nothing staged → no-op (git commit would fail).
	if out, _ := s.git("status", "--porcelain"); strings.TrimSpace(out) == "" {
		return nil
	}
	// Ensure identity for the commit even on bare machines.
	_, _ = s.git("config", "user.name", nonEmpty(s.Config.Actor, "kgai"))
	_, _ = s.git("config", "user.email", "kgai@local")
	_, err := s.git("commit", "-q", "-m", msg)
	return err
}

// SyncResult summarizes a sync run.
type SyncResult struct {
	Pulled   bool   `json:"pulled"`
	Pushed   bool   `json:"pushed"`
	Remote   string `json:"remote,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// PullPush performs the remote half of a sync: commit, fetch + fast-forward/union
// merge (NEVER rebase, NEVER force), then push. Returns whether new events arrived.
func (s *Store) PullPush(msg string) (SyncResult, error) {
	res := SyncResult{Remote: s.Config.Remote}
	if err := s.Commit(msg); err != nil {
		return res, err
	}
	if s.Config.Remote == "" {
		res.Detail = "no remote configured; committed locally only"
		return res, nil
	}
	// Make sure 'origin' points at the configured remote.
	if _, err := s.git("remote", "get-url", "origin"); err != nil {
		if _, err := s.git("remote", "add", "origin", s.Config.Remote); err != nil {
			return res, err
		}
	} else {
		_, _ = s.git("remote", "set-url", "origin", s.Config.Remote)
	}
	branch := "main"
	if _, err := s.git("fetch", "-q", "origin"); err != nil {
		res.Detail = "fetch failed (offline?): " + err.Error()
		return res, nil // offline is not fatal — local log is intact
	}
	// Merge remote branch if it exists. --no-rebase keeps append-only history.
	if _, err := s.git("rev-parse", "--verify", "-q", "origin/"+branch); err == nil {
		before, _ := s.git("rev-parse", "HEAD")
		// --allow-unrelated-histories: each install inits its own repo, so the first
		// cross-install merge joins two roots. Per-install shards are distinct files,
		// so this is still a conflict-free union, never a rebase or force.
		if out, err := s.git("merge", "--no-edit", "--allow-unrelated-histories", "-m", "kg sync merge", "origin/"+branch); err != nil {
			return res, fmt.Errorf("merge conflict (unexpected for per-install shards): %s", out)
		}
		after, _ := s.git("rev-parse", "HEAD")
		res.Pulled = strings.TrimSpace(before) != strings.TrimSpace(after)
	}
	// Ensure branch name then push.
	_, _ = s.git("branch", "-M", branch)
	if out, err := s.git("push", "-q", "-u", "origin", branch); err != nil {
		res.Detail = "push failed: " + strings.TrimSpace(out)
		return res, nil
	}
	res.Pushed = true
	return res, nil
}

func nonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
