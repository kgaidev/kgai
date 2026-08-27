// Package view is the read model over a kgai store: the store resolved as kg would
// resolve it, the log replayed in memory, and every list, detail and tally a viewer
// shows — as plain data, so any front end (a desktop window, an editor extension)
// renders the same answers. It never opens the graph database or the store's lock.
package view

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"kgai/internal/replay"
	"kgai/internal/store"
)

// Model is one store as a viewer sees it: where it was resolved from, what the log
// projects to, and the tallies. It is immutable once loaded; a refresh builds a new one.
type Model struct {
	Dir    string // the folder the resolution is anchored at ("" = working directory)
	Root   string // store root
	Source string // what decided Root: "KGAI_STORE", "project", "global", or "" (default)
	// Err is a resolution or open problem (a refused `store` setting, a store that was
	// never initialized). The app shows it instead of a graph.
	Err error

	Store     *store.Store
	Pending   string // path of a committed .kgairc waiting for approval (decides nothing)
	Remote    string // effective sync remote, credentials stripped
	Transport string // none | s3 | kgai-cloud | git

	Proj   *replay.Projection
	Stats  replay.Stats
	Loaded time.Time

	fingerprint string
	people      map[replay.GroupBy][]replay.Person
	elements    []*replay.Element // sorted by kind, name, id
}

// Load resolves and reads the store anchored at dir. It never fails: a problem is
// carried in Err with an empty projection, so the window can say what is wrong.
func Load(dir string) *Model {
	m := &Model{Dir: dir, Proj: replay.New(), Loaded: time.Now()}
	root, source, err := store.ResolveRootIn(dir)
	if err != nil {
		m.Err = err
		return m
	}
	m.Root, m.Source = root, source
	m.fingerprint = logFingerprint(root)
	if layers, err := store.LoadLayersIn(nil, dir); err == nil {
		for _, l := range layers {
			if l.Pending {
				m.Pending = l.Path
			}
		}
	}
	s, err := store.Open(root)
	if err != nil {
		m.Err = err
		return m
	}
	m.Store = s
	remote, _ := s.EffectiveRemote()
	m.Remote, m.Transport = redactURL(remote), transportOf(remote)
	evs, err := s.ReadAll()
	if err != nil {
		m.Err = fmt.Errorf("reading the log: %w", err)
		return m
	}
	m.Proj = replay.Build(evs)
	m.Stats = m.Proj.Stats()
	return m
}

// logFingerprint is what the watcher compares: the log's file names, sizes and
// mtimes. Cheap (a directory listing), and every append or sync changes it.
func logFingerprint(root string) string {
	entries, err := os.ReadDir(filepath.Join(root, "log"))
	if err != nil {
		return "missing"
	}
	var parts []string
	for _, e := range entries {
		if fi, err := e.Info(); err == nil {
			parts = append(parts, fmt.Sprintf("%s:%d:%d", e.Name(), fi.Size(), fi.ModTime().UnixNano()))
		}
	}
	return strings.Join(parts, ";")
}

// Changed reports whether the log on disk differs from what this model was built from.
func (m *Model) Changed() bool {
	if m.Root == "" {
		return false
	}
	return logFingerprint(m.Root) != m.fingerprint
}

// Project is the project directory the store belongs to (for the title bar).
func (m *Model) Project() string {
	if m.Dir != "" {
		if abs, err := filepath.Abs(m.Dir); err == nil {
			return abs
		}
		return m.Dir
	}
	wd, _ := os.Getwd()
	return wd
}

// LocationRule says which rule chose the store, in a phrase a reader can act on.
func (m *Model) LocationRule() string {
	switch m.Source {
	case "":
		return "default location"
	case store.LayerProject:
		return "set by the project's .kgairc"
	case store.LayerGlobal:
		return "set by ~/.kgai/config.json"
	case "KGAI_STORE":
		return "set by KGAI_STORE"
	}
	return m.Source
}

// SyncLabel says whether and how this store is shared.
func (m *Model) SyncLabel() string {
	switch m.Transport {
	case "none":
		return "not synced (stays on this machine)"
	case "s3":
		return "synced via S3"
	case "kgai-cloud":
		return "synced via kgai cloud"
	}
	return "synced via " + m.Transport
}

// SourceLabel names what decided the store location, in kg's words.
func (m *Model) SourceLabel() string {
	switch m.Source {
	case "":
		return "default"
	case store.LayerProject:
		return ".kgairc"
	case store.LayerGlobal:
		return "config.json"
	}
	return m.Source
}

// People tallies the log per actor or author, computed once per model.
func (m *Model) People(by replay.GroupBy) []replay.Person {
	if m.people == nil {
		m.people = map[replay.GroupBy][]replay.Person{}
	}
	if _, ok := m.people[by]; !ok {
		m.people[by] = m.Proj.People(by, time.Now(), 25)
	}
	return m.people[by]
}

