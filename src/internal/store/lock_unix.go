//go:build !windows

package store

import (
	"os"
	"syscall"
)

// lockFile takes the exclusive advisory lock on f (flock). With try set it returns
// isWouldBlock-true error instead of waiting when another process holds it.
func lockFile(f *os.File, try bool) error {
	how := syscall.LOCK_EX
	if try {
		how |= syscall.LOCK_NB
	}
	return syscall.Flock(int(f.Fd()), how)
}

func unlockFile(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }

func isWouldBlock(err error) bool { return err == syscall.EWOULDBLOCK }
