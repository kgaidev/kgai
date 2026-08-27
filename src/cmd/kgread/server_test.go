package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kgai/internal/view"
	"kgai/internal/view/viewtest"
)

// reply is one line from the server, decoded loosely.
type reply struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// client drives a server over pipes the way an extension would.
type client struct {
	t     *testing.T
	in    io.WriteCloser
	lines chan string
	next  int
}

func startServer(t *testing.T, dir string, interval time.Duration) *client {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- serve(inR, outW, dir, interval); outW.Close() }()
	c := &client{t: t, in: inW, lines: make(chan string, 64)}
	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
		for sc.Scan() {
			c.lines <- sc.Text()
		}
		close(c.lines)
	}()
	t.Cleanup(func() {
		inW.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("serve did not stop when stdin closed")
		}
	})
	return c
}

func (c *client) write(line string) {
	if _, err := io.WriteString(c.in, line+"\n"); err != nil {
		c.t.Fatal(err)
	}
}

// read returns the next line as a reply, within a timeout.
func (c *client) read() (reply, string) {
	select {
	case line, ok := <-c.lines:
		if !ok {
			c.t.Fatal("server closed its output")
		}
		var r reply
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			c.t.Fatalf("not JSON: %q", line)
		}
		return r, line
	case <-time.After(5 * time.Second):
		c.t.Fatal("no answer within 5s")
	}
	return reply{}, ""
}

// call sends one request and waits for its response, letting notifications pass.
func (c *client) call(method, params string) reply {
	c.t.Helper()
	c.next++
	id := c.next
	if params == "" {
		c.write(fmt.Sprintf(`{"id":%d,"method":%q}`, id, method))
	} else {
		c.write(fmt.Sprintf(`{"id":%d,"method":%q,"params":%s}`, id, method, params))
	}
	for {
		r, _ := c.read()
		if r.Method != "" {
			continue // a notification
		}
		if string(r.ID) != fmt.Sprint(id) {
			c.t.Fatalf("answer for id %s, wanted %d", r.ID, id)
		}
		return r
	}
}

func (c *client) result(method, params string, into any) {
	c.t.Helper()
	r := c.call(method, params)
	if r.Error != "" {
		c.t.Fatalf("%s: %s", method, r.Error)
	}
	if err := json.Unmarshal(r.Result, into); err != nil {
		c.t.Fatalf("%s: %v in %s", method, err, r.Result)
	}
}

func fixtureDir(t *testing.T) string {
	t.Helper()
	viewtest.Isolate(t)
	dir := t.TempDir()
	viewtest.WriteStore(t, filepath.Join(dir, ".kgai", "store"))
	return dir
}

func TestServeAnswers(t *testing.T) {
	dir := fixtureDir(t)
	c := startServer(t, dir, 0)

	var st view.Store
	c.result("status", "", &st)
	if st.Counts.Decisions != 4 || st.Counts.Conflicts != 1 || st.Error != "" {
		t.Fatalf("status = %+v", st)
	}

	var ds view.Decisions
	c.result("decisions", `{"state":"note"}`, &ds)
	if ds.Shown != 1 || ds.Rows[0].Title != "Considered PDF-only invoices, rejected" {
		t.Fatalf("decisions = %+v", ds)
	}
	c.result("decisions", "", &ds)
	if ds.Shown != 4 {
		t.Fatalf("all decisions = %+v", ds)
	}

	var d view.Decision
	c.result("decision", fmt.Sprintf(`{"id":%q}`, ds.Rows[1].ID), &d)
	if d.Title != "Draft invoices stay visible" || d.State != "head" || len(d.Supersedes) != 1 {
		t.Fatalf("decision = %+v", d)
	}

	var els elements
	c.result("elements", `{"text":"inv"}`, &els)
	if els.Shown != 1 || els.Total != 2 || els.Groups[0].Elements[0].Name != "Invoice" {
		t.Fatalf("elements = %+v", els)
	}
	var e view.Element
	c.result("element", fmt.Sprintf(`{"id":%q}`, viewtest.Invoice), &e)
	if e.Name != "Invoice" || e.Heads != 2 || len(e.History) != 4 {
		t.Fatalf("element = %+v", e)
	}

	var cs []view.Conflict
	c.result("conflicts", "", &cs)
	if len(cs) != 1 || len(cs[0].Heads) != 2 {
		t.Fatalf("conflicts = %+v", cs)
	}

	var people []view.PersonRow
	c.result("people", `{"by":"actor"}`, &people)
	if len(people) != 2 || people[0].Name != "alice" {
		t.Fatalf("people = %+v", people)
	}
	var bob view.Person
	c.result("person", `{"name":"bob","by":"actor"}`, &bob)
	if bob.Share != 50 || len(bob.Recent) != 2 {
		t.Fatalf("bob = %+v", bob)
	}

	var f view.Filters
	c.result("filters", "", &f)
	if len(f.Actors) != 2 || len(f.States) != 3 {
		t.Fatalf("filters = %+v", f)
	}
	var o view.Overview
	c.result("overview", "", &o)
	if o.Store.Counts.Elements != 2 || len(o.MostShaped) != 2 {
		t.Fatalf("overview = %+v", o)
	}

	if r := c.call("decision", `{"id":"d_nothing"}`); !strings.Contains(r.Error, "no decision") {
		t.Fatalf("missing decision: %+v", r)
	}
	if r := c.call("dance", ""); !strings.Contains(r.Error, "unknown method") {
		t.Fatalf("unknown method: %+v", r)
	}
	if r := c.call("decisions", `{"regex":true,"text":"("}`); r.Error != "" {
		t.Fatalf("a bad expression is an answer, not a failure: %+v", r)
	} else if !strings.Contains(string(r.Result), "not valid") {
		t.Fatalf("bad regex: %s", r.Result)
	}

	// A line that is not a request gets an error without an id, and the server goes on.
	c.write("this is not json")
	if r, _ := c.read(); r.Error == "" || len(r.ID) != 0 {
		t.Fatalf("garbage: %+v", r)
	}
	c.result("status", "", &st)
	if st.Counts.Decisions != 4 {
		t.Fatal("the server stopped answering after garbage")
	}
}

