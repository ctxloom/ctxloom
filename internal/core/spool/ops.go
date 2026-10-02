package spool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/owneronly"
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

// Consume marks an out/ message routed by RENAMING it into out/consumed/,
// and returns the new ref.
//
// The rename IS the acknowledgement: it is atomic, so exactly one consumer
// wins and the loser gets ErrAlreadyGone; the result is observable to the
// other side and to any human with `ls`; and restart recovery is a readdir.
// It is a move, never a delete, because out/consumed/ is how an operator
// tells a routed message from a refused one (out/failed/). An inbox message
// is not consumed this way: it is delivered with Deliver.
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

// FailedDirName is the terminal directory for an in/ entry this process's
// own reader parsed as a message (Sweep already returned it as an Entry, not
// a Problem) but could not otherwise classify or deliver: an unknown or
// future mailbox kind, a structured payload that will not decode at the
// delivery seam, or any comparable semantic rejection above this package's
// own parse layer.
//
// It sits beside in/, exactly like consumed/ and withdrawn/, but is
// deliberately NOT a member of the closed Dir set: Ref.Validate must keep
// refusing it, because nothing ever rings a doorbell about landing here — the
// move is entirely local to the reader that could not act on the file — so
// unlike every value in Dirs() it carries no wire obligation. Adding it to
// the closed set would grow the Dir/wire exhaustiveness pin
// (TestSpoolDoorbell_EnumExhaustiveBothDirections) for a directory the wire
// protocol has no reason to know about. It is a reader-local directory
// (LocalDirs): addressable through DirPath and Sweep, never through a Ref.
const FailedDirName Dir = "in/failed"

// FailedOutDirName is the same terminal state for the OTHER direction: an
// out/ entry the coordinator's sweep parsed but could not route.
//
// It exists because consumed/ has one meaning and a reader must be able to
// trust it. out/consumed/ says "the coordinator routed this to its
// recipient"; a message the sweep gave up on used to be renamed there too,
// which made a delivered report and a dropped one indistinguishable on disk —
// and reading a dropped report's presence in out/consumed/ as proof it had
// been taken is exactly how a lost report was misdiagnosed as an agent that
// never wrote one.
const FailedOutDirName Dir = "out/failed"

// FailedDirNames returns every terminal failed/ directory, so a scanner that
// wants to surface refused messages enumerates them from the authority rather
// than from a second hand-kept list. A direction added here and to nothing
// else must make an operator's view incomplete LOUDLY, at a compile or a test,
// not by quietly not being looked at.
func FailedDirNames() []Dir { return []Dir{FailedDirName, FailedOutDirName} }

// failedDirFor maps a live direction — or the owner's in-flight in/claimed/,
// whose entries are still in/ messages the reader has not delivered — onto
// its terminal failed/ sibling.
func failedDirFor(d Dir) (Dir, error) {
	switch d {
	case DirIn, ClaimedDirName:
		return FailedDirName, nil
	case DirOut:
		return FailedOutDirName, nil
	default:
		return "", fmt.Errorf("spool: only an %q, %q or %q entry can be marked failed, got %q", string(DirIn), string(ClaimedDirName), string(DirOut), string(d))
	}
}

// Fail moves ref — a live in/ or out/ entry — into its direction's local
// failed/ terminal directory: an entry PRESENT ON DISK and UNDELIVERED,
// distinct both from "not found" (nothing ever arrived) and from consumed/
// (delivered). Like Consume and Withdraw this is a rename, never a delete or
// an in-place rewrite: a delete would erase the one record that the message
// ever arrived, and an edit would give a reader two versions of the same
// file to disagree about.
//
// Unlike Consume, nothing on the other end of a channel is watching for
// this: it returns no Ref for a caller to announce, and neither failed/
// directory may ever reach SpoolDirToWire (which refuses them — see
// FailedOutDirName and FailedDirName).
func Fail(m PathMapper, ref Ref) error {
	target, err := failedDirFor(ref.Dir)
	if err != nil {
		return err
	}
	if err := ValidateName(ref.Name); err != nil {
		return err
	}
	// Through DirPath rather than Resolve: a claimed entry's ref names a
	// reader-local directory that is, by design, not a wire value.
	fromDir, err := DirPath(m, ref.Harp, ref.Dir)
	if err != nil {
		return err
	}
	toDir, err := DirPath(m, ref.Harp, target)
	if err != nil {
		return err
	}
	if err := renameInto(filepath.Join(fromDir, ref.Name), filepath.Join(toDir, ref.Name)); err != nil {
		return fmt.Errorf("spool: moving %s to %s: %w", ref, target, err)
	}
	return nil
}

// renameInto is the ONE raw-filesystem move in this package: create the
// destination directory, rename, then fsync the directory so the rename is
// durable. Every spool transition (consume, withdraw, fail) is a rename, so
// they all land here — a second copy of this sequence would be a second place
// for the durability fsync to be forgotten.
//
// It reports a missing source as ErrAlreadyGone: another sweep winning the
// race is ordinary, not a fault.
func renameInto(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), owneronly.DirMode); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(to), err)
	}
	if err := os.Rename(from, to); err != nil {
		if os.IsNotExist(err) {
			return ErrAlreadyGone
		}
		return err
	}
	return syncDir(filepath.Dir(to))
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
	if err := renameInto(from, to); err != nil {
		return Ref{}, fmt.Errorf("spool: moving %s to %s: %w", ref, dir, err)
	}
	return dst, nil
}

// Entry is one message a sweep found, with the ref that names it.
type Entry struct {
	Ref     Ref
	Name    Name
	Message *Message
}

// Identity is the one id every reader agrees a swept message answers to: the
// producer's OriginID when it carried one, else the filename stem. It is the
// dedupe key, so a producer's re-send — a NEW file carrying the SAME origin —
// is recognised as the message it repeats.
func (e Entry) Identity() string {
	if e.Message != nil && e.Message.OriginID != "" {
		return e.Message.OriginID
	}
	return e.Name.Stem()
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
	path, err := DirPath(m, harp, dir)
	if err != nil {
		return SweepResult{Dir: dir}, err
	}
	res, err := sweepDir(harp, dir, path)
	if err != nil {
		return res, fmt.Errorf("spool: sweeping %s: %w", path, err)
	}
	return res, nil
}

// sweepDir is Sweep's body over an already-resolved directory path. The
// readdir error is returned bare so a caller that treats "not there" as
// empty (Claim, for a spool nothing has written to yet) can tell it apart.
func sweepDir(harp string, dir Dir, path string) (SweepResult, error) {
	res := SweepResult{Dir: dir}
	names, isDir, err := sortedDirEntries(path)
	if err != nil {
		return res, err
	}
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

// sortedDirEntries lists path's entries in filename (sort.Strings) order,
// alongside which of them are sub-directories (structure, never a message).
func sortedDirEntries(path string) (names []string, isDir map[string]bool, err error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, nil, err
	}
	names = make([]string, 0, len(entries))
	isDir = make(map[string]bool, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
		isDir[e.Name()] = e.IsDir()
	}
	sort.Strings(names)
	return names, isDir, nil
}
