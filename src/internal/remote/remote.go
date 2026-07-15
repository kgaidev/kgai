// Package remote implements the pluggable sync transports for the KG store. All
// transports move the same thing — append-only, per-install log events — and the
// local engine does everything else (projection, conflicts, queries), so a remote
// can be as dumb as an object store.
//
// Backends:
//   - git:  the store dir is a git repo; sync = fetch + union merge + push (BYO git)
//   - s3:   write-once segments in any S3-compatible bucket (BYO S3)
//   - kgai: the kgai cloud — same segment protocol over an authorized HTTP API
//     (server adds projection/UI/MCP on top); not wired up yet
package remote

import (
	"strings"

	"kgai/internal/store"
)

// SyncResult summarizes a sync run (serialized into the CLI's JSON output).
type SyncResult struct {
	Pulled bool   `json:"pulled"`
	Pushed bool   `json:"pushed"`
	Remote string `json:"remote,omitempty"`
	Detail string `json:"detail,omitempty"`
	// RebuildNeeded is set when sync rewrote local history in place (retired-shard
	// reconciliation after an identity rotation) — incremental apply cannot express
	// that, so the caller must fully rebuild the projection.
	RebuildNeeded bool `json:"rebuild_needed,omitempty"`
}

// Remote is one sync transport. Sync exchanges log events with the remote: it uploads
// local events not yet there and appends newly arrived foreign events into the local
// per-install shards. The caller holds the store write lock.
type Remote interface {
	Sync(s *store.Store) (SyncResult, error)
}

// For picks the transport for a remote URL:
//
//	s3://bucket/prefix          → S3 segment sync
//	kgai://org/project          → kgai cloud (not yet available)
//	anything else (or empty)    → git (empty = local commit only)
func For(url string) (Remote, error) {
	switch {
	case strings.HasPrefix(url, "s3://"):
		return newS3Remote(url)
	case strings.HasPrefix(url, "kgai://"):
		return newCloudRemote(url)
	default:
		return &gitRemote{}, nil
	}
}