func TestOpenAnotherFolder(t *testing.T) {
	dir := fixtureDir(t)
	c := startServer(t, t.TempDir(), 0) // an empty folder first
	var st view.Store
	c.result("status", "", &st)
	if st.Error == "" || !strings.Contains(st.Hint, "No knowledge graph here yet") {
		t.Fatalf("empty folder: %+v", st)
	}
	var opened view.Store
	c.result("open", fmt.Sprintf(`{"dir":%q}`, dir), &opened)
	if opened.Error != "" || opened.Counts.Decisions != 4 {
		t.Fatalf("opened: %+v", opened)
	}
	var ds view.Decisions
	c.result("decisions", "", &ds)
	if ds.Shown != 4 {
		t.Fatalf("after open: %+v", ds)
	}
}

func TestWatchNotifies(t *testing.T) {
	dir := fixtureDir(t)
	c := startServer(t, dir, 30*time.Millisecond)
	var st view.Store
	c.result("status", "", &st)
	viewtest.Append(t, filepath.Join(dir, ".kgai", "store"), viewtest.More())
	deadline := time.After(5 * time.Second)
	for {
		select {
		case line := <-c.lines:
			var r reply
			if json.Unmarshal([]byte(line), &r) == nil && r.Method == "changed" {
				var s view.Store
				if err := json.Unmarshal(r.Params, &s); err != nil || s.Counts.Decisions != 5 {
					t.Fatalf("changed carried %s", r.Params)
				}
				c.result("status", "", &st)
				if st.Counts.Decisions != 5 {
					t.Fatalf("after change: %+v", st)
				}
				return
			}
		case <-deadline:
			t.Fatal("no changed notification within 5s")
		}
	}
}

func TestOnce(t *testing.T) {
	dir := fixtureDir(t)
	s := newServer(view.Load(dir), io.Discard)
	res, err := s.dispatch("overview", nil)
	if err != nil {
		t.Fatal(err)
	}
	if o := res.(view.Overview); o.Store.Counts.Decisions != 4 {
		t.Fatalf("overview = %+v", o)
	}
	if _, err := s.dispatch("person", json.RawMessage(`{"name":"carol"}`)); err == nil {
		t.Fatal("unknown person answered")
	}
	if _, err := s.dispatch("decision", json.RawMessage(`not json`)); err == nil {
		t.Fatal("bad params answered")
	}
}

// TestWriteFixture exports the fixture store for the extension's own tests:
// KGREAD_FIXTURE_OUT=<dir> go test ./cmd/kgread -run TestWriteFixture
func TestWriteFixture(t *testing.T) {
	out := os.Getenv("KGREAD_FIXTURE_OUT")
	if out == "" {
		t.Skip("set KGREAD_FIXTURE_OUT=<dir> to write the fixture store there")
	}
	if err := os.RemoveAll(filepath.Join(out, ".kgai")); err != nil {
		t.Fatal(err)
	}
	viewtest.WriteStore(t, filepath.Join(out, ".kgai", "store"))
}
