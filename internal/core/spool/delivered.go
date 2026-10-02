package spool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/owneronly"
)

// A DELIVERED MESSAGE IS DELETED; ITS IDENTITY IS WHAT SURVIVES IT.
//
// Once a reader has delivered an in/ message (the engine accepted the turn, or
// the owner's turn-start hook wrote it out), the file is removed and its
// Entry.Identity is recorded as an empty file in/delivered/<identity>. That
// record is the inbox's whole memory of what it has delivered:
//
//   - Claim and the runner's sweep refuse a copy whose identity is recorded;
//   - a steer withdrawal answers "already delivered" from it;
//   - the coordinator credits delivery progress from it.
//
// One file per identity, rather than one appended log, because the owner's
// record is written by a fresh hook process every turn: a shared file rewritten
// to bound it would lose whatever another process appended mid-rewrite, and
// closing that needs a lock. Per-identity files need none — a create is an
// atomic rename, a prune removes only what is past the window — and they are
// the in/wake/ convention this package already has. Like in/wake/, in/delivered/
// is not a Dir: nothing in it is a message, and a sweep of in/ skips it because
// sweeps skip directories.
//
// THE ORDER IS THE GUARANTEE. Deliver writes the record durably and only then
// deletes the file, so a crash leaves either the file alone (it is delivered
// again: at-least-once, the substrate's floor) or the file and its record (the
// next reader sees the record and finishes the delete), and never neither while
// the message is undelivered.

// deliveredDirName is the delivered-identity record, relative to the spool root.
const deliveredDirName = "in/delivered"

// DeliveredRetention bounds the record: an entry older than this is pruned.
//
// It is sized to the redelivery horizon, the longest a copy of a delivered
// identity can still reach a reader. No producer re-sends an identity it has
// already written (every coordinator send mints a fresh id), so the one copy
// that can arrive is this package's own: a file whose delete was interrupted
// after its record was written. The reader sees it at its NEXT start — a
// child's next incarnation, or the owner's next turn, which can be a session
// resumed days later. A week covers that with margin; past it, the cost is one
// repeated delivery, which at-least-once already admits.
const DeliveredRetention = 7 * 24 * time.Hour

// deliveredDir is harp's record directory in m's view.
func deliveredDir(m PathMapper, harp string) (string, error) {
	root, err := Root(m, harp)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, filepath.FromSlash(deliveredDirName)), nil
}

// deliveredEntry is identity's record path, refusing an identity that is not
// a bare file name: it comes from a message's frontmatter or from a caller,
// and is checked before it is ever joined to a path.
func deliveredEntry(m PathMapper, harp, identity string) (string, error) {
	if err := ValidateName(identity); err != nil {
		return "", fmt.Errorf("spool: %q cannot be recorded as a delivered identity: %w", identity, err)
	}
	dir, err := deliveredDir(m, harp)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, identity), nil
}

// Deliver acknowledges one delivered inbox message: it records identity in the
// spool's delivered record, durably, and only then deletes ref's file. ref must
// address in/ or in/claimed/ — the only places a reader delivers from.
//
// A file that is already gone is ErrAlreadyGone, returned AFTER the record is
// written: whoever removed it, the identity is delivered. Each call also prunes
// record entries older than DeliveredRetention as of now.
func Deliver(m PathMapper, ref Ref, identity string, now time.Time) error {
	if ref.Dir != DirIn && ref.Dir != ClaimedDirName {
		return fmt.Errorf("spool: %s is not an inbox entry; only %q and %q are delivered from", ref, DirIn, ClaimedDirName)
	}
	if err := ValidateName(ref.Name); err != nil {
		return err
	}
	src, err := DirPath(m, ref.Harp, ref.Dir)
	if err != nil {
		return err
	}
	entry, err := deliveredEntry(m, ref.Harp, identity)
	if err != nil {
		return err
	}
	if err := recordDelivered(entry, identity); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(src, ref.Name)); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("spool: deleting delivered %s: %w", ref, ErrAlreadyGone)
		}
		return fmt.Errorf("spool: deleting delivered %s: %w", ref, err)
	}
	if err := syncDir(src); err != nil {
		return fmt.Errorf("spool: deleting delivered %s: %w", ref, err)
	}
	return pruneDelivered(m, ref.Harp, now)
}

// recordDelivered writes identity's record entry, durably, unless it is
// already there: an existing entry already says what a new one would, and
// iox.WriteFileAtomic refuses to write zero bytes over an existing file.
func recordDelivered(entry, identity string) error {
	if _, err := os.Stat(entry); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("spool: reading the delivered record for %s: %w", identity, err)
	}
	if err := os.MkdirAll(filepath.Dir(entry), owneronly.DirMode); err != nil {
		return fmt.Errorf("spool: create %s: %w", filepath.Dir(entry), err)
	}
	if err := iox.WriteFileAtomic(entry, nil, owneronly.FileMode, iox.Durable()); err != nil {
		return fmt.Errorf("spool: recording %s as delivered: %w", identity, err)
	}
	return nil
}

// Delivered reports whether identity is in harp's delivered record.
func Delivered(m PathMapper, harp, identity string) (bool, error) {
	entry, err := deliveredEntry(m, harp, identity)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(entry); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("spool: reading the delivered record: %w", err)
	}
	return true, nil
}

// DeliveredIdentities lists harp's delivered record: each identity with the
// time it was recorded. A spool that never delivered anything has an empty one.
func DeliveredIdentities(m PathMapper, harp string) (map[string]time.Time, error) {
	dir, err := deliveredDir(m, harp)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]time.Time{}, nil
		}
		return nil, fmt.Errorf("spool: listing the delivered record %s: %w", dir, err)
	}
	out := make(map[string]time.Time, len(entries))
	for _, e := range entries {
		// ValidateName refuses the dot-prefixed staging name WriteFileAtomic
		// writes through, so a half-written entry is never listed.
		if e.IsDir() || ValidateName(e.Name()) != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue // pruned between the readdir and the stat
			}
			return nil, fmt.Errorf("spool: reading the delivered record %s: %w", dir, err)
		}
		out[e.Name()] = info.ModTime()
	}
	return out, nil
}

// pruneDelivered removes record entries recorded before now-DeliveredRetention.
func pruneDelivered(m PathMapper, harp string, now time.Time) error {
	ids, err := DeliveredIdentities(m, harp)
	if err != nil {
		return err
	}
	dir, err := deliveredDir(m, harp)
	if err != nil {
		return err
	}
	cutoff := now.Add(-DeliveredRetention)
	for id, at := range ids {
		if !at.Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, id)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("spool: pruning delivered record entry %s: %w", id, err)
		}
	}
	return nil
}
