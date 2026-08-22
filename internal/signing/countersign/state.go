package countersign

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/signing"
)

// StoreState is the RESOLVED state of one physical countersignature store.
// A store is in exactly one of four, and only the last of them may yield a
// trust verdict:
//
//	StateUnconfigured  nobody supplied a directory at all
//	StateAbsent        the directory is set and does not exist
//	StateUnreadable    the directory exists but cannot be listed, or a
//	                   record inside it cannot be read
//	StateReadable      the directory exists and every record in it parses
//
// The type exists because the boolean shape that preceded it (Readable's
// error / no-error) could not tell StateAbsent from StateReadable-and-empty:
// both answered "fine, nothing recorded". Those are different facts with
// opposite security consequences — an approvals volume that failed to mount
// presents as StateAbsent, and reading it as "nothing recorded" discards
// every rejection a human wrote while the operator believes the decision is
// in force.
type StoreState string

const (
	// StateUnconfigured is a Store over dir "" (or a nil *Store): nobody
	// managed to configure it. The live trigger is $HOME being unresolvable
	// under a systemd unit, `env -i`, or a container with no HOME, which
	// leaves the operations-level user-store construction with no directory
	// to hand over. Never a value — see configured.
	StateUnconfigured StoreState = "unconfigured"
	// StateAbsent is a directory that is set and does not exist. It covers
	// BOTH "nothing has ever been recorded here" (the fresh-user shape) and
	// "the store this process should be reading failed to mount" — the two
	// are indistinguishable from the filesystem alone, which is precisely
	// why the state is named rather than folded into StateReadable.
	StateAbsent StoreState = "absent"
	// StateUnreadable is a directory that exists but cannot be listed
	// (permission denied, I/O error), or that lists and holds a record file
	// this process cannot open or that will not parse. A store this process
	// cannot see might be hiding a REJECTION, and rejection is supreme
	// (spec §9.3).
	StateUnreadable StoreState = "unreadable"
	// StateReadable is the only state that may produce a trust verdict.
	StateReadable StoreState = "readable"
)

// Resolve reports which of the four states this store is in, together with
// the error naming the fault for the three that are faults. The error is nil
// if and ONLY if the state is StateReadable, so a caller may branch on either
// and get the same answer.
//
// Resolve does NOT verify any signature. A record that parses but fails
// cryptographic verification (untrusted signer, bytes that no longer match)
// is StateReadable: that is the normal "not proven" outcome the verify path
// exists to report, and treating it as a store fault would deny everything
// for every session carrying one stale record.
//
// A nil *Store resolves StateUnconfigured — there is no store here. Note that
// Store.Readable deliberately does NOT agree with Resolve on nil (nor on
// StateAbsent); see its own doc for the two behaviours it keeps and why.
func (s *Store) Resolve() (StoreState, error) {
	if s == nil {
		return StateUnconfigured, fmt.Errorf("countersignature store: no store")
	}
	if err := s.configured(); err != nil {
		return StateUnconfigured, err
	}
	entries, err := afero.ReadDir(s.fs, s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return StateAbsent, fmt.Errorf("countersignature store %s: the directory does not exist", s.dir)
		}
		return StateUnreadable, fmt.Errorf("countersignature store %s: %w", s.dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(s.dir, entry.Name())
		data, ferr := afero.ReadFile(s.fs, path)
		if ferr != nil {
			return StateUnreadable, fmt.Errorf("countersignature store %s: cannot read %s: %w", s.dir, entry.Name(), ferr)
		}
		if filepath.Ext(entry.Name()) != ".sig" {
			continue
		}
		if perr := signing.ParseArmored(data); perr != nil {
			return StateUnreadable, fmt.Errorf("countersignature store %s: record %s is unparseable (a corrupted record cannot be told apart from a suppressed rejection): %w", s.dir, entry.Name(), perr)
		}
	}
	return StateReadable, nil
}
