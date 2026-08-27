package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"kgai/internal/view"
)

// request is one line from the client. The id is echoed back as given.
type request struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// message is one line to the client: a response (id + result or error) or a
// notification (method + params, no id).
type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Result any             `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
	Method string          `json:"method,omitempty"`
	Params any             `json:"params,omitempty"`
}

// elements is the answer to "elements": the groups and how many rows they hold.
type elements struct {
	Groups []view.ElementGroup `json:"groups"`
	Shown  int                 `json:"shown"`
	Total  int                 `json:"total"`
}

type server struct {
	mu    sync.Mutex // guards model
	model *view.Model
	wmu   sync.Mutex // one line at a time on the wire
	out   io.Writer
}

func newServer(m *view.Model, out io.Writer) *server {
	return &server{model: m, out: out}
}

// serve answers requests from in on out until in ends. With an interval, the log is
// watched and every change reloads the model and announces itself.
func serve(in io.Reader, out io.Writer, dir string, interval time.Duration) error {
	s := newServer(view.Load(dir), out)
	if interval > 0 {
		stop := make(chan struct{})
		defer close(stop)
		go s.watch(interval, stop)
	}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			s.send(message{Error: "not a request: " + err.Error()})
			continue
		}
		res, err := s.dispatch(req.Method, req.Params)
		msg := message{ID: req.ID, Result: res}
		if err != nil {
			msg.Result, msg.Error = nil, err.Error()
		}
		s.send(msg)
	}
	return sc.Err()
}

func (s *server) send(m message) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	b, err := json.Marshal(m)
	if err != nil {
		b, _ = json.Marshal(message{ID: m.ID, Error: "encoding the answer: " + err.Error()})
	}
	_, _ = s.out.Write(append(b, '\n'))
}

func (s *server) current() *view.Model {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.model
}

func (s *server) set(m *view.Model) *view.Model {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.model = m
	return m
}

// watch polls the log and, when it changed, swaps in a fresh model and tells the client.
func (s *server) watch(interval time.Duration, stop <-chan struct{}) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			cur := s.current()
			if !cur.Changed() {
				continue
			}
			m := view.Load(cur.Dir)
			if s.current() == cur { // nobody opened another folder meanwhile
				s.set(m)
				s.send(message{Method: "changed", Params: m.Status()})
			}
		}
	}
}

// dispatch answers one method. Every answer comes from one immutable model, so a
// reload between two requests can never mix two states in one answer.
func (s *server) dispatch(method string, params json.RawMessage) (any, error) {
	m := s.current()
	var p struct {
		Dir, ID, Name, By, Kind, Text string
	}
	if len(params) > 0 && method != "decisions" {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("bad params: %w", err)
		}
	}
	switch method {
	case "status":
		return m.Status(), nil
	case "open":
		return s.set(view.Load(p.Dir)).Status(), nil
	case "refresh":
		return s.set(view.Load(m.Dir)).Status(), nil
	case "overview":
		return m.Overview(), nil
	case "filters":
		return m.FilterValues(), nil
	case "decisions":
		var f view.DecisionFilter
		if len(params) > 0 {
			if err := json.Unmarshal(params, &f); err != nil {
				return nil, fmt.Errorf("bad filter: %w", err)
			}
		}
		return m.DecisionRows(f), nil
	case "decision":
		d, ok := m.Decision(p.ID)
		if !ok {
			return nil, fmt.Errorf("no decision %q in this log", p.ID)
		}
		return d, nil
	case "elements":
		groups, shown := m.ElementGroups(p.Kind, p.Text)
		return elements{Groups: groups, Shown: shown, Total: m.Stats.Elements}, nil
	case "element":
		e, ok := m.Element(p.ID)
		if !ok {
			return nil, fmt.Errorf("no element %q in this log", p.ID)
		}
		return e, nil
	case "conflicts":
		return m.Conflicts(), nil
	case "people":
		return m.PersonRows(p.By), nil
	case "person":
		person, ok := m.Person(p.Name, p.By)
		if !ok {
			return nil, fmt.Errorf("nobody named %q in this log", p.Name)
		}
		return person, nil
	}
	return nil, fmt.Errorf("unknown method %q", method)
}
