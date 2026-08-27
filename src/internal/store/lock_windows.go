//go:build windows

package store

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockFile takes an exclusive lock on the whole of f (LockFileEx) — the Windows
// counterpart of flock, with the same try/wait semantics.
func lockFile(f *os.File, try bool) error {
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK)
	if try {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	ol := new(windows.Overlapped)
	return windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, ^uint32(0), ^uint32(0), ol)
}

func unlockFile(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, ^uint32(0), ^uint32(0), ol)
}

func isWouldBlock(err error) bool { return errors.Is(err, windows.ERROR_LOCK_VIOLATION) }
