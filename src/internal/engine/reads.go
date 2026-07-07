package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"kgai/internal/graph"
	"kgai/internal/store"
)

func removeAll(p string) error { return os.RemoveAll(p) }

// ---- conflicts -------------------------------------------------------------

type ConflictGroup struct {
	ElementID string   `json:"element_id"`
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Heads     []string `json:"heads"`  // competing decision ids
	Titles    []string `json:"titles"` // their titles
}

// Conflicts lists elements shaped by more than one authoritative (head) decision —
// i.e. two people changed the same element concurrently without one superseding the
// other. about, when set, filters by element-name substring.
func (e *Engine) Conflicts(about string) ([]ConflictGroup, error) {
	g, err := e.openRead()
	if err != nil {
		return nil, err
	}
	defer g.Close()
	return e.conflictsOn(g, about)
}

func (e *Engine) conflictsLocked() ([]ConflictGroup, error) {
	g, err := e.openWrite()
	if err != nil {
		return nil, err
	}
	defer g.Close()
	return e.conflictsOn(g, "")
}

func (e *Engine) conflictsOn(g *graph.Graph, about string) ([]ConflictGroup, error) {
	rows, err := g.Raw(`MATCH (d:Decision)-[s:SHAPES]->(e:Element)
		WHERE s.authority = true
		  AND NOT EXISTS { MATCH (d2:Decision)-[:SUPERSEDES]->(d), (d2)-[s2:SHAPES]->(e) WHERE s2.authority = true }
		WITH e, collect(d.id) AS heads, collect(d.title) AS titles
		WHERE size(heads) > 1
		RETURN e.id AS eid, e.name AS name, e.kind AS kind, heads, titles`)
	if err != nil {
		return nil, err
	}
	var out []ConflictGroup
	for _, r := range rows {
		cg := ConflictGroup{
			ElementID: asStr(r["eid"]), Name: asStr(r["name"]), Kind: asStr(r["kind"]),
			Heads: asStrSlice(r["heads"]), Titles: asStrSlice(r["titles"]),
		}
		if about != "" && !strings.Contains(strings.ToLower(cg.Name), strings.ToLower(about)) {
			continue
		}
		out = append(out, cg)
	}
	return out, nil
}

// ---- history ---------------------------------------------------------------

type HistoryDecision struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Rationale  string `json:"rationale,omitempty"`
	Author     string `json:"author"`
	When       string `json:"when"`
	Lamport    int64  `json:"lamport"`
	Mutation   string `json:"mutation,omitempty"`
	IsHead     bool   `json:"is_head"`
}

type HistoryResult struct {
	ElementID string            `json:"element_id"`
	Name      string            `json:"name"`
	Kind      string            `json:"kind"`
	Decisions []HistoryDecision `json:"decisions"`
}

// History returns the full chain of decisions that shaped an element, oldest first —
// the on-demand evolution of that element, with the why of each change.
func (e *Engine) History(token string) (HistoryResult, error) {
	g, err := e.openRead()
	if err != nil {
		return HistoryResult{}, err
	}
	defer g.Close()
	eid := e.resolveElementID(g, token)
	rows, err := g.Raw(`MATCH (d:Decision)-[:SHAPES]->(e:Element {id:'` + esc(eid) + `'})
		RETURN e.name AS name, e.kind AS kind, d.id AS id, d.title AS title,
		  d.rationale AS rationale, d.author AS author, d.recorded_at AS recorded,
		  d.lamport AS lamport, d.summary AS mutation
		ORDER BY d.lamport`)
	if err != nil {
		return HistoryResult{}, err
	}
	res := HistoryResult{ElementID: eid}
	heads := map[string]bool{}
	for _, h := range e.headDecisions(g, eid) {
		heads[h] = true
	}
	for _, r := range rows {
		res.Name, res.Kind = asStr(r["name"]), asStr(r["kind"])
		hd := HistoryDecision{
			ID: asStr(r["id"]), Title: asStr(r["title"]), Rationale: asStr(r["rationale"]),
			Author: asStr(r["author"]), When: fmtTime(r["recorded"]), Lamport: asInt(r["lamport"]),
			Mutation: asStr(r["mutation"]),
		}
		hd.IsHead = heads[hd.ID]
		res.Decisions = append(res.Decisions, hd)
	}
	if len(res.Decisions) == 0 {
		return res, fmt.Errorf("no decisions touch %q (element %s)", token, eid)
	}
	return res, nil
}

