package remote

import (
	"encoding/json"
	"fmt"
	"strings"

	"kgai/internal/event"
	"kgai/internal/store"
)

// ObjectStore is the minimal object API the segment protocol needs. S3 implements it
// directly; the kgai cloud will implement it via presigned URLs from its API.
type ObjectStore interface {
	List(prefix string) ([]string, error)
	Get(key string) ([]byte, error)
	// Put writes a new object. Implementations should refuse to overwrite an existing
	// key where the backend supports it (segments are write-once).
	Put(key string, data []byte) error
}

// objectRemote runs the stateless segment protocol over any ObjectStore.
type objectRemote struct {
	os     ObjectStore
	prefix string
	name   string // for SyncResult.Remote
}

func (r *objectRemote) Sync(s *store.Store) (SyncResult, error) {
	res := SyncResult{Remote: r.name}

	keys, err := r.os.List(r.prefix)
	if err != nil {
		res.Detail = "list failed (offline / no access?): " + err.Error()
		return res, nil // offline is not fatal — the local log is intact
	}
	var segs []Segment
	for _, k := range keys {
		if seg, ok := ParseSegmentKey(k); ok {
			segs = append(segs, seg)
		}
	}
	byInstall := GroupSegments(segs)

	counts, err := s.ShardCounts()
	if err != nil {
		return res, err
	}

	var problems []string

	// ---- push my tail ---------------------------------------------------------
	mine := s.Config.InstallID
	plan, err := PlanPush(counts[mine], byInstall[mine])
	if err != nil {
		problems = append(problems, err.Error())
	} else if plan.Needed {
		evs, err := s.ShardEvents(mine)
		if err != nil {
			return res, err
		}
		data, count := marshalSegment(evs[plan.FromIndex:])
		key := SegmentKey(r.prefix, mine, plan.Seq, count)
		if err := r.os.Put(key, data); err != nil {
			res.Detail = "push failed: " + err.Error()
			return res, nil
		}
		res.Pushed = true
	}

	// ---- pull everyone else's tails -------------------------------------------
	for install, isegs := range byInstall {
		if install == mine {
			continue
		}
		for _, f := range PlanPull(counts[install], isegs) {
			data, err := r.os.Get(f.Segment.Key)
			if err != nil {
				problems = append(problems, fmt.Sprintf("get %s: %v", f.Segment.Key, err))
				break // later segments of this install depend on this one
			}
			evs, err := decodeSegment(data, install)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", f.Segment.Key, err))
				break
			}
			if f.SkipEvents >= len(evs) {
				continue
			}
			if err := s.AppendForeign(install, evs[f.SkipEvents:]); err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", f.Segment.Key, err))
				break
			}
			res.Pulled = true
		}
	}

	if len(problems) > 0 {
		res.Detail = strings.Join(problems, "; ")
	}
	return res, nil
}

// marshalSegment serializes events as NDJSON, returning the payload and event count.
func marshalSegment(evs []event.Event) ([]byte, int) {
	var b strings.Builder
	for _, ev := range evs {
		line, _ := json.Marshal(ev)
		b.Write(line)
		b.WriteByte('\n')
	}
	return []byte(b.String()), len(evs)
}

// decodeSegment parses and verifies a segment: every event must carry a valid content
// hash and belong to the install whose key path it came from (a tampered or misfiled
// segment is rejected as a unit).
func decodeSegment(data []byte, install string) ([]event.Event, error) {
	var out []event.Event
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev event.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			return nil, fmt.Errorf("malformed event: %w", err)
		}
		if !ev.Verify() {
			return nil, fmt.Errorf("event %s fails hash verification", ev.Hash)
		}
		if ev.InstallID != install {
			return nil, fmt.Errorf("event %s claims install %s but lives under %s", ev.Hash, ev.InstallID, install)
		}
		out = append(out, ev)
	}
	return out, nil
}
