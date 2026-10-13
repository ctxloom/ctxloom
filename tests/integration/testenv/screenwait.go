package testenv

import (
	"fmt"
	"strings"
	"time"
)

// Unexpected names a screen a wait must not sit through: once Signature is
// in the output, what the wait is for is not coming (an interview that shows
// an engine menu its step never answers waits on that menu forever). Name is
// what the failure calls it.
type Unexpected struct {
	Name      string
	Signature string
}

// screenVerdict is how a screen wait ended.
type screenVerdict int

const (
	screenWanted     screenVerdict = iota // want held
	screenUnexpected                      // an Unexpected screen showed first
	screenExited                          // the process exited first
	screenExpired                         // the budget ran out
)

// awaitScreen re-checks the output on every write until want holds, one of
// unexpected shows, the process exits, or expired fires — whichever is first.
// want is checked before unexpected, and once more after the exit (whose
// report waits for the drain, so the process's last write is in by then).
// want nil never holds: the wait is then for the exit.
func (s *PTYSession) awaitScreen(expired <-chan time.Time, want func(string) bool, unexpected []Unexpected) (string, screenVerdict, Unexpected) {
	exited := s.exited
	for {
		cur, written := s.out.next()
		if want != nil && want(cur) {
			return cur, screenWanted, Unexpected{}
		}
		for _, u := range unexpected {
			if strings.Contains(cur, u.Signature) {
				return cur, screenUnexpected, u
			}
		}
		if exited == nil {
			return cur, screenExited, Unexpected{}
		}
		select {
		case <-written:
		case <-exited:
			exited = nil // one last look at the settled output, then report the exit
		case <-expired:
			return s.out.String(), screenExpired, Unexpected{}
		}
	}
}

// WaitForScreen waits up to budget for want to hold on the output, and fails
// FAST and NAMED instead of spending the budget when the screen it is waiting
// on cannot come: one of unexpected shows (the error names it and quotes its
// signature), or the process exits first (the error gives its exit code).
// what describes the awaited screen for the error ("init's question %q").
// Every error carries the captured output.
func (s *PTYSession) WaitForScreen(budget time.Duration, what string, want func(string) bool, unexpected ...Unexpected) error {
	out, verdict, u := s.awaitScreen(time.After(budget), want, unexpected)
	switch verdict {
	case screenWanted:
		return nil
	case screenUnexpected:
		return fmt.Errorf("waiting for %s, the screen showed %s (%q) instead; captured output:\n%s", what, u.Name, u.Signature, out)
	case screenExited:
		return fmt.Errorf("waiting for %s, the process exited (code %d) first; captured output:\n%s", what, s.ExitCode(), out)
	default:
		return fmt.Errorf("%s never showed within %s; captured output:\n%s", what, budget, out)
	}
}

// WaitForExitUnless is Wait that fails fast and named when one of unexpected
// shows before the exit — a prompt nobody will answer. exitErr is the
// process's Wait error once it exited; err is non-nil when it did not.
func (s *PTYSession) WaitForExitUnless(budget time.Duration, unexpected ...Unexpected) (exitErr, err error) {
	out, verdict, u := s.awaitScreen(time.After(budget), nil, unexpected)
	switch verdict {
	case screenExited:
		return s.exitErr, nil
	case screenUnexpected:
		return nil, fmt.Errorf("waiting for the process to exit, the screen showed %s (%q), which nothing will answer; captured output:\n%s", u.Name, u.Signature, out)
	default:
		return nil, fmt.Errorf("the process never exited within %s; captured output:\n%s", budget, out)
	}
}