func (e *Engine) resolveElementID(g *graph.Graph, token string) string {
	token = strings.TrimSpace(token)
	if rows, _ := g.Raw(`MATCH (n:Element {id:'` + esc(token) + `'}) RETURN n.id`); len(rows) > 0 {
		return token
	}
	id, _, _ := e.resolveElementRef(token, nil)
	return id
}

// ---- context ---------------------------------------------------------------

type ContextLink struct {
	Dir      string `json:"dir"`  // "out" | "in"
	Kind     string `json:"kind"`
	Neighbor string `json:"neighbor"`
}

type ContextItem struct {
	ElementID string            `json:"element_id"`
	Name      string            `json:"name"`
	Kind      string            `json:"kind"`
	Props     map[string]string `json:"props,omitempty"`
	Links     []ContextLink     `json:"links,omitempty"`
	Why       []ContextWhy      `json:"why,omitempty"` // decisions shaping this element, newest first
	Score     float64           `json:"score"`
}

type ContextWhy struct {
	Title     string `json:"title"`
	Rationale string `json:"rationale,omitempty"`
	When      string `json:"when"`
	IsHead    bool   `json:"is_head"`
}

type ContextResult struct {
	Items   []ContextItem `json:"items"`
	Shown   int           `json:"shown"`
	Omitted int           `json:"omitted"`
	Total   int           `json:"total"`
	Note    string        `json:"note,omitempty"`
}

type ContextQuery struct {
	Paths []string
	About string
	Max   int
}

// Context returns the relevant slice of the element graph for the work at hand: each
// matched element with its current links and the decisions that shaped it (the why).
// The live graph is small, so it loads fully and ranks in-process.
func (e *Engine) Context(q ContextQuery) (ContextResult, error) {
	if q.Max <= 0 {
		q.Max = 15
	}
	g, err := e.openRead()
	if err != nil {
		return ContextResult{}, err
	}
	defer g.Close()

	elems, err := g.Raw(`MATCH (e:Element) RETURN e.id AS id, e.kind AS kind, e.name AS name, e.props AS props`)
	if err != nil {
		return ContextResult{}, err
	}
	links, _ := g.Raw(`MATCH (a:Element)-[r:LINK]->(b:Element) RETURN a.id AS f, r.kind AS k, b.id AS t, b.name AS tn, a.name AS fn`)
	shapes, _ := g.Raw(`MATCH (d:Decision)-[:SHAPES]->(e:Element)
		RETURN e.id AS eid, d.id AS did, d.title AS title, d.rationale AS rationale,
		  d.recorded_at AS recorded, d.lamport AS lamport ORDER BY d.lamport DESC`)

	// index links + decisions by element
	outL := map[string][]ContextLink{}
	inL := map[string][]ContextLink{}
	for _, r := range links {
		outL[asStr(r["f"])] = append(outL[asStr(r["f"])], ContextLink{Dir: "out", Kind: asStr(r["k"]), Neighbor: asStr(r["tn"])})
		inL[asStr(r["t"])] = append(inL[asStr(r["t"])], ContextLink{Dir: "in", Kind: asStr(r["k"]), Neighbor: asStr(r["fn"])})
	}
	type decRow struct {
		title, rationale, when string
		lamport                int64
	}
	byEl := map[string][]decRow{}
	maxLam := map[string]int64{}
	for _, r := range shapes {
		eid := asStr(r["eid"])
		byEl[eid] = append(byEl[eid], decRow{asStr(r["title"]), asStr(r["rationale"]), fmtTime(r["recorded"]), asInt(r["lamport"])})
		if asInt(r["lamport"]) > maxLam[eid] {
			maxLam[eid] = asInt(r["lamport"])
		}
	}

	filtered := len(q.Paths) > 0 || q.About != ""
	var items []ContextItem
	for _, r := range elems {
		eid := asStr(r["id"])
		props := parseProps(asStr(r["props"]))
		it := ContextItem{ElementID: eid, Name: asStr(r["name"]), Kind: asStr(r["kind"]), Props: props}
		it.Links = append(outL[eid], inL[eid]...)
		score := 0.0
		// path overlap against the element's "paths" prop
		for _, qp := range q.Paths {
			for _, ap := range splitList(props["paths"]) {
				if globMatch(ap, qp) {
					score += 10
					break
				}
			}
		}
		if q.About != "" {
			needle := strings.ToLower(q.About)
			if strings.Contains(strings.ToLower(it.Name), needle) || strings.Contains(strings.ToLower(it.Kind), needle) {
				score += 6
			}
		}
		score += 0.001 * float64(maxLam[eid]) // recency tiebreak
		if filtered && score < 1 {
			continue
		}
		// attach decisions (newest first), mark the head (first)
		for i, d := range byEl[eid] {
			it.Why = append(it.Why, ContextWhy{Title: d.title, Rationale: oneLine(d.rationale), When: d.when, IsHead: i == 0})
		}
		it.Score = score
		items = append(items, it)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Score > items[j].Score })

	res := ContextResult{Total: len(items), Items: []ContextItem{}}
	if len(items) > q.Max {
		res.Items = items[:q.Max]
		res.Omitted = len(items) - q.Max
	} else if items != nil {
		res.Items = items
	}
	res.Shown = len(res.Items)
	if res.Omitted > 0 {
		res.Note = fmt.Sprintf("%d more element(s) omitted — narrow with --paths/--about or raise --max", res.Omitted)
	}
	if !filtered {
		res.Note = strings.TrimSpace("no filters — showing whole element graph. " + res.Note)
	}
	return res, nil
}

