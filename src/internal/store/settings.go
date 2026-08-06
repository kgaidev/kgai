package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Settings are the configuration keys that LAYER. The same shape is read from three
// files and resolved most-specific-first, so one key means the same thing wherever it
// is written:
//
//	session  <store>/kg.config.json   this install, never committed (holds the token)
//	project  <repo>/.kgairc           committed — the repo's default for everyone
//	global   <KgaiHome>/config.json   this machine, written only when asked for
//
// Precedence is session > project > global: the more specific layer wins, exactly as
// `git config` and npm resolve theirs. Every key overrides — nothing merges — so
// `kg config` can always name ONE layer as the source of a value.
//
// Identity (install_id, actor, machine, retired_installs) and the cloud token do NOT
// layer: they belong to one install, must never be inherited from a committed file,
// and stay in kg.config.json alone.
type Settings struct {
	// Remote is the sync target. "none" in a non-global layer opts that project out
	// of syncing even when a broader layer configures one.
	Remote string `json:"remote,omitempty"`
	// Prompt is the team's own capture rules, injected into the session in front of
	// the knowledge-graph skill. A repo commits its conventions here; a person keeps
	// personal ones in the global layer.
	Prompt string `json:"prompt,omitempty"`
	// CloudURL is the kgai cloud broker. The matching TOKEN is deliberately absent —
	// it is install-local (see Config) and must never reach a committed file.
	CloudURL string `json:"cloud_url,omitempty"`
	// StoreRoot moves the decision log somewhere other than <project>/.kgai/store —
	// the key that lets several repositories share one graph without every developer
	// exporting KGAI_STORE by hand. Supports ~ and ${VARS}, and resolves a relative
	// path against the repository root, so one committed value works on every machine.
	//
	// It is meaningless in the session layer: that file lives INSIDE the store it
	// would be pointing at, so only the project and global layers may set it.
	StoreRoot string `json:"store,omitempty"`
}

// Layer names, most specific first. The order of this slice IS the precedence.
const (
	LayerSession = "session"
	LayerProject = "project"
	LayerGlobal  = "global"
)

// ProjectConfigName is the committed, repo-level config, at the repository root the way
// npm keeps .npmrc — cloning a repo is all it takes to inherit its kgai defaults. The
// name follows the rc convention; the CONTENT is JSON, the same Settings shape as the
// other two layers, because one schema across layers is the point of this design.
const ProjectConfigName = ".kgairc"

// PromptMaxBytes caps the layered prompt. It is injected into EVERY session with the
// plugin enabled, so an unbounded value would quietly tax every conversation in the
// repo; a rule set that does not fit belongs in a document the rules point at.
const PromptMaxBytes = 8000

// SettingKeys are the layered keys, in the order `kg config` prints them. A key that
// is not here is rejected on write rather than stored and silently ignored later.
var SettingKeys = []string{"remote", "prompt", "cloud_url", "store"}

// keyLayers says where each key may be set, and it is enforced on BOTH paths: a write
// to the wrong layer is refused, and a value found in the wrong layer is ignored on
// read. Read-side enforcement is the load-bearing half — .kgairc is committed, so it
// arrives hand-written from whoever wrote the repository, never through `kg config`.
//
//	prompt     everywhere — the repo's capture rules are the reason .kgairc exists
//	store      project/global — the session file lives INSIDE the store it points at
//	remote     session/global — sync is a property of the STORE, not of one repo:
//	           several repos can share one log, and a per-repo remote would push that
//	           one log to several places. It also keeps a cloned file from choosing
//	           where a developer's decisions are uploaded.
//	cloud_url  session only — it is the address the install-local cloud TOKEN
//	           authenticates against, and the client reads it from there alone.
var keyLayers = map[string][]string{
	"prompt":    {LayerSession, LayerProject, LayerGlobal},
	"store":     {LayerProject, LayerGlobal},
	"remote":    {LayerSession, LayerGlobal},
	"cloud_url": {LayerSession},
}

// KeyAllowedIn reports whether a key may be set in a layer (see keyLayers).
func KeyAllowedIn(key, layer string) bool { return keyAllowedIn(key, layer) }

// ReadConfigFile reads one layer file without applying any layer rules — for `kg trust`,
// which must show what the file actually says, including the parts that will be ignored.
func ReadConfigFile(path string, into *Settings, exists *bool) error {
	return readSettings(path, into, exists)
}

func keyAllowedIn(key, layer string) bool {
	for _, l := range keyLayers[key] {
		if l == layer {
			return true
		}
	}
	return false
}

