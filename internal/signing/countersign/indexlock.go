package countersign

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofrs/flock"
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// WHY THIS FILE EXISTS, in one sentence: every write in this package is
// ATOMIC and exactly one of them is a READ-MODIFY-WRITE, and atomicity is no
// defence at all for that one.
//
// The signature records are content-addressed — indexHash(header, payload)
// plus a key tag — so two writers recording two different decisions write two
// different FILES and two writers recording the same decision write identical
// bytes to one path. Nothing there can clobber anything. MEASURED: 20
// concurrent WriteRefReject calls produce 20 records, every time.
//
// The sidecar index is the exception. AppendIndex and ForgetIndex both read
// the whole file, change it, and write the whole file back. The write goes
// through iox.WriteFileAtomicFs, which guarantees a reader never sees a TORN
// file — and guarantees nothing whatsoever about writer B having read the
// index before writer A's rename landed and then rewriting it without A's
// entry. MEASURED against this package before the lock existed: 20 concurrent
// AppendIndex calls left 3 or 4 entries. Sixteen to seventeen records lost,
// silently, with every call reporting success.
//
// That is the same shape task tall-nanny measured on config Save() (13 of 20
// lost, 20 of 20 once a lock actually engaged), and the lesson recorded there
// is the one that applies here: atomicity prevents a torn file, serialization
// prevents a lost update, and only one of those two problems has a fix in
// this package.

// lockedIndexUpdate runs fn as ONE serialized read-modify-write of this
// store's sidecar index. fn is the WHOLE cycle — read, modify, write — never
// just the write: a lock taken after the read protects nothing, because the
// stale read has already happened.
//
// LOCKING IS SKIPPED FOR A NON-OS FILESYSTEM. A lock exists to exclude other
// PROCESSES, and a test double (afero.MemMapFs and friends) has none — while
// composing a lock path from one of its often-nonexistent, often-unwritable
// paths and asking the REAL operating system to create and flock it would
// touch actual disk at an address the test never intended. Same reasoning,
// same shape, as agent.WithFileLock's own isOSBackedFs gate.
//
// AN ACQUISITION FAILURE FAILS THE CALL. flock.Lock blocks on ordinary
// contention rather than erroring, so an error here is a persistent
// environmental fault; proceeding unlocked on it would silently discard the
// one guarantee this function exists to provide, which is precisely the
// "advisory lock present but dead" shape that let tall-nanny's config Save()
// lose 13 of 20 writes while looking protected in review. The index is left
// untouched on that path — fn never runs.
//
// KNOWN LIMIT: the lock is keyed in the CALLING USER's home lock directory,
// so two UNIX accounts sharing one project's approvals store do not exclude
// each other. That is inherited from the ruled home-lock-dir placement (see
// indexLockPath) and is a strictly smaller exposure than the unlocked state
// this replaces, which excluded nobody at all.
func (s *Store) lockedIndexUpdate(fn func() error) error {
	if _, osBacked := s.fs.(*afero.OsFs); !osBacked {
		return fn()
	}
	lockPath, err := indexLockPath(s.indexPath())
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return fmt.Errorf("countersignature index lock: preparing %s: %w", filepath.Dir(lockPath), err)
	}
	fl := flock.New(lockPath)
	if err := fl.Lock(); err != nil {
		return fmt.Errorf("countersignature index lock: acquiring %s: %w", lockPath, err)
	}
	defer func() { _ = fl.Unlock() }()
	return fn()
}

// indexLockPath names the advisory lock guarding index, in ~/.ctxloom/locks.
//
// NOT beside the index. The USER store lives at ~/.ctxloom/approvals and the
// PROJECT store at <repo>/.ctxloom/approvals — and the project one is
// COMMITTABLE (paths.Layout marks it TierCommitted), so a sidecar there would
// be lock litter in everybody's diff. The home lock directory is where this
// repo already places locks for files a sidecar must not sit beside (ruled
// 2026-08-13, closing undated-bronco).
//
// The absolute path is flattened into ONE filename component, so every lock
// sits in one directory rather than a shadow tree. Two paths that flatten to
// the same name are left to COLLIDE deliberately: they merely over-serialize,
// which is the safe direction. The failure this cannot tolerate is the
// opposite one — one index, two lock names, excluding nobody — so the encoding
// is total and deterministic rather than injective.
func indexLockPath(index string) (string, error) {
	abs, err := filepath.Abs(index)
	if err != nil {
		return "", fmt.Errorf("countersignature index lock: resolve %s: %w", index, err)
	}
	dir, err := paths.HomeLocksDir()
	if err != nil {
		return "", fmt.Errorf("countersignature index lock: %w", err)
	}
	return filepath.Join(dir, strings.ReplaceAll(filepath.ToSlash(abs), "/", "__")+".lock"), nil
}