// ---- as-of (time travel) ---------------------------------------------------

type AsOfLink struct {
	From string `json:"from"`
	Kind string `json:"kind"`
	To   string `json:"to"`
}

type AsOfResult struct {
	At       string     `json:"at"`
	Elements []string   `json:"elements"`
	Links    []AsOfLink `json:"links"`
}

// AsOf reconstructs the exact element-graph structure effective at time ts by
// replaying every decision recorded at or before ts into an ephemeral graph.
func (e *Engine) AsOf(ts string) (AsOfResult, error) {
	cut, perr := time.Parse(time.RFC3339, ts)
	if perr != nil {
		if cut, perr = time.Parse("2006-01-02", ts); perr != nil {
			return AsOfResult{}, fmt.Errorf("invalid timestamp %q (use RFC3339 or YYYY-MM-DD)", ts)
		}
	}
	all, err := e.S.ReadAll()
	if err != nil {
		return AsOfResult{}, err
	}
	store.SortEvents(all)

	tmp, err := os.MkdirTemp("", "kg-asof-*")
	if err != nil {
		return AsOfResult{}, err
	}
	defer os.RemoveAll(tmp)
	g, err := graph.Open(filepath.Join(tmp, "g.kuzu"), false)
	if err != nil {
		return AsOfResult{}, err
	}
	defer g.Close()
	if err := g.EnsureSchema(); err != nil {
		return AsOfResult{}, err
	}
	for _, ev := range all {
		rt, perr := time.Parse(time.RFC3339, ev.RecordedAt)
		if perr != nil || rt.After(cut.UTC()) {
			continue // unparseable timestamp ⇒ excluded, not silently always-included
		}
		if !ev.Verify() {
			continue
		}
		if err := g.ApplyEvent(ev); err != nil {
			return AsOfResult{}, err
		}
	}
	out := AsOfResult{At: cut.UTC().Format(time.RFC3339)}
	er, _ := g.Raw(`MATCH (e:Element) RETURN e.name AS n ORDER BY n`)
	for _, r := range er {
		out.Elements = append(out.Elements, asStr(r["n"]))
	}
	lr, _ := g.Raw(`MATCH (a:Element)-[r:LINK]->(b:Element) RETURN a.name AS f, r.kind AS k, b.name AS t ORDER BY f,k,t`)
	for _, r := range lr {
		out.Links = append(out.Links, AsOfLink{From: asStr(r["f"]), Kind: asStr(r["k"]), To: asStr(r["t"])})
	}
	return out, nil
}

// ---- search ----------------------------------------------------------------