// ValidateLayerKey rejects a key in a layer where it cannot mean anything, at the point
// of writing rather than silently doing nothing when it is later read.
func ValidateLayerKey(layer, key string) error {
	if _, known := keyLayers[key]; !known {
		return unknownKey(key)
	}
	if keyAllowedIn(key, layer) {
		return nil
	}
	switch key {
	case "store":
		return fmt.Errorf("`store` cannot be set in the session layer — that file lives inside the store it would point at; use --project (committed, for everyone who clones the repo) or --global (this machine)")
	case "remote":
		return fmt.Errorf("`remote` cannot be set in the project layer — syncing belongs to the store, not to one repository (several repos can share one store, and one log cannot push to several remotes). Set it for this store with `kg remote <url>`, or machine-wide with `kg remote --global <url>`")
	case "cloud_url":
		return fmt.Errorf("`cloud_url` is install-local — it is the address your cloud token authenticates against, so it stays beside the token in this store's config. Set it with `kg config set cloud_url <url>` (session layer)")
	}
	return fmt.Errorf("`%s` cannot be set in the %s layer — allowed: %s", key, layer, strings.Join(keyLayers[key], ", "))
}

// Get reads one layered key. An unknown key is an error, never an empty string: a
// typo'd key must fail loudly at the CLI instead of resolving to "unset".
func (st *Settings) Get(key string) (string, error) {
	switch key {
	case "remote":
		return st.Remote, nil
	case "prompt":
		return st.Prompt, nil
	case "cloud_url":
		return st.CloudURL, nil
	case "store":
		return st.StoreRoot, nil
	}
	return "", unknownKey(key)
}

// Set writes one layered key. An empty value clears it (the field is omitempty, so a
// cleared key disappears from the file instead of persisting as "").
func (st *Settings) Set(key, val string) error {
	switch key {
	case "remote":
		st.Remote = val
	case "prompt":
		if len(val) > PromptMaxBytes {
			return fmt.Errorf("prompt is %d bytes, limit is %d: it is injected into every session, so keep it to the rules themselves and link out for detail", len(val), PromptMaxBytes)
		}
		st.Prompt = val
	case "cloud_url":
		st.CloudURL = val
	case "store":
		st.StoreRoot = val
	default:
		return unknownKey(key)
	}
	return nil
}

// StoreRootFromLayers resolves the configured store location from the layers that can
// be read WITHOUT a store — project first, then global. It reads the two files
// directly instead of going through LoadLayers, because LoadLayers needs to know where
// the store is to describe the session layer, and that would be a cycle.
//
// Errors are returned, never swallowed. Falling back to the per-project default on a
// corrupt or unapproved .kgairc would put decisions in a store nobody reads — and
// .kgairc is a committed file, so a conflicted merge is an ordinary way to get here.
func StoreRootFromLayers() (string, error) {
	project, projectTrusted, err := loadProjectLayer()
	if err != nil {
		return "", err
	}
	if project.StoreRoot != "" && projectTrusted {
		return ExpandStorePath(project.StoreRoot)
	}
	var global Settings
	var exists bool
	if err := readSettings(globalConfigPath(), &global, &exists); err != nil {
		return "", err
	}
	if global.StoreRoot != "" {
		return ExpandStorePath(global.StoreRoot)
	}
	return "", nil
}

// loadProjectLayer reads <repo>/.kgairc, drops any key that may not live there, and
// reports whether the file has been approved (see trust.go). An unapproved file parses
// but decides NOTHING: it arrives with a clone, from whoever wrote that repository.
func loadProjectLayer() (Settings, bool, error) {
	var st Settings
	var exists bool
	path := ProjectConfigPath()
	if err := readSettings(path, &st, &exists); err != nil {
		return Settings{}, false, err
	}
	if !exists {
		return st, true, nil
	}
	// Keys that may not be set here are ignored rather than obeyed — the write-side
	// check never ran on a hand-written file.
	for _, key := range SettingKeys {
		if !keyAllowedIn(key, LayerProject) {
			_ = st.Set(key, "")
		}
	}
	trusted, err := IsTrusted(path)
	if err != nil {
		return Settings{}, false, err
	}
	if !trusted {
		return Settings{}, false, nil
	}
	return st, true, nil
}

