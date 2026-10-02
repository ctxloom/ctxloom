package spool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// THE OWNER'S RESERVATION IS A DIRECTORY.
//
// A child's in/ is drained by a Home that lives as long as the run, so it can
// hold "what I have handed out and not yet acknowledged" in memory. The
// session OWNER's in/ is drained by a hook subprocess that lives for one
// turn: whatever it remembers dies with it, and the next turn's hook is a
// stranger. So the in-flight set is a directory, in/claimed/, and the three
// states a message passes through are three directories:
//
//	in/           written, unclaimed        — Pending asks only about this one
//	in/claimed/   taken by a reader, unacknowledged
//	(deleted)     delivered: its identity is in in/delivered/ (delivered.go)
//
// Claim moves in/ → in/claimed/ and returns EVERYTHING in in/claimed/;
// Deliver records one claimed message's identity and deletes it. A reader
// that claims and dies before it delivers leaves the file in in/claimed/, and
// the next Claim hands it out again: at-least-once is the substrate's floor,
// and a reservation that survived its reader without delivering would be a
// message permanently invisible. A reader therefore calls Deliver only for
// what it has actually DELIVERED — for the hook, what it wrote to the engine
// — never for what it merely returned.
//
// in/claimed/ is, like in/failed/, deliberately NOT a member of the closed
// Dir set: nothing rings a doorbell about a file landing there (the move is
// local to the reader that owns the directory), so it carries no wire
// obligation and must not grow the Dir/wire exhaustiveness pin.

// ClaimedDirName is the in-flight directory: an in/ entry a reader has taken
// and not yet acknowledged.
const ClaimedDirName Dir = "in/claimed"

// Claim takes every unclaimed message in harp's in/ into in/claimed/ and
// returns the whole in-flight set — what it just moved AND whatever an
// earlier Claim moved and never acknowledged — in filename (chronological)
// order, with each entry's Ref addressed where it now lives.
//
// Nothing is dropped between readdir and return: a file that is not a
// message is a Problem in the result, left where it was so an operator can
// still find it; a file that vanished between readdir and rename was taken
// by another reader, which is ordinary. A spool that was never created
// claims nothing and is not an error.
func Claim(m PathMapper, harp string) (SweepResult, error) {
	res := SweepResult{Dir: ClaimedDirName}
	inPath, err := DirPath(m, harp, DirIn)
	if err != nil {
		return res, err
	}
	claimedPath, err := DirPath(m, harp, ClaimedDirName)
	if err != nil {
		return res, err
	}
	// Swept — read and parsed — BEFORE anything is moved: a file that is not
	// a message stays in in/, where the operator tooling that reports
	// malformed spool files looks, rather than being carried into the
	// in-flight set and re-reported on every turn for the life of the session.
	unclaimed, err := sweepExisting(harp, DirIn, inPath)
	if err != nil {
		return res, fmt.Errorf("spool: claiming from %s: %w", inPath, err)
	}
	res.Problems = append(res.Problems, unclaimed.Problems...)
	if err := moveUnclaimed(m, harp, inPath, claimedPath, unclaimed.Entries); err != nil {
		return res, err
	}
	claimed, err := sweepExisting(harp, ClaimedDirName, claimedPath)
	if err != nil {
		return res, fmt.Errorf("spool: reading %s: %w", claimedPath, err)
	}
	if res.Entries, err = undelivered(m, harp, claimedPath, claimed.Entries); err != nil {
		return res, err
	}
	res.Problems = append(res.Problems, claimed.Problems...)
	return res, nil
}

// sweepExisting is sweepDir, with a directory that was never created
// sweeping as empty.
func sweepExisting(harp string, dir Dir, path string) (SweepResult, error) {
	res, err := sweepDir(harp, dir, path)
	if err != nil && !os.IsNotExist(err) {
		return res, err
	}
	return res, nil
}

// moveUnclaimed moves each unclaimed entry into claimed/ — or deletes it,
// unseen, when a copy of it was already delivered or is already in flight:
// the reader is a new process every turn, so the delivered record and the
// claimed/ directory are its only memory of what it has handed out. An entry
// another reader took first is ordinary.
func moveUnclaimed(m PathMapper, harp, inPath, claimedPath string, entries []Entry) error {
	var inFlight map[string]bool
	if len(entries) > 0 {
		var err error
		if inFlight, err = identitiesIn(m, harp, ClaimedDirName); err != nil {
			return err
		}
	}
	for _, e := range entries {
		from := filepath.Join(inPath, e.Ref.Name)
		id := e.Identity()
		delivered, err := Delivered(m, harp, id)
		if err != nil {
			return fmt.Errorf("spool: claiming %s: %w", e.Ref, err)
		}
		if delivered || inFlight[id] {
			if err := discard(from); err != nil {
				return fmt.Errorf("spool: dropping already-delivered copy %s: %w", e.Ref, err)
			}
			continue
		}
		inFlight[id] = true
		if err := renameInto(from, filepath.Join(claimedPath, e.Ref.Name)); err != nil && !errors.Is(err, ErrAlreadyGone) {
			return fmt.Errorf("spool: claiming %s: %w", e.Ref, err)
		}
	}
	return nil
}

// undelivered drops, from what Claim is about to return, every claimed entry
// whose identity is already recorded as delivered — a delivery whose delete
// was interrupted — finishing that delete.
func undelivered(m PathMapper, harp, claimedPath string, entries []Entry) ([]Entry, error) {
	out := entries[:0]
	for _, e := range entries {
		delivered, err := Delivered(m, harp, e.Identity())
		if err != nil {
			return nil, fmt.Errorf("spool: reading %s: %w", e.Ref, err)
		}
		if !delivered {
			out = append(out, e)
			continue
		}
		if err := discard(filepath.Join(claimedPath, e.Ref.Name)); err != nil {
			return nil, fmt.Errorf("spool: finishing the delivery of %s: %w", e.Ref, err)
		}
	}
	return out, nil
}

// discard deletes a copy that must not be delivered. One another reader
// already removed is the same outcome.
func discard(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncDir(filepath.Dir(path))
}

// Pending reports whether harp's in/ holds at least one unclaimed file — the
// only question a caller that is not the reader may ask of the owner's spool.
// A claimed message is spoken for and does not count; a spool that was never
// created has nothing pending.
func Pending(m PathMapper, harp string) (bool, error) {
	path, err := DirPath(m, harp, DirIn)
	if err != nil {
		return false, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("spool: peeking %s: %w", path, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			return true, nil
		}
	}
	return false, nil
}

// identitiesIn collects the Identity of every message in harp's dirs. A file
// there that does not parse is skipped rather than reported: Claim parses
// before it moves, so nothing unparseable got there by claiming, and the
// operator tooling that reports malformed files already looks at in/.
func identitiesIn(m PathMapper, harp string, dirs ...Dir) (map[string]bool, error) {
	seen := map[string]bool{}
	for _, d := range dirs {
		path, err := DirPath(m, harp, d)
		if err != nil {
			return nil, err
		}
		res, err := sweepDir(harp, d, path)
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("spool: reading %s: %w", path, err)
		}
		for _, e := range res.Entries {
			seen[e.Identity()] = true
		}
	}
	return seen, nil
}