type SearchHit struct {
	Kind  string `json:"kind"` // "element" | "decision"
	ID    string `json:"id"`
	Name  string `json:"name"`
	Extra string `json:"extra,omitempty"`
}

// Search does a substring match over element names and decision titles/rationales.
func (e *Engine) Search(text string, limit int) ([]SearchHit, error) {
	if limit <= 0 {
		limit = 20
	}
	g, err := e.openRead()
	if err != nil {
		return nil, err
	}
	defer g.Close()
	needle := strings.ToLower(strings.TrimSpace(text))
	var hits []SearchHit
	er, _ := g.Raw(`MATCH (e:Element) RETURN e.id AS id, e.kind AS kind, e.name AS name`)
	for _, r := range er {
		if needle == "" || strings.Contains(strings.ToLower(asStr(r["name"])), needle) {
			hits = append(hits, SearchHit{Kind: "element", ID: asStr(r["id"]), Name: asStr(r["name"]), Extra: asStr(r["kind"])})
		}
	}
	dr, _ := g.Raw(`MATCH (d:Decision) RETURN d.id AS id, d.title AS title, d.rationale AS rationale`)
	for _, r := range dr {
		hay := strings.ToLower(asStr(r["title"]) + " " + asStr(r["rationale"]))
		if needle == "" || strings.Contains(hay, needle) {
			hits = append(hits, SearchHit{Kind: "decision", ID: asStr(r["id"]), Name: asStr(r["title"]), Extra: oneLine(asStr(r["rationale"]))})
		}
		if len(hits) >= limit {
			break
		}
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// ---- raw query / resolve ---------------------------------------------------

func (e *Engine) Query(cypher string) ([]map[string]any, error) {
	g, err := e.openRead()
	if err != nil {
		return nil, err
	}
	defer g.Close()
	rows, err := g.Raw(cypher)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		for k, v := range r {
			if t, ok := v.(time.Time); ok {
				r[k] = t.UTC().Format(time.RFC3339)
			}
		}
	}
	return rows, nil
}

type ResolveResult struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	ElementID string   `json:"element_id"`
	Existed   bool     `json:"existed"`
	Heads     []string `json:"head_decisions,omitempty"`
}

// ResolveName reports the deterministic element id for a (kind,name), whether it
// already exists, and the decisions currently authoritative over it.
func (e *Engine) ResolveName(token string) (ResolveResult, error) {
	g, err := e.openRead()
	if err != nil {
		return ResolveResult{}, err
	}
	defer g.Close()
	id, kind, name := e.resolveElementRef(token, nil)
	existed := false
	if rows, _ := g.Raw(`MATCH (n:Element {id:'` + esc(id) + `'}) RETURN n.id`); len(rows) > 0 {
		existed = true
	}
	return ResolveResult{Name: name, Kind: kind, ElementID: id, Existed: existed, Heads: e.headDecisions(g, id)}, nil
}

// ---- canonical export ------------------------------------------------------

type ExportResult struct {
	Elements  []map[string]any `json:"elements"`
	Links     []map[string]any `json:"links"`
	Decisions []map[string]any `json:"decisions"`
	Shapes    []map[string]any `json:"shapes"`
	Digest    string           `json:"digest,omitempty"`
}

// Export is a deterministic, sorted dump. Two stores that replayed the same events
// produce identical exports (and digests) — the replay-determinism check.
func (e *Engine) Export(canonical bool) (ExportResult, error) {
	g, err := e.openRead()
	if err != nil {
		return ExportResult{}, err
	}
	defer g.Close()
	var out ExportResult
	out.Elements, _ = g.Raw(`MATCH (e:Element) RETURN e.id AS id, e.kind AS kind, e.name AS name, e.props AS props ORDER BY e.id`)
	out.Decisions, _ = g.Raw(`MATCH (d:Decision) RETURN d.id AS id, d.title AS title, d.lamport AS lamport ORDER BY d.id`)
	out.Links, _ = g.Raw(`MATCH (a:Element)-[r:LINK]->(b:Element) RETURN a.id AS f, r.kind AS k, b.id AS t ORDER BY f,k,t`)
	out.Shapes, _ = g.Raw(`MATCH (d:Decision)-[:SHAPES]->(e:Element) RETURN d.id AS d, e.id AS e ORDER BY d,e`)
	if canonical {
		b, _ := json.Marshal(out)
		sum := sha256.Sum256(b)
		out.Digest = "sha256:" + hex.EncodeToString(sum[:])
	}
	return out, nil
}

