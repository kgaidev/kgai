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
	mine := s.Config.InstallID

	// ---- fork check on my shard ------------------------------------------------
	// One shard, one writer: before pushing anything, prove the remote's copy of MY
	// shard is a prefix of my local shard. Count math alone cannot see a same-length
	// fork (a copied store whose twin already pushed different events), so the last
	// remote segment's content is compared — the shard is a hash chain, so agreement
	// on the final event proves the whole remote prefix.
	myEvs, err := s.ShardEvents(mine)
	if err != nil {
		return res, err
	}
	if err := r.verifyOwnShard(mine, myEvs, byInstall[mine]); err != nil {
		return res, err
	}

	// ---- reconcile retired shards ----------------------------------------------
	// This store previously wrote as these installIds (identity was rotated after a
	// copy/migration). The remote is the canon for a retired id: any local events it
	// never received are re-recorded under the CURRENT identity, and the local shard
	// file is aligned with the remote so pulls stay consistent.
	for _, retired := range s.Config.RetiredInstalls {
		reemitted, rewrote, err := r.reconcileRetired(s, retired, byInstall[retired])
		if rewrote {
			// even a partial rewrite invalidates the incremental-apply assumption
			res.RebuildNeeded = true
			res.Pulled = true
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("reconcile %s: %v", retired, err))
			continue
		}
		if reemitted > 0 {
			// my shard grew — refresh the local view before planning the push
			if myEvs, err = s.ShardEvents(mine); err != nil {
				return res, err
			}
		}
	}
	counts, err = s.ShardCounts()
	if err != nil {
		return res, err
	}

	// ---- push my tail ---------------------------------------------------------
	plan, err := PlanPush(len(myEvs), byInstall[mine])
	if err != nil {
		return res, forkError(mine, err.Error())
	}
	if plan.Needed {
		data, count := marshalSegment(myEvs[plan.FromIndex:])
		key := SegmentKey(r.prefix, mine, plan.Seq, count)
		if err := r.os.Put(key, data); err != nil {
			if strings.Contains(err.Error(), "already exists") {
				// Write-once arbitration fired: someone else just wrote OUR next
				// segment — a live twin of this install is racing us right now.
				return res, forkError(mine, err.Error())
			}
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
		// Accumulate all of this install's new events and append ONCE: AppendForeign
		// re-reads the whole local shard to verify the hash chain, so appending per
		// segment is quadratic in shard size (painful on a cold clone of a big log).
		var batch []event.Event
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
			batch = append(batch, evs[f.SkipEvents:]...)
		}
		if len(batch) == 0 {
			continue
		}
		if err := s.AppendForeign(install, batch); err != nil {
			// A hash-chain break here means THAT install forked (its writer must
			// `kg rotate`); our own log is unaffected.
			problems = append(problems, fmt.Sprintf("append %s: %v", install, err))
			continue
		}
		res.Pulled = true
	}

	if len(problems) > 0 {
		res.Detail = strings.Join(problems, "; ")
	}
	return res, nil
}

// verifyOwnShard fails with a fork error when the remote's copy of this install's
// shard is not a prefix of the local shard.
func (r *objectRemote) verifyOwnShard(install string, local []event.Event, segs []Segment) error {
	if len(segs) == 0 {
		return nil
	}
	total := 0
	for _, sg := range segs {
		total += sg.Count
	}
	if total > len(local) {
		return forkError(install, fmt.Sprintf("the remote holds %d events for this install but only %d exist locally", total, len(local)))
	}
	last := segs[len(segs)-1]
	data, err := r.os.Get(last.Key)
	if err != nil {
		// Cannot verify → do not push blind; fail the sync as transient.
		return fmt.Errorf("cannot verify shard integrity (get %s: %w) — retry the sync", last.Key, err)
	}
	evs, err := decodeSegment(data, install)
	if err != nil {
		return fmt.Errorf("remote segment %s is corrupt: %w", last.Key, err)
	}
	if len(evs) != last.Count {
		return fmt.Errorf("remote segment %s declares %d events but holds %d", last.Key, last.Count, len(evs))
	}
	if evs[len(evs)-1].Hash != local[total-1].Hash {
		return forkError(install, fmt.Sprintf("remote event %d is %s but the local event at that position is %s", total, short(evs[len(evs)-1].Hash), short(local[total-1].Hash)))
	}
	return nil
}

// reconcileRetired aligns one retired shard with the remote. Returns how many
// orphaned events were re-emitted under the current identity and whether the local
// shard file was rewritten (which forces a projection rebuild).
func (r *objectRemote) reconcileRetired(s *store.Store, retired string, segs []Segment) (reemitted int, rewrote bool, err error) {
	local, err := s.ShardEvents(retired)
	if err != nil || len(local) == 0 {
		return 0, false, err
	}
	// Materialize the remote's version of the shard.
	var remote []event.Event
	for _, sg := range segs {
		data, err := r.os.Get(sg.Key)
		if err != nil {
			return 0, false, fmt.Errorf("get %s: %w", sg.Key, err)
		}
		evs, err := decodeSegment(data, retired)
		if err != nil {
			return 0, false, fmt.Errorf("%s: %w", sg.Key, err)
		}
		remote = append(remote, evs...)
	}
	// Find the divergence point.
	k := 0
	for k < len(local) && k < len(remote) && local[k].Hash == remote[k].Hash {
		k++
	}
	orphans := local[k:]
	if len(orphans) == 0 {
		return 0, false, nil // local is a prefix of remote → the normal pull path appends the rest
	}
	// The remote is the canon for a retired id: adopt its version locally…
	if err := s.RewriteShard(retired, remote); err != nil {
		return 0, false, err
	}
	// …and re-record the orphaned decisions under the current identity (content-
	// addressed decision ids make this idempotent in the projection).
	n, err := s.ReEmit(orphans)
	if err != nil {
		return n, true, err
	}
	return n, true, nil
}

func forkError(install, detail string) error {
	return fmt.Errorf("shard fork detected for install %s: this store and another live installation share one identity — the store was probably copied (%s).\nRun `kg rotate` in THIS store, then `kg sync` again: it gets a fresh identity and any local-only decisions are re-recorded under it. Nothing is lost", install, detail)
}

func short(h string) string {
	if len(h) > 19 {
		return h[:19] + "…"
	}
	return h
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
