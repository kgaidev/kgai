// Package engine orchestrates the log (source of truth) and the graph (derived
// read-model): it turns a decision + its mutations into an immutable event, resolves
// element identities, supersedes the prior head decision(s) of every element it
// changes, and projects the result. Read commands (context/history/conflicts/as-of)
// query the element + decision planes.
package engine

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"kgai/internal/event"
	"kgai/internal/graph"
	"kgai/internal/store"
)

// parseImportDate accepts YYYY-MM-DD or RFC3339 and returns a normalized RFC3339 UTC
// timestamp, for back-dating imported decisions.
func parseImportDate(s string) (string, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format(time.RFC3339), nil
		}
	}
	return "", fmt.Errorf("invalid date %q (use YYYY-MM-DD or RFC3339)", s)
}

type Engine struct {
	S *store.Store
}

func New(s *store.Store) *Engine { return &Engine{S: s} }

func (e *Engine) openWrite() (*graph.Graph, error) {
	g, err := graph.Open(e.S.GraphPath(), false)
	if err != nil {
		return nil, err
	}
	if err := g.EnsureSchema(); err != nil {
		g.Close()
		return nil, err
	}
	return g, nil
}

// openRead opens the projection READ-ONLY so read commands can neither mutate the
// cache (a stray `kg query "… DELETE …"`) nor collide with the single writer. If the
// graph doesn't exist yet, it is built once (write-open + schema) and then reopened RO.
func (e *Engine) openRead() (*graph.Graph, error) {
	g, err := graph.Open(e.S.GraphPath(), true)
	if err != nil {
		wg, werr := e.openWrite()
		if werr != nil {
			return nil, werr
		}
		wg.Close()
		return graph.Open(e.S.GraphPath(), true)
	}
	return g, nil
}

// ---- ingest ----------------------------------------------------------------

// Ingest records one or more decisions (each an atomic immutable event) and projects
// them. On dryRun nothing is written; the result reports resolution only.
func (e *Engine) Ingest(in IngestInput, dryRun bool) (IngestResult, error) {
	var inputs []DecisionInput
	if in.Decision != nil {
		inputs = append(inputs, *in.Decision)
	}
	inputs = append(inputs, in.Decisions...)
	if len(inputs) == 0 {
		return IngestResult{}, fmt.Errorf("empty ingest: provide a \"decision\" (or \"decisions\")")
	}

	if err := e.S.Lock(); err != nil {
		return IngestResult{}, err
	}
	defer e.S.Unlock()

	g, err := e.openWrite()
	if err != nil {
		return IngestResult{}, err
	}
	defer g.Close()

	res := IngestResult{DryRun: dryRun, Elements: map[string]string{}}
	for _, di := range inputs {
		dr, ev, err := e.buildDecisionEvent(g, di, &res)
		if err != nil {
			return res, err
		}
		if dryRun {
			res.Decisions = append(res.Decisions, dr)
			continue
		}
		// Full idempotency: an identical decision (same content id) is already in the
		// log and graph — record nothing, so re-running an ingest is a true no-op.
		if rows, _ := g.Raw(`MATCH (d:Decision {id:'` + esc(ev.Decision.ID) + `'}) RETURN d.id`); len(rows) > 0 {
			res.Warnings = append(res.Warnings, "decision already recorded (no-op): "+dr.Title)
			res.Decisions = append(res.Decisions, dr)
			continue
		}
		// Back-dating: an explicit date sets the event's recorded_at (else Append stamps
		// now). Lamport is still assigned in ingest order, so listing decisions oldest-
		// first gives a history whose causal order matches the real dates.
		if di.Date != "" {
			ts, derr := parseImportDate(di.Date)
			if derr != nil {
				return res, fmt.Errorf("decision %q: %w", di.Title, derr)
			}
			ev.RecordedAt = ts
		}
		lam, err := e.S.NextLamport()
		if err != nil {
			return res, err
		}
		ev.Lamport = lam
		if err := e.S.Append(&ev); err != nil {
			return res, err
		}
		if err := g.ApplyEvent(ev); err != nil {
			return res, fmt.Errorf("event appended but projection failed (run `kg rebuild`): %w", err)
		}
		dr.EventHash = ev.Hash
		dr.Lamport = ev.Lamport
		res.Decisions = append(res.Decisions, dr)
	}
	return res, nil
}

