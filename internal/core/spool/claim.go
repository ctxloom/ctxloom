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
//	in/consumed/  acknowledged: delivered   — the audit trail, never pruned here
//
// Claim moves in/ → in/claimed/ and returns EVERYTHING in in/claimed/; Ack
// moves one file in/claimed/ → in/consumed/. A reader that claims and dies
// before it acks leaves the file in in/claimed/, and the next Claim hands it
// out again: at-least-once is the substrate's floor, and a reservation that
// survived its reader without delivering would be a message permanently
// invisible. A reader therefore acks only what it has actually DELIVERED —
// for the hook, what it wrote to the engine — never what it merely returned.
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
	unclaimed, err := sweepDir(harp, DirIn, inPath)
	if err != nil && !os.IsNotExist(err) {
		return res, fmt.Errorf("spool: claiming from %s: %w", inPath, err)
	}
	res.Problems = append(res.Problems, unclaimed.Problems...)
	var seen map[string]bool
	if len(unclaimed.Entries) > 0 {
		if seen, err = identitiesIn(m, harp, DirInConsumed, ClaimedDirName); err != nil {
			return res, err
		}
	}
	consumedPath, err := DirPath(m, harp, DirInConsumed)
	if err != nil {
		return res, err
	}
	for _, e := range unclaimed.Entries {
		// A copy of something already delivered, or already in flight, is
		// acknowledged unseen: the reader is a new process every turn, so
		// the directories are its only memory of what it has handed out.
		to := claimedPath
		if id := e.Identity(); seen[id] {
			to = consumedPath
		} else {
			seen[id] = true
		}
		if err := renameInto(filepath.Join(inPath, e.Ref.Name), filepath.Join(to, e.Ref.Name)); err != nil && !errors.Is(err, ErrAlreadyGone) {
			return res, fmt.Errorf("spool: claiming %s: %w", e.Ref, err)
		}
	}
	claimed, err := sweepDir(harp, ClaimedDirName, claimedPath)
	if err != nil && !os.IsNotExist(err) {
		return res, fmt.Errorf("spool: reading %s: %w", claimedPath, err)
	}
	res.Entries = claimed.Entries
	res.Problems = append(res.Problems, claimed.Problems...)
	return res, nil
}

// Ack acknowledges one claimed message by renaming in/claimed/<name> into
// in/consumed/<name>. The rename is the acknowledgement, exactly as Consume's
// is for a live entry, and for the same reasons: atomic, observable with
// `ls`, and a move rather than a delete so the audit trail survives.
//
// A name not in in/claimed/ is ErrAlreadyGone — another reader acknowledged
// it first, or it was never claimed — and never a generic failure.
func Ack(m PathMapper, harp, name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	from, err := DirPath(m, harp, ClaimedDirName)
	if err != nil {
		return err
	}
	to, err := DirPath(m, harp, DirInConsumed)
	if err != nil {
		return err
	}
	if err := renameInto(filepath.Join(from, name), filepath.Join(to, name)); err != nil {
		return fmt.Errorf("spool: acknowledging %s: %w", name, err)
	}
	return nil
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
