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
// do. Each lock is a one-slot channel: holding it is having filled the slot.
var memLockTable = struct {
	sync.Mutex
	byFs map[afero.Fs]map[string]chan struct{}
}{byFs: map[afero.Fs]map[string]chan struct{}{}}

// memLocks are in-process locks over a test filesystem. They keep the lock
// file on that filesystem (created, refused when not a regular file), so
// Current and Held see what a removal did there.
type memLocks struct{ fs afero.Fs }

func (l memLocks) slot(path string) chan struct{} {
	memLockTable.Lock()
	defer memLockTable.Unlock()
	paths := memLockTable.byFs[l.fs]
	if paths == nil {
		paths = map[string]chan struct{}{}
		memLockTable.byFs[l.fs] = paths
	}
	key := filepath.Clean(path)
	s := paths[key]
	if s == nil {
		s = make(chan struct{}, 1)
		paths[key] = s
	}
	return s
}

func (l memLocks) Lock(path string) (Lock, error) {
	if err := l.fs.MkdirAll(filepath.Dir(path), lockDirMode); err != nil {
		return nil, fmt.Errorf("safefs: preparing lock directory for %s: %w", path, err)
	}
	if err := l.prepare(path); err != nil {
		return nil, err
	}
	s := l.slot(path)
	s <- struct{}{}
	return l.held(path, s)
}

func (l memLocks) TryLock(ctx context.Context, path string) (Lock, error) {
	if _, err := l.fs.Stat(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := l.prepare(path); err != nil {
		return nil, err
	}
	s := l.slot(path)
	select {
	case s <- struct{}{}:
		return l.held(path, s)
	default:
	}
	select {
	case s <- struct{}{}:
		return l.held(path, s)
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %s", ErrLockHeld, path)
	}
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
	s := l.slot(path)
	select {
	case s <- struct{}{}:
		<-s
		return false, nil
	default:
		return true, nil
	}
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

// held records which file was locked, for Current. Taken after the slot is
// filled, so a removal racing the grant is seen as one.
func (l memLocks) held(path string, s chan struct{}) (Lock, error) {
	info, err := l.fs.Stat(path)
	if err != nil {
		<-s
		return nil, fmt.Errorf("safefs: stat lock %s: %w", path, err)
	}
	return &memLock{fs: l.fs, path: path, info: info, slot: s}, nil
}

type memLock struct {
	fs   afero.Fs
	path string
	info fs.FileInfo
	slot chan struct{}
	once sync.Once
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
	l.once.Do(func() { <-l.slot })
	return nil
}
