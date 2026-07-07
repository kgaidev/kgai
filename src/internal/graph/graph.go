// Package graph is the only place that talks to the embedded property-graph engine
// (Kuzu today; LadybugDB is an API/Cypher-compatible fork and can be swapped here
// without touching the rest of the codebase).
//
// The graph is a DERIVED, rebuildable projection of the decision log:
//   - a LIVE element graph (Element nodes + current LINK edges) = the current state,
//   - a DECISION plane (Decision nodes, SHAPES/SUPERSEDES) = the immutable history.
//
// Applying a decision mutates the live graph (it may DELETE a LINK on retire); this
// loses nothing because immutability lives in the log, and replay reconstructs any
// past state. Every projection write is idempotent (MERGE / no-op DELETE), so
// replaying an event twice — or replaying a partial log — stays consistent.
package graph

import (
	"fmt"
	"sort"
	"strings"
	"time"

	kuzu "github.com/kuzudb/go-kuzu"
	"kgai/internal/event"
)

// Graph wraps an open database + connection.
type Graph struct {
	db       *kuzu.Database
	conn     *kuzu.Connection
	readonly bool
	// stmts caches prepared statements per query string — replay/ingest run the same
	// handful of MERGE statements tens of thousands of times.
	stmts map[string]*kuzu.PreparedStatement
	inTxn bool
}

// Open opens (or creates) the graph at path. readonly opens it for queries only.
func Open(path string, readonly bool) (*Graph, error) {
	cfg := kuzu.DefaultSystemConfig()
	cfg.ReadOnly = readonly
	db, err := kuzu.OpenDatabase(path, cfg)
	if err != nil {
		return nil, fmt.Errorf("open graph: %w", err)
	}
	conn, err := kuzu.OpenConnection(db)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("open connection: %w", err)
	}
	return &Graph{db: db, conn: conn, readonly: readonly, stmts: map[string]*kuzu.PreparedStatement{}}, nil
}

// Close releases the connection and database (an open transaction is rolled back).
func (g *Graph) Close() {
	if g.inTxn {
		_ = g.exec(`ROLLBACK`, nil)
	}
	for _, s := range g.stmts {
		s.Close()
	}
	if g.conn != nil {
		g.conn.Close()
	}
	if g.db != nil {
		g.db.Close()
	}
}

// Begin/Commit group many projection writes into one transaction. Without this every
// statement auto-commits (own WAL flush), which makes bulk replay ~50× slower.
func (g *Graph) Begin() error {
	if g.inTxn {
		return nil
	}
	if err := g.exec(`BEGIN TRANSACTION`, nil); err != nil {
		return err
	}
	g.inTxn = true
	return nil
}

func (g *Graph) Commit() error {
	if !g.inTxn {
		return nil
	}
	g.inTxn = false
	return g.exec(`COMMIT`, nil)
}

// Raw runs an arbitrary read query and returns rows as column→value maps.
func (g *Graph) Raw(query string) ([]map[string]any, error) {
	return g.query(query, nil)
}