// ExpandStorePath resolves a configured store location and refuses the values that
// would turn a misconfiguration into damage. Every rejection here was a reproduced
// failure: an unset ${VAR} became "" and resolved to the repository root, where store
// init overwrote the project's own .gitignore and scattered the log through the working
// tree; ~ turned the home directory into a git repo; ../other-repo wrote into a
// neighbouring project.
func ExpandStorePath(p string) (string, error) {
	raw := p
	if p == "" {
		return "", fmt.Errorf("`store` is set to an empty value")
	}
	// Expand ${VAR}/$VAR ourselves so an unset one is an error instead of "": a
	// committed value naming a variable this machine does not have must say so.
	var missing []string
	p = os.Expand(p, func(name string) string {
		if v, ok := os.LookupEnv(name); ok {
			return v
		}
		missing = append(missing, name)
		return ""
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("`store` is %q but %s is not set on this machine — set it, or use a path that does not depend on the environment", raw, "$"+strings.Join(missing, ", $"))
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("`store` is %q but this machine has no home directory: %w", raw, err)
		}
		p = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
	}
	// A relative path is relative to the PROJECT ROOT, not to the working directory:
	// the same committed value must resolve to the same store from anywhere in the repo
	// ("../shared-kg" for sibling repositories is the case worth supporting).
	if !filepath.IsAbs(p) {
		p = filepath.Join(ProjectRoot(), p)
	}
	p = filepath.Clean(p)

	// Resolve symlinks before judging the path, so a link cannot point the checks at
	// one directory and the writes at another. A path that does not exist yet is fine
	// — that is the normal first run — so resolve the deepest existing ancestor.
	resolved := resolveExisting(p)
	proj := ProjectRoot()
	if home, err := os.UserHomeDir(); err == nil && resolved == filepath.Clean(home) {
		return "", fmt.Errorf("`store` resolves to your home directory (%s) — the store owns the directory it lives in (it git-inits it and writes .gitignore), so it needs one of its own", resolved)
	}
	if resolved == proj {
		return "", fmt.Errorf("`store` resolves to the repository root (%s) — the store owns the directory it lives in and would overwrite this repo's .gitignore; point it at a directory of its own", resolved)
	}
	if err := checkStoreDirUsable(resolved); err != nil {
		return "", err
	}
	return p, nil
}

// checkStoreDirUsable refuses a directory that already holds someone else's files. A
// store git-inits its root and writes .gitignore/.gitattributes there, so it may only
// take over a directory that is empty or already a kgai store.
func checkStoreDirUsable(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // does not exist yet (or unreadable) — Init reports that itself
	}
	if _, err := os.Stat(filepath.Join(dir, "kg.config.json")); err == nil {
		return nil // already a kgai store
	}
	for _, e := range entries {
		switch {
		// Only kgai's own unmistakable artifacts are tolerated in a directory that has
		// no kg.config.json (a half-finished init). NOT .gitignore/.gitattributes/.git:
		// those are exactly the files a real project keeps, and exactly the ones the
		// scaffold would overwrite.
		case e.Name() == "log", e.Name() == ".kg.lock",
			strings.HasPrefix(e.Name(), "graph.kuzu"):
			continue
		}
		return fmt.Errorf("`store` points at %s, which already contains other files (%s…) — the store writes .gitignore/.gitattributes and git-inits its root, so it needs an empty directory or an existing kgai store", dir, e.Name())
	}
	return nil
}

// resolveExisting resolves symlinks in the deepest part of the path that exists.
func resolveExisting(p string) string {
	for dir := p; ; {
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			if dir == p {
				return filepath.Clean(r)
			}
			return filepath.Clean(filepath.Join(r, strings.TrimPrefix(p, dir+string(os.PathSeparator))))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return p
		}
		dir = parent
	}
}

func unknownKey(key string) error {
	return fmt.Errorf("unknown setting %q — valid keys: %s", key, strings.Join(SettingKeys, ", "))
}

// Layer is one configuration file and what it currently holds.
type Layer struct {
	Name     string   `json:"layer"`
	Path     string   `json:"path"`
	Exists   bool     `json:"exists"`
	Settings Settings `json:"settings"`
	// Pending is set on the project layer when the file exists but has not been
	// approved on this machine: it is reported, and it decides nothing. Never silent
	// — an ignored config that nobody mentions is how a store ends up somewhere its
	// owner never looks.
	Pending bool `json:"pending_approval,omitempty"`
	// Ignored lists keys present in the file that this layer may not set, so
	// `kg config` can say why they had no effect instead of leaving a mystery.
	Ignored []string `json:"ignored_keys,omitempty"`
	// InheritedFrom names the repository this exact configuration was first approved
	// from, when this one is covered by that approval rather than its own. Reported
	// once so an inherited approval is visible rather than silent.
	InheritedFrom string `json:"approval_inherited_from,omitempty"`
}

