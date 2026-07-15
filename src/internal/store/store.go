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

// Config is persisted as kg.config.json (install-local, gitignored — the cloud token
// therefore never leaves this machine via sync).
type Config struct {
	InstallID  string `json:"install_id"`
	Actor      string `json:"actor"`
	Remote     string `json:"remote,omitempty"`
	CloudURL   string `json:"cloud_url,omitempty"`
	CloudToken string `json:"cloud_token,omitempty"`
	SchemaVer  int    `json:"schema_version"`

	// Machine binds the installId to the machine it was minted on (see machine.go).
	// A store opened on a different machine is a COPY and rotates its identity.
	Machine string `json:"machine,omitempty"`
	// RetiredInstalls lists installIds this store previously wrote as. Their shards
	// are read-only history; sync reconciles them against the remote and re-records
	// any events the remote never received under the current identity.
	RetiredInstalls []string `json:"retired_installs,omitempty"`
}

// Store is a handle to an initialized KG store on disk.
type Store struct {
	Root   string
	Config Config
	lock   *os.File

	// tailHash caches the hash of the last event in THIS install's shard so batch
	// appends don't re-read the whole shard per event. Valid only while the write
	// lock is held (single writer); nil = not yet loaded.
	tailHash *string
}

const SchemaVersion = 1

// ProjectRoot resolves the current project: the git top-level of the working directory,
// or the working directory itself when it isn't a git repo. KGAI_PROJECT overrides.
func ProjectRoot() string {
	if v := os.Getenv("KGAI_PROJECT"); v != "" {
		return v
	}
	if out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output(); err == nil {
		if p := strings.TrimSpace(string(out)); p != "" {
			return p
		}
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// DefaultRoot resolves the store root. By default the KG is PER-PROJECT: it lives in
// <project>/.kgai/store, so each project has its own decision graph (with its own git/
// sync cycle, independent of the project's own repo). KGAI_STORE overrides. The engine
// binary + native lib still live in the shared ~/.kgai home (see KgaiHome).
func DefaultRoot() string {
	if v := os.Getenv("KGAI_STORE"); v != "" {
		return v
	}
	return filepath.Join(ProjectRoot(), ".kgai", "store")
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
	s.applyMachineBinding()
	if err := s.saveConfig(); err != nil {
		return nil, err
	}
	if err := s.ensureGitScaffold(); err != nil {
		return nil, err
	}
	// If this is the default per-project layout, keep the store out of the project's own
	// git — it has its own repo and push cycle.
	if proj := ProjectRoot(); s.Root == filepath.Join(proj, ".kgai", "store") {
		addProjectIgnore(proj)
	}
	return s, nil
}

// addProjectIgnore makes the project's own git ignore the per-project KG store dir.
func addProjectIgnore(proj string) {
	if _, err := os.Stat(filepath.Join(proj, ".git")); err != nil {
		return // not a git project → nothing to ignore
	}
	gi := filepath.Join(proj, ".gitignore")
	data, _ := os.ReadFile(gi)
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == ".kgai/" {
			return // already ignored
		}
	}
	f, err := os.OpenFile(gi, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	prefix := ""
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		prefix = "\n"
	}
	_, _ = f.WriteString(prefix + "# kgai per-project knowledge graph store (own git/sync cycle)\n.kgai/\n")
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
	// 0600: the config may hold the cloud token.
	return os.WriteFile(s.configPath(), append(b, '\n'), 0o600)
}

// SaveConfig persists config changes made after Init (e.g. cloud credentials).
func (s *Store) SaveConfig() error { return s.saveConfig() }

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
		// Local git history is a nicety (and required only by the git sync transport).
		// Environments without a git binary — e.g. a Lambda serving queries over an
		// s3:// remote — must still be able to run a store, so failure is non-fatal.
		if _, err := s.git("init", "-q"); err == nil {
			// union merge needs to be enabled in the repo's git config too.
			_, _ = s.git("config", "merge.union.driver", "git merge-file --union %A %O %B")
		}
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
	// Re-read the config under the lock (a concurrent process may have rotated the
	// identity between Open and Lock) and verify the machine binding before this
	// process writes anything as the possibly-stale installId.
	if b, err := os.ReadFile(s.configPath()); err == nil {
		var c Config
		if json.Unmarshal(b, &c) == nil && c.InstallID != "" {
			s.Config = c
			s.tailHash = nil
		}
	}
	if _, changed := s.applyMachineBinding(); changed {
		if err := s.saveConfig(); err != nil {
			s.Unlock()
			return err
		}
	}
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
	return s.ShardEvents(s.Config.InstallID)
}

