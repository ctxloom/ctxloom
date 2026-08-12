package spool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ErrAlreadyGone reports that a spool file is not at the ref the caller
// named: the other party won the race (consumed or withdrew it), or — across
// a container mount whose attribute cache is stale — it is not visible YET.
//
// It is a typed sentinel rather than a generic error because those two cases
// and a genuine failure demand different responses: ErrAlreadyGone means
// "retry or let the sweep handle it", never "the operation failed". A caller
// that treated an ENOENT as an error would turn every won race into a
// spurious alarm; one that treated it as success would drop messages.
var ErrAlreadyGone = errors.New("spool: file is no longer (or not yet) at that ref")

// Read decodes the message at ref in m's view.
//
// A missing file returns an error wrapping ErrAlreadyGone: a doorbell naming
// a file that is not there is the expected outcome of a lost race, and the
// caller answers it with a retry or a sweep, not a failure.
func Read(m PathMapper, ref Ref) (*Message, error) {
	path, err := m.Resolve(ref)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("spool: reading %s: %w", ref, ErrAlreadyGone)
		}
		return nil, fmt.Errorf("spool: reading %s: %w", ref, err)
	}
	msg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("spool: %s: %w", ref, err)
	}
	return msg, nil
}

// Consume marks a message consumed by RENAMING it into its direction's
// consumed/ directory, and returns the new ref.
//
// The rename IS the acknowledgement, and it is why there is no cursor file,
// no tombstone and no in-memory reservation ledger: rename is atomic, so
// exactly one consumer wins and the loser gets ErrAlreadyGone; the result is
// observable to the other side and to any human with `ls`; and restart
// recovery is a readdir.
//
// Consuming is a MOVE, never a delete. The consumed/ directory is the audit
// trail — what was delivered, in order, still readable — which a delete would
// destroy while leaving every test that only checks "gone from in/" green.
func Consume(m PathMapper, ref Ref) (Ref, error) {
	target, err := ref.Dir.Consumed()
	if err != nil {
		return Ref{}, err
	}
	return moveTo(m, ref, target)
}

// Withdraw retracts an unconsumed message by renaming it into
// in/withdrawn/, and returns the new ref.
//
// The race with the reader is resolved by the filesystem: rename-won means
// retracted, ErrAlreadyGone means the reader consumed it first (the caller's
// "pulled" answer). No lock, no tombstone, no ambiguity.
func Withdraw(m PathMapper, ref Ref) (Ref, error) {
	target, err := ref.Dir.Withdrawn()
	if err != nil {
		return Ref{}, err
	}
	return moveTo(m, ref, target)
}

// moveTo renames ref into dir, returning the new ref.
func moveTo(m PathMapper, ref Ref, dir Dir) (Ref, error) {
	from, err := m.Resolve(ref)
	if err != nil {
		return Ref{}, err
	}
	dst := Ref{Harp: ref.Harp, Dir: dir, Name: ref.Name}
	to, err := m.Resolve(dst)
	if err != nil {
		return Ref{}, err
	}
	if err := os.MkdirAll(filepath.Dir(to), dirPerm); err != nil {
		return Ref{}, fmt.Errorf("spool: create %s: %w", filepath.Dir(to), err)
	}
	if err := os.Rename(from, to); err != nil {
		if os.IsNotExist(err) {
			return Ref{}, fmt.Errorf("spool: moving %s to %s: %w", ref, dir, ErrAlreadyGone)
		}
		return Ref{}, fmt.Errorf("spool: moving %s to %s: %w", ref, dir, err)
	}
	if err := syncDir(filepath.Dir(to)); err != nil {
		return Ref{}, err
	}
	return dst, nil
}

// Entry is one message a sweep found, with the ref that names it.
type Entry struct {
	Ref     Ref
	Name    Name
	Message *Message
}

// Problem is one directory entry a sweep could NOT turn into a message: a
// filename outside the grammar, a file that would not parse, a file that
// would not read.
//
// Problems are RETURNED, not logged and forgotten, because a reader that
// silently skips what it cannot understand is this project's characteristic
// defect: the sweep reports success, the message never arrives, and every
// cheap signal says the system is healthy.
type Problem struct {
	// Ref names the offending file when its NAME was at least a legal path
	// segment; Path always names it.
	Ref  Ref
	Path string
	Err  error
}

// Error renders the problem for a log line.
func (p Problem) Error() string { return p.Path + ": " + p.Err.Error() }

// Unwrap exposes the underlying cause.
func (p Problem) Unwrap() error { return p.Err }

// SweepResult is one directory's worth of sweep: the messages, in filename
// (i.e. chronological) order, and everything that could not be read as one.
type SweepResult struct {
	Dir      Dir
	Entries  []Entry
	Problems []Problem
}

// ProblemErr joins every problem into one error, or returns nil when there
// were none. Callers that cannot act on individual problems still have no
// excuse for dropping them.
func (r SweepResult) ProblemErr() error {
	if len(r.Problems) == 0 {
		return nil
	}
	msgs := make([]string, 0, len(r.Problems))
	for _, p := range r.Problems {
		msgs = append(msgs, p.Error())
	}
	return fmt.Errorf("spool: %d unreadable file(s) in %s: %s", len(r.Problems), r.Dir, strings.Join(msgs, "; "))
}

// Sweep lists one spool directory in filename order and parses every file in
// it.
//
// The sweep is the at-least-once floor of the whole substrate: a doorbell
// only bounds latency, while a sweep re-derives the complete picture from
// readdir, which is what makes a lost notification harmless. It is also a
// first-class delivery path at startup, not merely doorbell-miss recovery — a
// coordinator or runner coming up cold drains its spool before any channel
// traffic exists.
//
// Sub-directories (consumed/, withdrawn/) are skipped as structure. EVERY
// other entry is either an Entry or a Problem; nothing is dropped in between.
func Sweep(m PathMapper, harp string, dir Dir) (SweepResult, error) {
	res := SweepResult{Dir: dir}
	path, err := DirPath(m, harp, dir)
	if err != nil {
		return res, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return res, fmt.Errorf("spool: sweeping %s: %w", path, err)
	}
	names := make([]string, 0, len(entries))
	isDir := make(map[string]bool, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
		isDir[e.Name()] = e.IsDir()
	}
	sort.Strings(names)
	for _, name := range names {
		if isDir[name] {
			continue
		}
		full := filepath.Join(path, name)
		ref := Ref{Harp: harp, Dir: dir, Name: name}
		parsed, err := ParseName(name)
		if err != nil {
			res.Problems = append(res.Problems, Problem{Path: full, Err: err})
			continue
		}
		data, err := os.ReadFile(full)
		if err != nil {
			if os.IsNotExist(err) {
				// Consumed or withdrawn between readdir and read: the other
				// party won, exactly as a doorbell race would resolve.
				continue
			}
			res.Problems = append(res.Problems, Problem{Ref: ref, Path: full, Err: err})
			continue
		}
		msg, err := Parse(data)
		if err != nil {
			res.Problems = append(res.Problems, Problem{Ref: ref, Path: full, Err: err})
			continue
		}
		res.Entries = append(res.Entries, Entry{Ref: ref, Name: parsed, Message: msg})
	}
	return res, nil
}