// ---- doctor ----------------------------------------------------------------

type DoctorReport struct {
	Root          string   `json:"root"`
	InstallID     string   `json:"install_id"`
	Actor         string   `json:"actor"`
	Remote        string   `json:"remote,omitempty"`
	SchemaVersion int      `json:"schema_version"`
	Events        int      `json:"events"`
	Elements      int      `json:"elements"`
	Decisions     int      `json:"decisions"`
	Conflicts     int      `json:"conflicts"`
	Problems      []string `json:"problems"`
	OK            bool     `json:"ok"`
}

func (e *Engine) Doctor() (DoctorReport, error) {
	rep := DoctorReport{
		Root: e.S.Root, InstallID: e.S.Config.InstallID, Actor: e.S.Config.Actor,
		Remote: redactURL(e.S.Config.Remote), SchemaVersion: e.S.Config.SchemaVer,
	}
	all, err := e.S.ReadAll()
	if err != nil {
		return rep, err
	}
	rep.Events = len(all)
	for _, ev := range all {
		if !ev.Verify() {
			rep.Problems = append(rep.Problems, "bad content hash: "+ev.Hash)
		}
	}
	mine, _ := e.S.MyShard()
	var prev string
	for i, ev := range mine {
		if i > 0 && (len(ev.Parents) == 0 || ev.Parents[0] != prev) {
			rep.Problems = append(rep.Problems, fmt.Sprintf("broken hash-chain at lamport %d", ev.Lamport))
		}
		prev = ev.Hash
	}
	if g, err := e.openRead(); err == nil {
		if r, _ := g.Raw(`MATCH (e:Element) RETURN count(e) AS c`); len(r) > 0 {
			rep.Elements = int(asInt(r[0]["c"]))
		}
		if r, _ := g.Raw(`MATCH (d:Decision) RETURN count(d) AS c`); len(r) > 0 {
			rep.Decisions = int(asInt(r[0]["c"]))
		}
		g.Close()
	}
	if conf, err := e.Conflicts(""); err == nil {
		rep.Conflicts = len(conf)
	}
	rep.OK = len(rep.Problems) == 0
	return rep, nil
}

// ---- small helpers ---------------------------------------------------------

func asStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", x)
	}
}

func asInt(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	default:
		return 0
	}
}

func asStrSlice(v any) []string {
	switch x := v.(type) {
	case []any:
		out := make([]string, 0, len(x))
		for _, it := range x {
			if s := asStr(it); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return x
	default:
		return nil
	}
}

func parseProps(blob string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(blob, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		kv := strings.SplitN(line, "=", 2)
		if len(kv) == 2 {
			out[kv[0]] = kv[1]
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// redactURL strips any embedded credentials from a remote URL so a token in the git
// remote never lands in JSON that gets piped into the AI's context/transcript.
func redactURL(u string) string {
	at := strings.LastIndex(u, "@")
	scheme := strings.Index(u, "://")
	if at > 0 && scheme > 0 && at > scheme {
		return u[:scheme+3] + "***@" + u[at+1:]
	}
	return u
}

func oneLine(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", " "))
	if len(s) > 160 {
		return s[:157] + "..."
	}
	return s
}

func fmtTime(v any) string {
	if t, ok := v.(time.Time); ok && !t.IsZero() {
		return t.UTC().Format(time.RFC3339)
	}
	return ""
}

// globMatch reports whether a stored path/glob matches a query path.
func globMatch(stored, query string) bool {
	stored, query = strings.TrimSpace(stored), strings.TrimSpace(query)
	if stored == "" || query == "" {
		return false
	}
	if ok, _ := path.Match(stored, query); ok {
		return true
	}
	if ok, _ := path.Match(query, stored); ok {
		return true
	}
	return strings.HasPrefix(query, strings.TrimSuffix(stored, "/")) ||
		strings.HasPrefix(stored, strings.TrimSuffix(query, "/")) ||
		strings.Contains(query, stored) || strings.Contains(stored, query)
}
