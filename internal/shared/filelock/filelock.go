// Package filelock locks for a caller that is handed only an afero.Fs, not a
// safefs.Root: it picks the lock from the fs — the controller's own kernel
// locks (safefs.New) for the OS filesystem, none at all for anything else.
// That choice is the fs detection a safefs.Root makes unnecessary, so a
// caller that can be handed a Root locks through safefs.WithLock instead.
package filelock

import (
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// IsOSBackedFs reports whether fs is the real operating-system filesystem, as
// opposed to a test double (afero.MemMapFs, a ReadOnlyFs wrapping one, ...).
// A nil fs is the OS filesystem by the family's convention.
func IsOSBackedFs(fs afero.Fs) bool {
	if fs == nil {
		return true
	}
	_, ok := fs.(*afero.OsFs)
	return ok
}

// WithLock runs fn under lockPath's lock (safefs.WithLock over safefs.New's
// Locks) when fs is OS-backed, and runs fn unlocked otherwise: locking
// excludes other processes, which a test double has none of, and asking the
// REAL OS to lock a path the test never meant would touch actual disk.
func WithLock(fs afero.Fs, lockPath string, fn func() error) error {
	if !IsOSBackedFs(fs) {
		return fn()
	}
	return safefs.WithLock(safefs.New().Locks, lockPath, fn)
}
