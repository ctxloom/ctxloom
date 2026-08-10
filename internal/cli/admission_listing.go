package cli

import (
	"fmt"
	"io"
)

// printAdmissionListings renders recorded trust decisions, one per line: the
// state, then the caller's description of the thing decided about. Every
// admission store — companion binaries, publish destinations — answers the
// same two questions in the same shape, and they must not drift apart into
// two spellings of "allowed".
//
// An empty store says so IN WORDS rather than printing nothing. "no output"
// and "nothing recorded" are the same pixels and very different facts, and a
// silent empty listing is indistinguishable from a listing that failed.
func printAdmissionListings[T any](w io.Writer, listings []T, empty string, allowed func(T) bool, describe func(T) string) error {
	if len(listings) == 0 {
		_, err := fmt.Fprintln(w, empty)
		return err
	}
	for _, l := range listings {
		state := "allowed"
		if !allowed(l) {
			state = "declined"
		}
		if _, err := fmt.Fprintf(w, "%-10s %s\n", state, describe(l)); err != nil {
			return err
		}
	}
	return nil
}

// printForgetResult reports how many recorded decisions a withdrawal removed.
//
// Zero removed is stated, never reported as a bare success: the caller asked
// to undo something, and "I removed nothing" is the one outcome they cannot
// infer from an exit code. A withdrawal that changed nothing usually means the
// name was spelled differently from the record.
func printForgetResult(w io.Writer, removed int, subject string) error {
	if removed == 0 {
		_, err := fmt.Fprintf(w, "forgot 0 decisions — nothing recorded for %s\n", subject)
		return err
	}
	_, err := fmt.Fprintf(w, "forgot %d decision(s) for %s\n", removed, subject)
	return err
}
