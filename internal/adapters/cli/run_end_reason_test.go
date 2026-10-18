package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// TestEndReasonOf_ClassesFollowTheExitStatus: all four classes, each from the
// error that ends a run that way, with the exit status the process would exit
// with (clifmt.ExitCodeOf) recorded beside it.
func TestEndReasonOf_ClassesFollowTheExitStatus(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantExit  int
		wantClass string
	}{
		{"a refusal", refusal{errors.New("no credential")}, exitCodeRefused, sessions.EndClassRefused},
		{"a fatal-findings abort", findingsAbort{ExitStatus: clifmt.ExitStatus{Code: exitCodeFatalFindings}}, exitCodeFatalFindings, sessions.EndClassFatalStartup},
		{"an ordinary error", errors.New("resolve failed"), exitCodeError, sessions.EndClassFailed},
		{"a cancelled run", fmt.Errorf("start: %w", context.Canceled), exitCodeError, sessions.EndClassCancelled},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := endReasonOf(c.err)
			assert.Equal(t, c.wantExit, got.Exit)
			assert.Equal(t, c.wantClass, got.Class)
			assert.Equal(t, clifmt.ExitCodeOf(c.err), got.Exit, "the record's exit is the process's exit")
		})
	}
}

// TestEndReasonOf_MessageIsWhatWasPrinted: an ordinary error carries its text
// and its fix as cobrafmt.Report prints them; a relayed silent status printed
// nothing.
func TestEndReasonOf_MessageIsWhatWasPrinted(t *testing.T) {
	got := endReasonOf(refusal{report.Error{Msg: "no credential", Fix: "log in"}})
	assert.Equal(t, "no credential\n  fix: log in", got.Message)

	assert.Empty(t, endReasonOf(clifmt.ExitStatus{Code: 7}).Message)
}

// TestGateAbort_RecordsTheFindingsAsPrinted: the abort a closed gate returns
// carries the listing's header and each finding exactly as the listing
// printed it, so the record cannot differ from the terminal.
func TestGateAbort_RecordsTheFindingsAsPrinted(t *testing.T) {
	resetStrictness(t)
	var out bytes.Buffer
	g := newPhaseGates(&out, strictness.Mode{})
	strictness.Fail(report.KindBundle, "repair the bundle", "failed to load bundle \"x\"")

	err := g.close(PhaseStartup)

	got := endReasonOf(err)
	require.Equal(t, sessions.EndClassFatalStartup, got.Class)
	assert.Equal(t, exitCodeFatalFindings, got.Exit)
	require.Len(t, got.Findings, 1)
	assert.Contains(t, got.Findings[0], "failed to load bundle \"x\"")
	assert.Contains(t, got.Findings[0], "fix: repair the bundle")
	assert.Contains(t, got.Message, "aborting startup")
	assert.Equal(t, out.String(), got.Message+"\n"+got.Findings[0]+"\n", "header and items are exactly the printed listing")
}
