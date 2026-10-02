package countersign

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/filelock"
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
// through safefs.WriteFile, which guarantees a reader never sees a TORN
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
// The acquisition is filelock.WithLock, the toolbox's one advisory lock, so
// this lock behaves as every other blocking lock in the tree does: skipped for
// a non-OS filesystem (a test double has no other process to exclude), failed
// closed on an acquisition error (fn never runs), and reported through
// lockwait when the wait runs long — the one lock that serializes recorded
// approvals and rejections must not be the one that blocks silently.
//
// KNOWN LIMIT: the lock is keyed in the CALLING USER's home lock directory,
// so two UNIX accounts sharing one project's approvals store do not exclude
// each other. That is inherited from the ruled home-lock-dir placement (see
// indexLockPath) and is a strictly smaller exposure than the unlocked state
// this replaces, which excluded nobody at all.
func (s *Store) lockedIndexUpdate(fn func() error) error {
	if !filelock.IsOSBackedFs(s.fs) {
		return fn()
	}
	lockPath, err := indexLockPath(s.indexPath())
	if err != nil {
		return err
	}
	return filelock.WithLock(s.fs, lockPath, fn)
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
// paths.HomePathFor is exactly that mapping — resolve to absolute, flatten
// into one bounded filename component, place under the home lock directory
// — and it is the mapping every other home-rooted foreign-file lock uses.
// Spelling it again here was a second copy of the encoding, which is the one
// failure a lock name cannot tolerate: one index, two lock names, excluding
// nobody.
func indexLockPath(index string) (string, error) {
	path, err := paths.HomePathFor(index)
	if err != nil {
		return "", fmt.Errorf("countersignature index lock: %w", err)
	}
	return path, nil
}
