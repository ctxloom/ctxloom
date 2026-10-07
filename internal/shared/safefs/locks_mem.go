package safefs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/spf13/afero"
	"github.com/spf13/afero/mem"
)

// memLockTable is every in-memory lock, by filesystem and then by cleaned
// path, so Roots NewMem builds over one fs contend as processes on one disk
// do.
var memLockTable = struct {
	sync.Mutex
	byFs map[afero.Fs]map[string]*memRW
}{byFs: map[afero.Fs]map[string]*memRW{}}

// memRW is one path's in-memory lock: an exclusive holder, or any number of
// shared ones. changed is closed (and replaced) on every release, so a waiter
// re-checks exactly when the holders change and can also give up on its
// context.
type memRW struct {
	mu      sync.Mutex
	writer  bool
	readers int
	changed chan struct{}
}

// acquire takes the lock, shared or exclusive, waiting until ctx is done. It
// attempts once before looking at ctx, so a done ctx is one attempt.
func (m *memRW) acquire(ctx context.Context, shared bool) bool {
	for {
		m.mu.Lock()
		if !m.writer && (shared || m.readers == 0) {
			if shared {
				m.readers++
			} else {
				m.writer = true
			}
			m.mu.Unlock()
			return true
		}
		wait := m.changed
		m.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return false
		}
	}
}

func (m *memRW) release(shared bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if shared {
		m.readers--
	} else {
		m.writer = false
	}
	close(m.changed)
	m.changed = make(chan struct{})
}

func (m *memRW) held() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writer || m.readers > 0
}

// memLocks are in-process locks over a test filesystem. They keep the lock
// file on that filesystem (created, refused when not a regular file), so
// Current and Held see what a removal did there.
type memLocks struct{ fs afero.Fs }

func (l memLocks) slot(path string) *memRW {
	memLockTable.Lock()
	defer memLockTable.Unlock()
	paths := memLockTable.byFs[l.fs]
	if paths == nil {
		paths = map[string]*memRW{}
		memLockTable.byFs[l.fs] = paths
	}
	key := filepath.Clean(path)
	s := paths[key]
	if s == nil {
		s = &memRW{changed: make(chan struct{})}
		paths[key] = s
	}
	return s
}

func (l memLocks) Lock(path string) (Lock, error) { return l.take(path, false) }

func (l memLocks) RLock(path string) (Lock, error) { return l.take(path, true) }

// take is Lock and RLock: it blocks until path's lock is held in the kind
// shared names.
func (l memLocks) take(path string, shared bool) (Lock, error) {
	if err := l.fs.MkdirAll(filepath.Dir(path), lockDirMode); err != nil {
		return nil, fmt.Errorf("safefs: preparing lock directory for %s: %w", path, err)
	}
	if err := l.prepare(path); err != nil {
		return nil, err
	}
	s := l.slot(path)
	s.acquire(context.Background(), shared)
	return l.held(path, s, shared)
}

func (l memLocks) TryLock(ctx context.Context, path string) (Lock, error) {
	return l.try(ctx, path, true)
}

func (l memLocks) TryLockExisting(ctx context.Context, path string) (Lock, error) {
	return l.try(ctx, path, false)
}

// try is TryLock and TryLockExisting; create says whether a missing lock
// file is created.
func (l memLocks) try(ctx context.Context, path string, create bool) (Lock, error) {
	if create {
		if _, err := l.fs.Stat(filepath.Dir(path)); err != nil {
			return nil, err
		}
		if err := l.prepare(path); err != nil {
			return nil, err
		}
	} else if err := l.existing(path); err != nil {
		return nil, err
	}
	s := l.slot(path)
	if !s.acquire(ctx, false) {
		return nil, fmt.Errorf("%w: %s", ErrLockHeld, path)
	}
	return l.held(path, s, false)
}

// existing refuses a lock path that is missing (its stat error,
// fs.ErrNotExist) or not a regular file.
func (l memLocks) existing(path string) error {
	info, err := l.fs.Stat(path)
	switch {
	case err != nil:
		return fmt.Errorf("safefs: lock %s: %w", path, err)
	case !info.Mode().IsRegular():
		return fmt.Errorf("%w: %s is %s", ErrNotRegularFile, path, info.Mode().Type())
	}
	return nil
}

func (l memLocks) Held(path string) (bool, error) {
	info, err := l.fs.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("safefs: probing lock %s: %w", path, err)
	case !info.Mode().IsRegular():
		return false, fmt.Errorf("%w: %s is %s", ErrNotRegularFile, path, info.Mode().Type())
	}
	return l.slot(path).held(), nil
}

// prepare creates the lock file if it is missing and refuses one that is not
// a regular file.
func (l memLocks) prepare(path string) error {
	f, err := l.fs.OpenFile(path, os.O_CREATE|os.O_RDONLY, lockFileMode)
	if err != nil {
		return fmt.Errorf("safefs: opening lock %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	return requireRegular(f, path)
}

// held records which file was locked, for Current. Taken after the lock is
// granted, so a removal racing the grant is seen as one.
func (l memLocks) held(path string, s *memRW, shared bool) (Lock, error) {
	info, err := l.fs.Stat(path)
	if err != nil {
		s.release(shared)
		return nil, fmt.Errorf("safefs: stat lock %s: %w", path, err)
	}
	return &memLock{fs: l.fs, path: path, info: info, rw: s, shared: shared}, nil
}

type memLock struct {
	fs     afero.Fs
	path   string
	info   fs.FileInfo
	rw     *memRW
	shared bool
	once   sync.Once
}

// Current compares file identity where the filesystem exposes it
// (afero.MemMapFs's file data) and otherwise settles for the path still
// existing.
func (l *memLock) Current() bool {
	now, err := l.fs.Stat(l.path)
	if err != nil {
		return false
	}
	a, aok := l.info.(*mem.FileInfo)
	b, bok := now.(*mem.FileInfo)
	if aok && bok {
		return a.FileData == b.FileData
	}
	return true
}

func (l *memLock) Unlock() error {
	l.once.Do(func() { l.rw.release(l.shared) })
	return nil
}
