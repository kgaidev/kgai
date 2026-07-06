// Segment protocol shared by object-store remotes (S3 today, kgai cloud later).
//
// The wire format is a set of WRITE-ONCE objects:
//
//	<prefix>/segments/<installID>/<seq %06d>-<eventCount>.ndjson
//
// Each segment holds the next batch of events from one install's append-only shard,
// in order. Because every install only ever writes its own keys and objects are
// immutable, the remote state is a pure union — no merge, no conflicts, no rebase.
//
// The protocol is STATELESS on the client: what has been pushed/pulled is derived by
// comparing local shard event counts with the cumulative event counts encoded in the
// segment keys. Losing local caches never desyncs anything.
package remote

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
)

// Segment describes one remote segment object.
type Segment struct {
	Key     string // full object key
	Install string
	Seq     int
	Count   int
}

var segKeyRe = regexp.MustCompile(`(^|/)segments/([^/]+)/(\d{6})-(\d+)\.ndjson$`)

// ParseSegmentKey extracts install/seq/count from an object key. ok=false for keys
// that are not segments (they are ignored, so the bucket can hold other data too).
func ParseSegmentKey(key string) (seg Segment, ok bool) {
	m := segKeyRe.FindStringSubmatch(key)
	if m == nil {
		return Segment{}, false
	}
	seq, err1 := strconv.Atoi(m[3])
	count, err2 := strconv.Atoi(m[4])
	if err1 != nil || err2 != nil || count <= 0 {
		return Segment{}, false
	}
	return Segment{Key: key, Install: m[2], Seq: seq, Count: count}, true
}

// SegmentKey builds the object key for a new segment.
func SegmentKey(prefix, install string, seq, count int) string {
	return path.Join(prefix, "segments", install, fmt.Sprintf("%06d-%d.ndjson", seq, count))
}

// GroupSegments buckets parsed segments by install, each sorted by seq.
func GroupSegments(segs []Segment) map[string][]Segment {
	byInstall := map[string][]Segment{}
	for _, s := range segs {
		byInstall[s.Install] = append(byInstall[s.Install], s)
	}
	for k := range byInstall {
		sort.Slice(byInstall[k], func(i, j int) bool { return byInstall[k][i].Seq < byInstall[k][j].Seq })
	}
	return byInstall
}

// PushPlan says what (if anything) to upload for this install's shard.
type PushPlan struct {
	Needed    bool
	FromIndex int // first local event index to include
	Seq       int // seq number for the new segment
}

// PlanPush compares the local shard length with what the remote already has.
// remoteSegs must be this install's segments only. An error means the remote claims
// MORE events from this install than exist locally — a reused installID or a reset
// store; pushing would fork the shard, so it is refused.
func PlanPush(localCount int, remoteSegs []Segment) (PushPlan, error) {
	cum, lastSeq := 0, 0
	for _, s := range remoteSegs {
		cum += s.Count
		if s.Seq > lastSeq {
			lastSeq = s.Seq
		}
	}
	if cum > localCount {
		return PushPlan{}, fmt.Errorf("remote has %d events for this install but only %d exist locally (reused install id or reset store) — refusing to push", cum, localCount)
	}
	if cum == localCount {
		return PushPlan{}, nil
	}
	return PushPlan{Needed: true, FromIndex: cum, Seq: lastSeq + 1}, nil
}

// Fetch is one segment to download, with SkipEvents already-present events to drop
// from its head (a segment can be partially applied when counts moved between LIST
// and a previous partial sync).
type Fetch struct {
	Segment    Segment
	SkipEvents int
}

// PlanPull returns, for one foreign install, which segments to download given how
// many of that install's events are already in the local shard.
func PlanPull(localCount int, remoteSegs []Segment) []Fetch {
	var out []Fetch
	cum := 0
	for _, s := range remoteSegs {
		if localCount >= cum+s.Count {
			cum += s.Count
			continue // fully present locally
		}
		skip := 0
		if localCount > cum {
			skip = localCount - cum
		}
		out = append(out, Fetch{Segment: s, SkipEvents: skip})
		cum += s.Count
	}
	return out
}