// ShardEvents reads one install's shard (empty result if it doesn't exist yet).
func (s *Store) ShardEvents(installID string) ([]event.Event, error) {
	evs, err := readShard(filepath.Join(s.logDir(), installID+".ndjson"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return evs, err
}

// ShardCounts returns the number of events currently held per install. Object-store
// remotes derive push/pull plans from these counts (the protocol is stateless).
func (s *Store) ShardCounts() (map[string]int, error) {
	entries, err := os.ReadDir(s.logDir())
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ndjson") {
			continue
		}
		install := strings.TrimSuffix(e.Name(), ".ndjson")
		evs, err := s.ShardEvents(install)
		if err != nil {
			return nil, err
		}
		out[install] = len(evs)
	}
	return out, nil
}

// AppendForeign appends already-hashed events from another install to that install's
// local shard, enforcing the shard's hash chain: the first new event must reference
// the current local tail as its parent (or have no parent when the shard is empty).
// The caller must hold the write lock and must never use this for its own install.
func (s *Store) AppendForeign(installID string, evs []event.Event) error {
	if installID == s.Config.InstallID {
		return fmt.Errorf("refusing to append foreign events to own shard")
	}
	if len(evs) == 0 {
		return nil
	}
	existing, err := s.ShardEvents(installID)
	if err != nil {
		return err
	}
	prev := ""
	if len(existing) > 0 {
		prev = existing[len(existing)-1].Hash
	}
	for _, ev := range evs {
		got := ""
		if len(ev.Parents) > 0 {
			got = ev.Parents[0]
		}
		if got != prev {
			return fmt.Errorf("hash-chain break for install %s: event %s expects parent %q, local tail is %q", installID, ev.Hash, got, prev)
		}
		prev = ev.Hash
	}
	f, err := os.OpenFile(filepath.Join(s.logDir(), installID+".ndjson"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, ev := range evs {
		line, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			return err
		}
	}
	return nil
}

// RewriteShard atomically replaces one RETIRED/foreign shard file with the given
// events. Only sync's fork reconciliation uses this: when a retired shard diverged
// from the remote, the remote's version is the canon for that installId (its orphaned
// local events are re-recorded under the current identity via ReEmit).
func (s *Store) RewriteShard(installID string, evs []event.Event) error {
	if installID == s.Config.InstallID {
		return fmt.Errorf("refusing to rewrite own shard")
	}
	p := filepath.Join(s.logDir(), installID+".ndjson")
	tmp := p + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	for _, ev := range evs {
		line, err := json.Marshal(ev)
		if err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, p)
}

// ReEmit re-records the decisions carried by orphaned events (a retired shard's tail
// the remote never received) under THIS install's identity, with fresh lamports and
// a fresh hash chain. Decision ids are content-addressed, so a graph that already
// applied the originals converges to the same state. The caller holds the write lock.
func (s *Store) ReEmit(evs []event.Event) (int, error) {
	if len(evs) == 0 {
		return 0, nil
	}
	lam, err := s.NextLamport()
	if err != nil {
		return 0, err
	}
	for i, old := range evs {
		ev := event.Event{
			Op:         old.Op,
			Lamport:    lam + int64(i),
			RecordedAt: old.RecordedAt, // preserve the real decision time
			Decision:   old.Decision,
		}
		if err := s.Append(&ev); err != nil {
			return i, err
		}
	}
	return len(evs), nil
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
	if s.tailHash == nil {
		mine, err := s.MyShard()
		if err != nil {
			return err
		}
		tail := ""
		if len(mine) > 0 {
			tail = mine[len(mine)-1].Hash
		}
		s.tailHash = &tail
	}
	ev.InstallID = s.Config.InstallID
	ev.Actor = s.Config.Actor
	if *s.tailHash != "" {
		ev.Parents = []string{*s.tailHash}
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
	s.tailHash = &ev.Hash
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

// ---- local git scaffold ------------------------------------------------------
// (Sync transports live in internal/remote; the store only keeps the local repo
// scaffold so the log has free local history regardless of the configured remote.)

func (s *Store) git(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = s.Root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