func (e *Engine) buildDecisionEvent(g *graph.Graph, di DecisionInput, res *IngestResult) (DecisionResult, event.Event, error) {
	if strings.TrimSpace(di.Title) == "" {
		return DecisionResult{}, event.Event{}, fmt.Errorf("decision missing required \"title\"")
	}
	d := event.Decision{
		Title:     di.Title,
		Rationale: di.Rationale,
		Author:    orDefault(di.Author, e.S.Config.Actor),
		Refs:      joinRefs(di.Refs),
		Summary:   summarize(di.Mutations),
	}

	shapes := map[string]bool{}  // every element touched (provenance)
	targets := map[string]bool{} // elements this decision becomes the authority on
	upserted := map[string]bool{}
	touch := func(id string)  { shapes[id] = true }
	target := func(id string) { targets[id] = true; shapes[id] = true }
	// ensureUpsert guarantees an element referenced only by a link/set_prop is created
	// WITH its kind+name (otherwise it would be an unreadable nameless ghost node).
	ensureUpsert := func(tokenID, kind, name string) {
		if tokenID == "" || upserted[tokenID] {
			return
		}
		upserted[tokenID] = true
		d.Mutations = append(d.Mutations, event.Mutation{Op: event.MutUpsertElement, ElementID: tokenID, Kind: kind, Name: name})
		touch(tokenID)
	}

	for _, mi := range di.Mutations {
		switch event.MutOp(mi.Op) {
		case event.MutUpsertElement:
			m, err := e.resolveMutation(mi, res)
			if err != nil {
				return DecisionResult{}, event.Event{}, err
			}
			upserted[m.ElementID] = true
			touch(m.ElementID)
			if len(m.Props) > 0 {
				target(m.ElementID)
			}
			d.Mutations = append(d.Mutations, m)
		case event.MutSetProp:
			id, kind, name := e.resolveElementRef(mi.Element, res)
			ensureUpsert(id, kind, name)
			m, err := e.resolveMutation(mi, res)
			if err != nil {
				return DecisionResult{}, event.Event{}, err
			}
			target(m.ElementID)
			d.Mutations = append(d.Mutations, m)
		case event.MutAddLink, event.MutRetireLink:
			fid, fk, fn := e.resolveElementRef(mi.From, res)
			tid, tk, tn := e.resolveElementRef(mi.To, res)
			ensureUpsert(fid, fk, fn)
			ensureUpsert(tid, tk, tn)
			m, err := e.resolveMutation(mi, res)
			if err != nil {
				return DecisionResult{}, event.Event{}, err
			}
			target(m.FromID)
			touch(m.ToID)
			d.Mutations = append(d.Mutations, m)
		default:
			return DecisionResult{}, event.Event{}, fmt.Errorf("unknown mutation op %q", mi.Op)
		}
	}
	// Explicit extra authorities.
	for _, ref := range di.SupersedesOn {
		id, _, _ := e.resolveElementRef(ref, res)
		target(id)
	}

	// Supersede the current head decision(s) of every target element.
	supSet := map[string]bool{}
	for id := range targets {
		for _, h := range e.headDecisions(g, id) {
			supSet[h] = true
		}
	}
	d.Supersedes = sortedKeys(supSet)
	d.Shapes = sortedKeys(shapes)
	d.ID = event.DecisionID(d)

	dr := DecisionResult{ID: d.ID, Title: d.Title, Shapes: d.Shapes, Supersedes: d.Supersedes}
	ev := event.Event{Op: event.OpAssert, Decision: &d}
	return dr, ev, nil
}

func (e *Engine) resolveMutation(mi MutationInput, res *IngestResult) (event.Mutation, error) {
	switch event.MutOp(mi.Op) {
	case event.MutUpsertElement:
		if strings.TrimSpace(mi.Name) == "" {
			return event.Mutation{}, fmt.Errorf("upsert_element missing \"name\"")
		}
		kind := orDefault(mi.Kind, "concept")
		id := event.ElementID(kind, mi.Name)
		res.Elements[mi.Name] = id
		return event.Mutation{Op: event.MutUpsertElement, ElementID: id, Kind: kind, Name: mi.Name, Props: mi.Props}, nil
	case event.MutSetProp:
		id, _, _ := e.resolveElementRef(mi.Element, res)
		if id == "" {
			return event.Mutation{}, fmt.Errorf("set_prop missing \"element\"")
		}
		return event.Mutation{Op: event.MutSetProp, ElementID: id, Key: mi.Key, Value: mi.Value}, nil
	case event.MutAddLink, event.MutRetireLink:
		from, _, _ := e.resolveElementRef(mi.From, res)
		to, _, _ := e.resolveElementRef(mi.To, res)
		if from == "" || to == "" || strings.TrimSpace(mi.Link) == "" {
			return event.Mutation{}, fmt.Errorf("%s requires from, to and link", mi.Op)
		}
		return event.Mutation{Op: event.MutOp(mi.Op), FromID: from, ToID: to, LinkKind: strings.ToUpper(mi.Link), ElementID: from}, nil
	default:
		return event.Mutation{}, fmt.Errorf("unknown mutation op %q", mi.Op)
	}
}

