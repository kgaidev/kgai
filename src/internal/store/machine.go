package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
)

// The segment sync protocol rests on one invariant: ONE SHARD, ONE WRITER. A store
// that is copied to another machine (rsync, backup restore, laptop migration) breaks
// it — two live installations would share an installId and race each other's shard,
// silently forking it. The machine binding below detects the copy and rotates the
// install identity BEFORE the fork can happen; `kg rotate` (and the sync-side fork
// error) covers the same-machine copy that fingerprinting cannot see.

// machineFingerprint identifies the machine this store lives on. KGAI_MACHINE
// overrides (tests, containers with cloned machine-ids). Empty means "no signal";
// callers must never rotate on an empty fingerprint.
func machineFingerprint() string {
	if v := os.Getenv("KGAI_MACHINE"); v != "" {
		sum := sha256.Sum256([]byte(v))
		return "m" + hex.EncodeToString(sum[:])[:16]
	}
	var parts []string
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		if b, err := os.ReadFile(p); err == nil {
			if id := strings.TrimSpace(string(b)); id != "" {
				parts = append(parts, id)
				break
			}
		}
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		parts = append(parts, h)
	}
	if len(parts) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "m" + hex.EncodeToString(sum[:])[:16]
}

// applyMachineBinding records or verifies the machine binding on the loaded config.
// On a machine change it rotates the install identity in memory: the old installId
// is retired (its shard stays on disk as read-only history; sync reconciles it) and
// a fresh one is minted. The caller persists the config. Returns whether a rotation
// happened and whether the config changed at all.
func (s *Store) applyMachineBinding() (rotated, changed bool) {
	fp := machineFingerprint()
	if fp == "" || s.Config.Machine == fp {
		return false, false
	}
	if s.Config.Machine == "" {
		// First run with a binding-aware engine: adopt the current machine.
		s.Config.Machine = fp
		return false, true
	}
	old := s.Config.InstallID
	s.rotateInstallLocked()
	s.Config.Machine = fp
	fmt.Fprintf(os.Stderr, "kgai: store was copied from another machine — install identity rotated %s → %s (the old shard is kept as read-only history and reconciles on the next sync)\n", old, s.Config.InstallID)
	return true, true
}

// rotateInstallLocked retires the current installId and mints a fresh one. The old
// shard file is left untouched: it becomes foreign history that sync reconciles
// (re-recording any of its events the remote never received under the new identity).
func (s *Store) rotateInstallLocked() {
	s.Config.RetiredInstalls = append(s.Config.RetiredInstalls, s.Config.InstallID)
	s.Config.InstallID = "i" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	s.tailHash = nil
}

// RotateInstall forces a fresh install identity and persists it. It is the remedy
// the sync fork error points at: a store copied WITHIN one machine (cp -r, restored
// snapshot next to the original) shares both installId and machine fingerprint, so
// only the remote-side fork check can catch it — and rotation is how the copy
// re-enters the one-shard-one-writer protocol.
func (s *Store) RotateInstall() (old, cur string, err error) {
	old = s.Config.InstallID
	s.rotateInstallLocked()
	if fp := machineFingerprint(); fp != "" {
		s.Config.Machine = fp
	}
	return old, s.Config.InstallID, s.saveConfig()
}
