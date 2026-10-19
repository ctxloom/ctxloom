package safefs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// GuardingLocks are Locks whose exclusive taker can record, in a lock file it
// creates, the path that file guards. A sidecar lock file — one kept apart
// from what it guards, under a flattened name that cannot always be read
// back into the path (paths.FlatName keeps only a tail and a hash) — otherwise
// cannot say whether what it guarded still exists, and so whether anything
// will ever take it again.
//
// It is a separate interface, not part of Locks, so that a Locks wrapping
// another by embedding (a test's tracker, say) keeps seeing every exclusive
// take through its own Lock: LockGuarding falls back to Lock on any Locks
// that does not implement this.
type GuardingLocks interface {
	// LockGuarding is Lock on path, recording guarded (absolute) as the
	// content of a lock file it creates. A lock file already there is left
	// as it is.
	LockGuarding(path, guarded string) (Lock, error)
}

// LockGuarding takes the exclusive lock at lockPath, a sidecar guarding the
// file or directory at guarded: through l's own LockGuarding when it has one
// (GuardingLocks), so a lock file it creates records guarded, made absolute;
// through Lock otherwise, recording nothing. The lock is re-checked as
// WithLock re-checks it.
func LockGuarding(l Locks, lockPath, guarded string) (Lock, error) {
	g, ok := l.(GuardingLocks)
	if !ok {
		return takeCurrent(lockPath, func() (Lock, error) { return l.Lock(lockPath) })
	}
	abs, err := filepath.Abs(guarded)
	if err != nil {
		return nil, fmt.Errorf("safefs: resolve %s, guarded by lock %s: %w", guarded, lockPath, err)
	}
	return takeCurrent(lockPath, func() (Lock, error) { return g.LockGuarding(lockPath, abs) })
}

// WithLockGuarding is WithLock taking its lock with LockGuarding.
func WithLockGuarding(l Locks, lockPath, guarded string, fn func() error) error {
	lk, err := LockGuarding(l, lockPath, guarded)
	if err != nil {
		return err
	}
	defer func() { _ = lk.Unlock() }()
	return fn()
}

// guardRecordMax bounds what GuardedPath reads as a record: a path longer
// than any filesystem names is no record.
const guardRecordMax = 8192

// guardRecord is the content a lock file records guarded as: the path and
// one newline, nothing else. The newline is the end marker — a record caught
// half-written has none — and the path is the whole of it, so it carries no
// secret a lock file readable by others could leak.
func guardRecord(guarded string) []byte { return []byte(guarded + "\n") }

// createRecording creates the lock file at path with guarded recorded in it
// (guardRecord) when no file is there yet; open opens path on the lock
// file's filesystem with the flags it is given. A file already there is
// left exactly as it is: whatever it records, or does not, came from the
// taker that created it, and opening it to write would put a second
// descriptor on a file someone may hold.
func createRecording(path, guarded string, open func(flag int) (io.WriteCloser, error)) error {
	f, err := open(os.O_WRONLY | os.O_CREATE | os.O_EXCL)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("safefs: creating lock %s: %w", path, err)
	}
	_, werr := f.Write(guardRecord(guarded))
	if err := errors.Join(werr, f.Close()); err != nil {
		return fmt.Errorf("safefs: recording what lock %s guards: %w", path, err)
	}
	return nil
}

// GuardedPath reads back the path a lock file's content records (see
// GuardingLocks). ok is false for anything that is not a whole record: an
// empty file (a lock file a taker that records nothing created), one caught
// half-written (no closing newline), or anything but one absolute, clean
// path free of control characters — a reader must never act on a path the
// record does not plainly state.
func GuardedPath(content []byte) (guarded string, ok bool) {
	if len(content) < 2 || len(content) > guardRecordMax {
		return "", false
	}
	body, found := bytes.CutSuffix(content, []byte("\n"))
	if !found || bytes.ContainsFunc(body, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", false
	}
	p := string(body)
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return "", false
	}
	return p, true
}

// lockFileRemover is a held lock that can unlink its own lock file, on the
// filesystem its Locks live on.
type lockFileRemover interface {
	removeLockFile() error
}

// ReapLockFile removes the lock file at path if, and only if, no taker holds
// it, and reports whether it did. A lock file that does not exist is not
// reaped and is no error; one some taker holds is not reaped either.
//
// It opens the file without creating it, takes it with ONE non-blocking
// attempt, confirms what it took is still the file at path (Lock.Current),
// and unlinks it while still holding it, releasing it after. Unlinking under
// the lock is what makes removal safe against the takers of this package: a
// taker that opened the file before the unlink is granted it only after,
// finds it no longer at the path, and takes the lock again on whatever file
// is there (Locks). A taker that does NOT re-check — a binary older than
// LockProtocol — would keep the unlinked file and hold the lock beside the
// next taker, so a caller must first establish that none can be running.
func ReapLockFile(l Locks, path string) (reaped bool, err error) {
	once, cancel := context.WithCancel(context.Background())
	cancel() // a done context: one attempt, never a wait
	lk, err := l.TryLockExisting(once, path)
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, ErrLockHeld):
		return false, nil
	case err != nil:
		return false, err
	}
	defer func() { _ = lk.Unlock() }()
	if !lk.Current() {
		return false, nil
	}
	rm, ok := lk.(lockFileRemover)
	if !ok {
		return false, fmt.Errorf("safefs: reap %s: a %T cannot remove the lock file it holds", path, lk)
	}
	if err := rm.removeLockFile(); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("safefs: reap %s: %w", path, err)
	}
	return true, nil
}
