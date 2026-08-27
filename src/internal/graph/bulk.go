package graph

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"kgai/internal/event"
	"kgai/internal/replay"
)

// BulkLoad projects a full, canonically-sorted event log into an EMPTY graph via
// Kuzu's COPY FROM bulk import: the events are replayed into the in-memory
// projection (internal/replay — the same semantics as ApplyEvent, shared with the
// kgview preview app), then loaded as a handful of CSV COPYs instead of one MERGE
// statement per fact. On a 20k-decision log this turns a
// minutes-long rebuild into seconds. The caller guarantees the database is fresh
// (rebuild path) and events are verified and sorted.
func (g *Graph) BulkLoad(events []event.Event) (int, error) {
	st := replay.New()
	for _, ev := range events {
		st.Apply(ev)
	}

	dir, err := os.MkdirTemp("", "kg-bulk-*")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)

	write := func(name string, header []string, rows [][]string) (string, int, error) {
		p := filepath.Join(dir, name)
		f, err := os.Create(p)
		if err != nil {
			return "", 0, err
		}
		w := csv.NewWriter(f)
		_ = w.Write(header)
		if err := w.WriteAll(rows); err != nil {
			f.Close()
			return "", 0, err
		}
		w.Flush()
		if err := f.Close(); err != nil {
			return "", 0, err
		}
		return p, len(rows), nil
	}

	copyIn := func(table, path string, n int) error {
		if n == 0 {
			return nil
		}
		// PARALLEL=FALSE: props/rationales legitimately contain newlines, and Kuzu's
		// parallel CSV reader cannot split multi-line quoted records.
		q := fmt.Sprintf(`COPY %s FROM '%s' (HEADER=true, PARALLEL=FALSE)`, table, escPath(path))
		return g.exec(q, nil)
	}

	// ---- nodes -----------------------------------------------------------------
	var rows [][]string
	for _, id := range replay.SortedKeys(st.Elements) {
		e := st.Elements[id]
		rows = append(rows, []string{id, e.Kind, e.Name, e.Props})
	}
	p, n, err := write("element.csv", []string{"id", "kind", "name", "props"}, rows)
	if err != nil {
		return 0, err
	}
	if err := copyIn("Element", p, n); err != nil {
		return 0, err
	}

	rows = nil
	for _, id := range replay.SortedKeys(st.Decisions) {
		d := st.Decisions[id]
		rows = append(rows, []string{id, d.Title, d.Rationale, d.Author, d.Refs, d.Summary,
			d.RecordedAt, strconv.FormatInt(d.Lamport, 10), d.InstallID, d.EventHash})
	}
	p, n, err = write("decision.csv", []string{"id", "title", "rationale", "author", "refs",
		"summary", "recorded_at", "lamport", "install_id", "event_hash"}, rows)
	if err != nil {
		return 0, err
	}
	if err := copyIn("Decision", p, n); err != nil {
		return 0, err
	}

	rows = nil
	for _, h := range st.AppliedOrder {
		rows = append(rows, []string{h})
	}
	p, n, err = write("applied.csv", []string{"hash"}, rows)
	if err != nil {
		return 0, err
	}
	if err := copyIn("_Applied", p, n); err != nil {
		return 0, err
	}

	// ---- rels ------------------------------------------------------------------
	rows = nil
	for _, k := range replay.SortedKeys(st.Links) {
		l := st.Links[k]
		rows = append(rows, []string{l.From, l.To, l.Kind, l.CreatedBy})
	}
	p, n, err = write("link.csv", []string{"from", "to", "kind", "created_by"}, rows)
	if err != nil {
		return 0, err
	}
	if err := copyIn("LINK", p, n); err != nil {
		return 0, err
	}

	rows = nil
	for _, k := range replay.SortedKeys(st.Shapes) {
		s := st.Shapes[k]
		rows = append(rows, []string{s.Decision, s.Element, strconv.FormatBool(s.Authority)})
	}
	p, n, err = write("shapes.csv", []string{"from", "to", "authority"}, rows)
	if err != nil {
		return 0, err
	}
	if err := copyIn("SHAPES", p, n); err != nil {
		return 0, err
	}

	rows = nil
	for _, k := range replay.SortedKeys(st.Supersedes) {
		s := st.Supersedes[k]
		rows = append(rows, []string{s.From, s.To})
	}
	p, n, err = write("supersedes.csv", []string{"from", "to"}, rows)
	if err != nil {
		return 0, err
	}
	if err := copyIn("SUPERSEDES", p, n); err != nil {
		return 0, err
	}

	// Kuzu's CSV reader turns empty fields into NULLs; the statement path stores empty
	// strings. Normalize so both paths yield byte-identical canonical exports.
	if err := g.exec(`MATCH (e:Element) SET e.kind=coalesce(e.kind,''),
		e.name=coalesce(e.name,''), e.props=coalesce(e.props,'')`, nil); err != nil {
		return 0, err
	}
	if err := g.exec(`MATCH (d:Decision) SET d.title=coalesce(d.title,''),
		d.rationale=coalesce(d.rationale,''), d.author=coalesce(d.author,''),
		d.refs=coalesce(d.refs,''), d.summary=coalesce(d.summary,''),
		d.install_id=coalesce(d.install_id,''), d.event_hash=coalesce(d.event_hash,'')`, nil); err != nil {
		return 0, err
	}

	return len(st.AppliedOrder), nil
}

func escPath(p string) string { return p } // MkdirTemp paths contain no quotes