func (g *Graph) query(q string, params map[string]any) ([]map[string]any, error) {
	res, err := g.run(q, params)
	if err != nil {
		return nil, err
	}
	defer res.Close()
	var out []map[string]any
	for res.HasNext() {
		t, err := res.Next()
		if err != nil {
			return nil, err
		}
		m, err := t.GetAsMap()
		t.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func (g *Graph) run(q string, params map[string]any) (*kuzu.QueryResult, error) {
	if len(params) == 0 {
		return g.conn.Query(q)
	}
	stmt, ok := g.stmts[q]
	if !ok {
		var err error
		stmt, err = g.conn.Prepare(q)
		if err != nil {
			return nil, fmt.Errorf("prepare: %w (%s)", err, q)
		}
		g.stmts[q] = stmt
	}
	return g.conn.Execute(stmt, params)
}

func (g *Graph) exec(q string, params map[string]any) error {
	res, err := g.run(q, params)
	if err != nil {
		return fmt.Errorf("exec: %w (%s)", err, q)
	}
	res.Close()
	return nil
}

// EnsureSchema creates the node/rel tables if they do not already exist.
func (g *Graph) EnsureSchema() error {
	ddl := []string{
		`CREATE NODE TABLE Element(id STRING, kind STRING, name STRING, props STRING,
			PRIMARY KEY(id))`,
		`CREATE NODE TABLE Decision(id STRING, title STRING, rationale STRING, author STRING,
			refs STRING, summary STRING, recorded_at TIMESTAMP, lamport INT64,
			install_id STRING, event_hash STRING, PRIMARY KEY(id))`,
		`CREATE NODE TABLE _Applied(hash STRING, PRIMARY KEY(hash))`,
		`CREATE REL TABLE LINK(FROM Element TO Element, kind STRING, created_by STRING)`,
		`CREATE REL TABLE SHAPES(FROM Decision TO Element, authority BOOLEAN)`,
		`CREATE REL TABLE SUPERSEDES(FROM Decision TO Decision)`,
	}
	for _, q := range ddl {
		if err := g.exec(q, nil); err != nil {
			if strings.Contains(err.Error(), "already exists") {
				continue
			}
			return err
		}
	}
	// Heal caches built before SHAPES carried `authority` (default true = the old
	// semantics, where every shaping decision counted as a head candidate).
	if err := g.exec(`ALTER TABLE SHAPES ADD authority BOOLEAN DEFAULT true`, nil); err != nil &&
		!strings.Contains(err.Error(), "already exists") && !strings.Contains(err.Error(), "duplicated") {
		// Older engines that can't ALTER simply keep working after a `kg rebuild`.
		_ = err
	}
	return nil
}

// Applied reports whether an event hash has already been projected into the graph.
func (g *Graph) Applied(hash string) (bool, error) {
	rows, err := g.query(`MATCH (a:_Applied {hash:$h}) RETURN a.hash AS h`, map[string]any{"h": hash})
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

// ApplyEvent projects one decision event into the graph idempotently.
func (g *Graph) ApplyEvent(ev event.Event) error {
	if ev.Decision == nil {
		return g.exec(`MERGE (:_Applied {hash:$h})`, map[string]any{"h": ev.Hash})
	}
	d := ev.Decision

	// 1) Decision node (history plane).
	if err := g.exec(
		`MERGE (n:Decision {id:$id})
		 ON CREATE SET n.title=$title, n.rationale=$rat, n.author=$author, n.refs=$refs,
		   n.summary=$summary, n.recorded_at=$ra, n.lamport=$lam, n.install_id=$inst,
		   n.event_hash=$eh`,
		map[string]any{
			"id": d.ID, "title": d.Title, "rat": d.Rationale, "author": d.Author,
			"refs": d.Refs, "summary": d.Summary, "ra": parseTime(ev.RecordedAt),
			"lam": ev.Lamport, "inst": ev.InstallID, "eh": ev.Hash,
		}); err != nil {
		return err
	}

	// 2) Structural mutations against the live element graph.
	for _, m := range d.Mutations {
		if err := g.applyMutation(m); err != nil {
			return err
		}
	}

	// 3) Provenance: this decision SHAPES every element it touched; `authority` marks
	// the subset it structurally changed (only those drive heads/conflicts). Events
	// from engines predating Targets get authority=true everywhere (old semantics).
	targets := map[string]bool{}
	for _, t := range d.Targets {
		targets[t] = true
	}
	for _, eid := range d.Shapes {
		g.ensureElement(eid, "", "")
		auth := targets[eid] || len(d.Targets) == 0
		if err := g.exec(
			`MATCH (n:Decision {id:$d}), (e:Element {id:$e}) MERGE (n)-[r:SHAPES]->(e)
			 ON CREATE SET r.authority=$a`,
			map[string]any{"d": d.ID, "e": eid, "a": auth}); err != nil {
			return err
		}
	}

	// 4) Decision evolution: supersede prior head decisions (stub created if not yet replayed).
	for _, prev := range d.Supersedes {
		g.ensureDecision(prev)
		if err := g.exec(
			`MATCH (a:Decision {id:$a}), (b:Decision {id:$b}) MERGE (a)-[:SUPERSEDES]->(b)`,
			map[string]any{"a": d.ID, "b": prev}); err != nil {
			return err
		}
	}

	return g.exec(`MERGE (:_Applied {hash:$h})`, map[string]any{"h": ev.Hash})
}

func (g *Graph) applyMutation(m event.Mutation) error {
	switch m.Op {
	case event.MutUpsertElement:
		if err := g.ensureElement(m.ElementID, m.Kind, m.Name); err != nil {
			return err
		}
		// Persist inline props (previously silently dropped). Sorted keys keep the
		// blob deterministic regardless of apply order.
		for k, v := range m.Props {
			cur, err := g.elementProps(m.ElementID)
			if err != nil {
				return err
			}
			if err := g.exec(`MATCH (e:Element {id:$id}) SET e.props=$p`,
				map[string]any{"id": m.ElementID, "p": setProp(cur, k, v)}); err != nil {
				return err
			}
		}
		return nil
	case event.MutSetProp:
		if err := g.ensureElement(m.ElementID, "", ""); err != nil {
			return err
		}
		cur, err := g.elementProps(m.ElementID)
		if err != nil {
			return err
		}
		return g.exec(
			`MATCH (e:Element {id:$id}) SET e.props = $props`,
			map[string]any{"id": m.ElementID, "props": setProp(cur, m.Key, m.Value)})
	case event.MutAddLink:
		g.ensureElement(m.FromID, "", "")
		g.ensureElement(m.ToID, "", "")
		return g.exec(
			`MATCH (a:Element {id:$f}), (b:Element {id:$t})
			 MERGE (a)-[r:LINK {kind:$k}]->(b) ON CREATE SET r.created_by=$by`,
			map[string]any{"f": m.FromID, "t": m.ToID, "k": m.LinkKind, "by": m.ElementID})
	case event.MutRetireLink:
		// Remove the live edge; the decision (history) records that it was retired.
		return g.exec(
			`MATCH (a:Element {id:$f})-[r:LINK {kind:$k}]->(b:Element {id:$t}) DELETE r`,
			map[string]any{"f": m.FromID, "t": m.ToID, "k": m.LinkKind})
	default:
		return fmt.Errorf("unknown mutation op %q", m.Op)
	}
}

// ensureElement MERGEs an element; properties are set on create only (kind/name are
// immutable identity facts). A bare ensure (no kind/name) makes a stub for edges.
func (g *Graph) ensureElement(id, kind, name string) error {
	if id == "" {
		return nil
	}
	if kind == "" && name == "" {
		return g.exec(`MERGE (e:Element {id:$id})`, map[string]any{"id": id})
	}
	return g.exec(
		`MERGE (e:Element {id:$id}) ON CREATE SET e.kind=$kind, e.name=$name, e.props=''`,
		map[string]any{"id": id, "kind": kind, "name": name})
}

func (g *Graph) ensureDecision(id string) {
	if id != "" {
		_ = g.exec(`MERGE (:Decision {id:$id})`, map[string]any{"id": id})
	}
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// elementProps returns the raw props blob of an element ("" if none/absent).
func (g *Graph) elementProps(id string) (string, error) {
	rows, err := g.query(`MATCH (e:Element {id:$id}) RETURN e.props AS p`, map[string]any{"id": id})
	if err != nil || len(rows) == 0 {
		return "", err
	}
	if s, ok := rows[0]["p"].(string); ok {
		return s, nil
	}
	return "", nil
}

// setProp updates one key in a newline-delimited "key=value" props blob, preserving
// the other keys. The live graph is tiny, so a flat text blob is enough.
func setProp(blob, key, value string) string {
	key = strings.TrimSpace(key)
	// The blob is newline-delimited "key=value"; neutralize separators in inputs so a
	// value can never forge another key (the key never legitimately contains '=').
	key = strings.NewReplacer("\n", " ", "\r", " ", "=", "-").Replace(key)
	value = strings.NewReplacer("\n", " ", "\r", " ").Replace(value)
	var out []string
	replaced := false
	for _, line := range strings.Split(blob, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if k := strings.SplitN(line, "=", 2)[0]; k == key {
			out = append(out, key+"="+value)
			replaced = true
		} else {
			out = append(out, line)
		}
	}
	if !replaced {
		out = append(out, key+"="+value)
	}
	// Keep lines sorted by key so the blob is independent of apply order — this keeps
	// the projection deterministic across machines (incremental vs full replay).
	sort.Slice(out, func(i, j int) bool {
		return strings.SplitN(out[i], "=", 2)[0] < strings.SplitN(out[j], "=", 2)[0]
	})
	return strings.Join(out, "\n")
}
