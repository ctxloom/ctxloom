package cli

import (
	"context"
	"errors"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// endReasonOf is why a run that ended with err ended, for its session's
// record (sessions.EndReason). The exit status is clifmt.ExitCodeOf: the same
// function that decides the process's own, so the record cannot disagree with
// what the shell saw. The class is that status's meaning, except that a run
// ended by its context (a signal) is cancelled whatever status followed.
//
// Message is what ctxloom printed: the error and its fix as cobrafmt.Report
// writes them, or for a fatal-findings abort the listing's header with
// Findings carrying each finding as printed. An ExitStatus that is not an
// abort (a status relayed from a wrapped process) printed nothing.
func endReasonOf(err error) sessions.EndReason {
	exit := clifmt.ExitCodeOf(err)
	reason := sessions.EndReason{Exit: exit, Class: endClassOf(err, exit)}
	var abort findingsAbort
	var silent clifmt.ExitStatus
	switch {
	case errors.As(err, &abort):
		reason.Message = abort.Header
		reason.Findings = abort.Items
	case errors.As(err, &silent):
	default:
		fix, _ := clifmt.RemedyOf(err)
		reason.Message = err.Error() + clifmt.FixLine("  ", fix)
	}
	return reason
}

// endClassOf names what exit means for a run that ended with err.
func endClassOf(err error, exit int) string {
	switch {
	case errors.Is(err, context.Canceled):
		return sessions.EndClassCancelled
	case exit == exitCodeFatalFindings:
		return sessions.EndClassFatalStartup
	case exit == exitCodeRefused:
		return sessions.EndClassRefused
	default:
		return sessions.EndClassFailed
	}
}

// recordEndReasonUnlaunched records why the run's session ended when the run
// failed before its engine launched. A session whose engine launched is
// covered by the engine's transcript and status, so nothing is written.
func (st *runState) recordEndReasonUnlaunched(err error) {
	if err == nil || st.engineLaunched || st.mintedHarp == "" {
		return
	}
	st.recordEndReason(endReasonOf(err))
}

// recordCredentialRefused records the end of a run whose engine refused its
// credential after launch. The coordinator's CREDENTIAL REFUSED notice, with
// its remedy, was printed live; the record points at it rather than copying a
// notice this process holds only as holds.
func (st *runState) recordCredentialRefused() {
	st.recordEndReason(sessions.EndReason{
		Exit:    exitCodeRefused,
		Class:   sessions.EndClassRefused,
		Message: "the engine refused its credential after launch (see the CREDENTIAL REFUSED notice)",
	})
}

// recordEndReason writes reason onto the session this run minted. A failed
// write warns: the run's own outcome stands.
func (st *runState) recordEndReason(reason sessions.EndReason) {
	if st.mintedHarp == "" {
		return
	}
	if err := operations.RecordEndReason(st.mintedHarp, reason); err != nil {
		clidiag.Warn("ctxloom", "session end reason not recorded: %v", err)
	}
}