// resolveElementRef parses "kind:name" (or "name", default kind concept) into a
// deterministic element id and records the resolution.
func (e *Engine) resolveElementRef(token string, res *IngestResult) (id, kind, name string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", "", ""
	}
	kind, name = "concept", token
	if i := strings.Index(token, ":"); i > 0 {
		kind, name = strings.TrimSpace(token[:i]), strings.TrimSpace(token[i+1:])
	}
	id = event.ElementID(kind, name)
	if res != nil {
		res.Elements[name] = id
	}
	return id, kind, name
}

// headDecisions returns the decision(s) currently authoritative over an element: those
// that SHAPES it with no other shaping decision superseding them. More than one ⇒ a
// conflict branch.
func (e *Engine) headDecisions(g *graph.Graph, elementID string) []string {
	rows, err := g.Raw(`MATCH (d:Decision)-[:SHAPES]->(e:Element {id:'` + esc(elementID) + `'})
		WHERE NOT EXISTS { MATCH (d2:Decision)-[:SUPERSEDES]->(d), (d2)-[:SHAPES]->(e) }
		RETURN d.id AS id, d.lamport AS lamport ORDER BY lamport DESC`)
	if err != nil {
		return nil
	}
	var out []string
	for _, r := range rows {
		out = append(out, asStr(r["id"]))
	}
	return out
}

// ---- rebuild / incremental apply ------------------------------------------

func (e *Engine) Rebuild() (int, error) {
	if err := e.S.Lock(); err != nil {
		return 0, err
	}
	defer e.S.Unlock()
	if err := removeAll(e.S.GraphPath()); err != nil {
		return 0, err
	}
	g, err := e.openWrite()
	if err != nil {
		return 0, err
	}
	defer g.Close()
	return e.replay(g)
}

func (e *Engine) ApplyNew() (int, error) {
	g, err := e.openWrite()
	if err != nil {
		return 0, err
	}
	defer g.Close()
	return e.replay(g)
}

func (e *Engine) replay(g *graph.Graph) (int, error) {
	all, err := e.S.ReadAll()
	if err != nil {
		return 0, err
	}
	store.SortEvents(all)
	n := 0
	for _, ev := range all {
		applied, err := g.Applied(ev.Hash)
		if err != nil {
			return n, err
		}
		if applied {
			continue
		}
		// Integrity gate: never project an event whose content doesn't match its hash,
		// or whose decision id doesn't match its content (corruption / tampered shard).
		// `kg doctor` reports these; replay just refuses to trust them.
		if !ev.Verify() {
			continue
		}
		if ev.Decision != nil && event.DecisionID(*ev.Decision) != ev.Decision.ID {
			continue
		}
		if err := g.ApplyEvent(ev); err != nil {
			return n, fmt.Errorf("apply %s: %w", ev.Hash, err)
		}
		n++
	}
	return n, nil
}

// ---- sync ------------------------------------------------------------------

func (e *Engine) Sync() (store.SyncResult, int, []ConflictGroup, error) {
	if err := e.S.Lock(); err != nil {
		return store.SyncResult{}, 0, nil, err
	}
	defer e.S.Unlock()
	sr, err := e.S.PullPush("kg sync")
	if err != nil {
		return sr, 0, nil, err
	}
	n, err := e.ApplyNew()
	if err != nil {
		return sr, n, nil, err
	}
	conf, err := e.conflictsLocked()
	return sr, n, conf, err
}

// ---- helpers ---------------------------------------------------------------

// esc escapes a string for safe single-quoted Cypher interpolation. Backslash must be
// escaped first, otherwise a trailing backslash would escape the closing quote. (Only
// hash-derived ids reach the interpolated read queries; this is defense in depth.)
func esc(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	return strings.ReplaceAll(s, "'", "\\'")
}

func orDefault(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

func joinRefs(refs []RefInput) string {
	var parts []string
	for _, r := range refs {
		parts = append(parts, r.System+":"+r.URL)
	}
	return strings.Join(parts, " ")
}

func summarize(ms []MutationInput) string {
	var parts []string
	for _, m := range ms {
		switch m.Op {
		case "upsert_element":
			parts = append(parts, "element "+m.Name)
		case "add_link":
			parts = append(parts, "link "+m.From+"-"+m.Link+"->"+m.To)
		case "retire_link":
			parts = append(parts, "retire "+m.From+"-"+m.Link+"->"+m.To)
		case "set_prop":
			parts = append(parts, "set "+m.Element+"."+m.Key)
		}
	}
	return strings.Join(parts, "; ")
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		if k != "" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