// LoadLayers reads all three layers in precedence order (most specific first). The
// store may be nil — a read command must answer before anything is initialized — and
// the session layer is then simply absent. A corrupt file is an error: silently
// treating it as empty would push a project's decisions to the wrong remote, or drop
// the team's capture rules, with no sign that anything was wrong.
func LoadLayers(s *Store) ([]Layer, error) {
	session := Layer{Name: LayerSession}
	if s != nil {
		session.Path, session.Exists, session.Settings = s.configPath(), true, s.Config.Settings
	} else {
		session.Path = filepath.Join(DefaultRoot(), "kg.config.json")
	}

	project := Layer{Name: LayerProject, Path: ProjectConfigPath()}
	var onFile Settings
	if err := readSettings(project.Path, &onFile, &project.Exists); err != nil {
		return nil, err
	}
	if project.Exists {
		for _, key := range SettingKeys {
			if v, _ := onFile.Get(key); v != "" && !keyAllowedIn(key, LayerProject) {
				project.Ignored = append(project.Ignored, key)
			}
		}
		ts, err := TrustStateOf(project.Path)
		if err != nil {
			return nil, err
		}
		project.Pending = !ts.Trusted
		project.InheritedFrom = ts.InheritedFrom
		if ts.Trusted {
			project.Settings = onFile
			for _, key := range project.Ignored {
				_ = project.Settings.Set(key, "")
			}
		}
	}

	global := Layer{Name: LayerGlobal, Path: globalConfigPath()}
	if err := readSettings(global.Path, &global.Settings, &global.Exists); err != nil {
		return nil, err
	}
	// The session layer holds `store` only in a hand-edited or legacy file, where it
	// could never take effect (that file lives inside the store). Blank it so the
	// resolver and `kg config` cannot disagree about which layer decided.
	session.Settings.StoreRoot = ""

	return []Layer{session, project, global}, nil
}

func readSettings(path string, into *Settings, exists *bool) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, into); err != nil {
		return fmt.Errorf("corrupt %s: %w", path, err)
	}
	*exists = true
	return nil
}

// Effective resolves one key across the layers and names the layer it came from. An
// unset key returns ("", "") — the caller decides what "nothing configured" means.
func Effective(layers []Layer, key string) (val, source string) {
	for _, l := range layers {
		v, err := l.Settings.Get(key)
		if err == nil && v != "" {
			return v, l.Name
		}
	}
	return "", ""
}

// WriteLayer persists one key into one layer, creating the file if needed. The session
// layer is not written here: it lives inside the store, which owns its own locking and
// identity fields (see Store.SaveConfig).
func WriteLayer(name, path, key, val string) error {
	var st Settings
	var exists bool
	if err := readSettings(path, &st, &exists); err != nil {
		return err
	}
	if err := st.Set(key, val); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// ProjectConfigPath is the committed repo config: exactly <ProjectRoot()>/.kgairc, with
// no search. One project, one file, whatever directory the session happens to run in.
//
// Both halves of that are load-bearing, and each replaced a way the graph could split:
//
// NO WALK-UP from the working directory. A .kgairc in a subdirectory — hand-written, or
// carried by a vendored tree that has no .git of its own — used to win over the repo
// root's, so `kg ingest` from services/api wrote into a different store than the same
// command from the top. One repository, two graphs, decided by where the shell stood.
//
// ANCHORED AT ProjectRoot(), which maps a linked worktree back to the MAIN worktree —
// the same rule the store location follows, and they must agree. A worktree is a branch
// checked out somewhere else and the knowledge graph is branch-agnostic by design: read
// from the checked-out tree, a branch editing `store` would point that worktree at
// another graph. This holds for a worktree nested inside the repo (<repo>/.worktrees/x),
// which an earlier "is the working directory under the root?" test let through.
//
// So a change to .kgairc takes effect once it is merged and checked out in the main
// worktree, exactly as the store itself ignores branches.
func ProjectConfigPath() string {
	return filepath.Join(ProjectRoot(), ProjectConfigName)
}

// projectStart is where the search begins: the session's working directory, or
// KGAI_PROJECT when it is pinned explicitly.
func projectStart() string {
	if v := os.Getenv("KGAI_PROJECT"); v != "" {
		return v
	}
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}