// Elements lists every element, kind then name then id — the one order the app uses.
func (m *Model) Elements() []*replay.Element {
	if m.elements == nil {
		for _, id := range replay.SortedKeys(m.Proj.Elements) {
			m.elements = append(m.elements, m.Proj.Elements[id])
		}
		sort.SliceStable(m.elements, func(i, j int) bool {
			a, b := m.elements[i], m.elements[j]
			if a.Kind != b.Kind {
				return a.Kind < b.Kind
			}
			if a.Name != b.Name {
				return a.Name < b.Name
			}
			return a.ID < b.ID
		})
	}
	return m.elements
}

// ---- decisions: the deterministic filter ------------------------------------------

// DecisionFilter is a conjunction of exact rules. Text is a case-insensitive substring
// (or a regular expression) over title, rationale and summary; the rest are exact
// matches or bounds. There is no ranking: results always come newest first, by
// recorded time, then lamport, then id.
type DecisionFilter struct {
	Text      string `json:"text,omitempty"`
	Regex     bool   `json:"regex,omitempty"`
	Actor     string `json:"actor,omitempty"`
	Author    string `json:"author,omitempty"`
	Kind      string `json:"kind,omitempty"`      // element kind the decision shaped
	Shard     string `json:"install,omitempty"`   // the install (log shard) that recorded it
	State     string `json:"state,omitempty"`     // "" | head | superseded | note
	From      string `json:"from,omitempty"`      // YYYY-MM-DD, inclusive
	To        string `json:"to,omitempty"`        // YYYY-MM-DD, inclusive
	ElementID string `json:"elementId,omitempty"` // exact element the decision shaped (set by navigation)
	Element   string `json:"element,omitempty"`   // element name substring
	Oldest    bool   `json:"oldest,omitempty"`    // oldest first instead of newest first
}

// Decisions applies the filter. A malformed regular expression matches nothing and is
// reported so the screen can say so.
func (m *Model) Decisions(f DecisionFilter) ([]*replay.Decision, error) {
	var re *regexp.Regexp
	text := strings.ToLower(f.Text)
	if f.Regex && f.Text != "" {
		var err error
		if re, err = regexp.Compile("(?i)" + f.Text); err != nil {
			return nil, err
		}
	}
	from, to := f.From, f.To
	if to != "" {
		to += "\xff" // inclusive: anything on that day sorts below
	}
	var out []*replay.Decision
	for _, id := range replay.SortedKeys(m.Proj.Decisions) {
		d := m.Proj.Decisions[id]
		if d.Stub {
			continue
		}
		if f.Actor != "" && d.Actor != f.Actor {
			continue
		}
		if f.Author != "" && d.Author != f.Author {
			continue
		}
		if f.Shard != "" && d.InstallID != f.Shard {
			continue
		}
		if from != "" && d.RecordedAt < from {
			continue
		}
		if to != "" && d.RecordedAt > to {
			continue
		}
		if text != "" {
			hay := d.Title + "\n" + d.Rationale + "\n" + d.Summary
			if re != nil {
				if !re.MatchString(hay) {
					continue
				}
			} else if !strings.Contains(strings.ToLower(hay), text) {
				continue
			}
		}
		if f.ElementID != "" || f.Kind != "" || f.Element != "" {
			hit := false
			for _, el := range m.Proj.ElementsOf(id) {
				e := m.Proj.Elements[el]
				if f.ElementID != "" && el != f.ElementID {
					continue
				}
				if f.Kind != "" && e.Kind != f.Kind {
					continue
				}
				if f.Element != "" && !strings.Contains(strings.ToLower(e.Name), strings.ToLower(f.Element)) {
					continue
				}
				hit = true
				break
			}
			if !hit {
				continue
			}
		}
		switch f.State {
		case "head":
			if !m.Proj.HeadAnywhere(id) {
				continue
			}
		case "superseded":
			if !m.Proj.HasAuthority(id) || m.Proj.HeadAnywhere(id) {
				continue
			}
		case "note":
			if m.Proj.HasAuthority(id) {
				continue
			}
		}
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.RecordedAt != b.RecordedAt {
			return (a.RecordedAt > b.RecordedAt) != f.Oldest
		}
		if a.Lamport != b.Lamport {
			return (a.Lamport > b.Lamport) != f.Oldest
		}
		return a.ID < b.ID
	})
	return out, nil
}

// Active reports whether any filter narrows the list.
func (f DecisionFilter) Active() bool {
	return f.Text != "" || f.Actor != "" || f.Author != "" || f.Kind != "" || f.Shard != "" ||
		f.State != "" || f.From != "" || f.To != "" || f.ElementID != "" || f.Element != ""
}

// State names a decision's standing, in the words the filter uses.
func (m *Model) State(id string) string {
	switch {
	case !m.Proj.HasAuthority(id):
		return "note"
	case m.Proj.HeadAnywhere(id):
		return "head"
	default:
		return "superseded"
	}
}

// ---- small helpers --------------------------------------------------------------------

func redactURL(u string) string {
	if u == "" {
		return ""
	}
	p, err := url.Parse(u)
	if err != nil || p.User == nil {
		return u
	}
	p.User = nil
	return p.String()
}

func transportOf(remote string) string {
	switch {
	case remote == "":
		return "none"
	case strings.HasPrefix(remote, "s3://"):
		return "s3"
	case strings.HasPrefix(remote, "kgai://"):
		return "kgai-cloud"
	}
	return "git"
}

func day(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02")
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
