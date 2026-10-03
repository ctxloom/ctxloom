package spool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/owneronly"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// identityRecord is one per-identity record directory under the spool root:
// the memory a reader keeps of the messages it has finished with, after the
// files themselves are deleted. in/delivered/ (Deliver) and out/routed/
// (Consume) are both one of these, so the record-then-delete order and the
// retention prune exist once.
//
// One file per identity, rather than one appended log, because a record can be
// written by a fresh process every turn: a shared file rewritten to bound it
// would lose whatever another process appended mid-rewrite, and closing that
// needs a lock. Per-identity files need none — a create is an atomic rename, a
// prune removes only what is past the window. A record directory is not a Dir:
// nothing in it is a message, and a sweep of its parent skips it because sweeps
// skip directories.
type identityRecord struct {
	// rel is the record directory, slash-separated, relative to the spool root.
	rel string
	// what names the transition in errors ("delivered", "routed").
	what string
}

// dir is harp's record directory in m's view.
func (r identityRecord) dir(m PathMapper, harp string) (string, error) {
	root, err := Root(m, harp)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, filepath.FromSlash(r.rel)), nil
}

// entry is identity's record path, refusing an identity that is not a bare
// file name: it comes from a message's frontmatter or from a caller, and is
// checked before it is ever joined to a path.
func (r identityRecord) entry(m PathMapper, harp, identity string) (string, error) {
	if err := ValidateName(identity); err != nil {
		return "", fmt.Errorf("spool: %q cannot be recorded as a %s identity: %w", identity, r.what, err)
	}
	dir, err := r.dir(m, harp)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, identity), nil
}

// finish records identity durably and only then deletes ref's file from
// srcDir, then prunes entries older than DeliveredRetention as of now.
//
// THE ORDER IS THE GUARANTEE: a crash leaves either the file alone (it is
// handled again: at-least-once) or the file and its record (the next reader
// sees the record and finishes the delete), never neither while the message is
// unhandled. A file already gone is ErrAlreadyGone, returned AFTER the record
// is written: whoever removed it, the identity is finished.
func (r identityRecord) finish(m PathMapper, ref Ref, srcDir Dir, identity string, now time.Time) error {
	if err := ValidateName(ref.Name); err != nil {
		return err
	}
	src, err := DirPath(m, ref.Harp, srcDir)
	if err != nil {
		return err
	}
	entry, err := r.entry(m, ref.Harp, identity)
	if err != nil {
		return err
	}
	if err := r.write(entry, identity); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(src, ref.Name)); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("spool: deleting %s %s: %w", r.what, ref, ErrAlreadyGone)
		}
		return fmt.Errorf("spool: deleting %s %s: %w", r.what, ref, err)
	}
	if err := syncDir(src); err != nil {
		return fmt.Errorf("spool: deleting %s %s: %w", r.what, ref, err)
	}
	return r.prune(m, ref.Harp, now)
}

// write records identity's entry, durably, unless it is already there: an
// existing entry already says what a new one would, and safefs.WriteFile
// refuses to write zero bytes over an existing file.
func (r identityRecord) write(entry, identity string) error {
	if _, err := os.Stat(entry); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("spool: reading the %s record for %s: %w", r.what, identity, err)
	}
	if err := os.MkdirAll(filepath.Dir(entry), owneronly.DirMode); err != nil {
		return fmt.Errorf("spool: create %s: %w", filepath.Dir(entry), err)
	}
	if err := safefs.WriteFile(afero.NewOsFs(), entry, nil, owneronly.FileMode, safefs.Durable()); err != nil {
		return fmt.Errorf("spool: recording %s as %s: %w", identity, r.what, err)
	}
	return nil
}

// has reports whether identity is in harp's record.
func (r identityRecord) has(m PathMapper, harp, identity string) (bool, error) {
	entry, err := r.entry(m, harp, identity)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(entry); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("spool: reading the %s record: %w", r.what, err)
	}
	return true, nil
}

// identities lists harp's record: each identity with the time it was
// recorded. A spool that never recorded anything has an empty one.
func (r identityRecord) identities(m PathMapper, harp string) (map[string]time.Time, error) {
	dir, err := r.dir(m, harp)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]time.Time{}, nil
		}
		return nil, fmt.Errorf("spool: listing the %s record %s: %w", r.what, dir, err)
	}
	out := make(map[string]time.Time, len(entries))
	for _, e := range entries {
		// ValidateName refuses the dot-prefixed staging name safefs.WriteFile
		// writes through, so a half-written entry is never listed.
		if e.IsDir() || ValidateName(e.Name()) != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue // pruned between the readdir and the stat
			}
			return nil, fmt.Errorf("spool: reading the %s record %s: %w", r.what, dir, err)
		}
		out[e.Name()] = info.ModTime()
	}
	return out, nil
}

// prune removes entries recorded before now-DeliveredRetention.
func (r identityRecord) prune(m PathMapper, harp string, now time.Time) error {
	ids, err := r.identities(m, harp)
	if err != nil {
		return err
	}
	dir, err := r.dir(m, harp)
	if err != nil {
		return err
	}
	cutoff := now.Add(-DeliveredRetention)
	for id, at := range ids {
		if !at.Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, id)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("spool: pruning %s record entry %s: %w", r.what, id, err)
		}
	}
	return nil
}
