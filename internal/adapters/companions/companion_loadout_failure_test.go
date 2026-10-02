package companions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

const ltkGuardLoadout = `
mcp:
  ltk-guard:
    command: ltk
    args: ["mcp"]
`

// exitErr returns a real *exec.ExitError from a process that exited with a
// status — the shape a companion with no `loadout` subcommand answers in.
func exitErr(t *testing.T, script string) error {
	t.Helper()
	err := exec.Command("sh", "-c", script).Run()
	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee, "the fixture must produce a real exit error")
	return err
}

func TestClassifyLoadoutProbe_SplitsAnAnswerFromAFailure(t *testing.T) {
	exited := exitErr(t, "exit 3")
	killed := exitErr(t, "kill -9 $$")
	cases := []struct {
		name string
		err  error
		want error
	}{
		{"a clean non-zero exit is an answer: no loadout support", exited, ErrLoadoutUnsupported},
		{"a process killed by a signal never answered", killed, ErrLoadoutProbeFailed},
		{"a timeout never answered", context.DeadlineExceeded, ErrLoadoutProbeFailed},
		{"an exit caused by the timeout's kill is still the timeout", fmt.Errorf("%w: %w", context.DeadlineExceeded, exited), ErrLoadoutProbeFailed},
		{"an exec failure never answered", exec.ErrNotFound, ErrLoadoutProbeFailed},
		{"an exit error with no process state shows no answer", &exec.ExitError{}, ErrLoadoutProbeFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyLoadoutProbe(tc.err)
			require.ErrorIs(t, got, tc.want)
			other := ErrLoadoutUnsupported
			if tc.want == ErrLoadoutUnsupported {
				other = ErrLoadoutProbeFailed
			}
			assert.NotErrorIs(t, got, other, "the two kinds are exclusive")
			assert.ErrorIs(t, got, tc.err, "the underlying error is kept for the warning")
		})
	}
}

// probeLtk drives one probe of an admitted ltk whose loadout exec answers
// with out/err, returning the probe's account and every warning it printed.
func probeLtk(t *testing.T, out []byte, err error) (bundles.CompanionProbe, string) {
	t.Helper()
	t.Cleanup(SetCompanionLoadoutOutputForTesting(func(string) ([]byte, error) { return out, err }))
	var warnings bytes.Buffer
	restore := clidiag.SetSink(&warnings)
	defer restore()
	probe, perr := Prober{}.ProbeCompanionLoadouts(context.Background(), nil)
	require.NoError(t, perr)
	return probe, warnings.String()
}

func ltkCandidate(probe bundles.CompanionProbe) (bundles.CompanionCandidate, bool) {
	for _, c := range probe.Candidates {
		if c.Bin == "ltk" {
			return c, true
		}
	}
	return bundles.CompanionCandidate{}, false
}

func ltkFixture(t *testing.T) []byte {
	t.Helper()
	admitEveryDiscoveredCompanion(t)
	t.Cleanup(SetLookPathForTesting(lookPathOnly(map[string]string{"ltk": "/fake/ltk"})))
	envelope, err := fakeCompanionEnvelope(t, ltkGuardLoadout)("")
	require.NoError(t, err)
	return envelope
}

// TestProbeLoadout_FailedProbe_CarriesLastKnownLoadoutForward is
// unread-spectrum's follow-on: a VERIFIED companion whose probe errors or
// times out did not answer, so what it contributes is UNKNOWN, not empty. Its
// last-known loadout is carried forward unchanged — every surface it fed is
// rendered with the same entries — and the user is told which companion and
// what to do.
func TestProbeLoadout_FailedProbe_CarriesLastKnownLoadoutForward(t *testing.T) {
	cases := []struct {
		name    string
		failure func(t *testing.T) ([]byte, error)
	}{
		{"timeout", func(*testing.T) ([]byte, error) { return nil, fmt.Errorf("%w", context.DeadlineExceeded) }},
		{"exec error", func(*testing.T) ([]byte, error) { return nil, exec.ErrNotFound }},
		{"signal", func(t *testing.T) ([]byte, error) { return nil, exitErr(t, "kill -9 $$") }},
		{"killed on exit", func(t *testing.T) ([]byte, error) {
			return nil, fmt.Errorf("%w: %w", context.DeadlineExceeded, exitErr(t, "exit 1"))
		}},
		{"bad envelope", func(*testing.T) ([]byte, error) { return []byte("not an envelope"), nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			envelope := ltkFixture(t)
			good, _ := probeLtk(t, envelope, nil)
			require.Len(t, good.Loadouts, 1, "the seed probe must succeed for the carry to prove anything")

			out, failure := tc.failure(t)
			probe, warned := probeLtk(t, out, failure)

			require.Len(t, probe.Loadouts, 1, "the companion's last-known loadout is carried forward")
			assert.Equal(t, good.Loadouts[0], probe.Loadouts[0], "carried UNCHANGED")
			_, isCand := ltkCandidate(probe)
			assert.False(t, isCand, "a carried companion is a read, not a candidate")
			assert.Contains(t, warned, `companion "ltk"`)
			assert.Contains(t, warned, "last-known loadout")
			assert.Contains(t, warned, "/fake/ltk loadout --format json", "the remedy names the command that must answer")
		})
	}
}

// TestProbeLoadout_FailedProbe_NoRecord_ContributesNothingAndSaysSo: with no
// earlier loadout there is nothing to carry; the companion is a probe-failed
// candidate and the warning says it contributes nothing this time.
func TestProbeLoadout_FailedProbe_NoRecord_ContributesNothingAndSaysSo(t *testing.T) {
	ltkFixture(t)
	probe, warned := probeLtk(t, nil, context.DeadlineExceeded)

	assert.Empty(t, probe.Loadouts)
	cand, ok := ltkCandidate(probe)
	require.True(t, ok)
	assert.Equal(t, bundles.CandidateProbeFailed, cand.Reason)
	assert.Contains(t, warned, `companion "ltk"`)
	assert.Contains(t, warned, "no earlier loadout")
}

// TestProbeLoadout_NoLoadoutAnswer_ContributesNothingAndForgets: a clean
// "no loadout support" answer is a FACT about the companion — it contributes
// nothing, silently — and the record of what it used to contribute is
// dropped, so a later failure cannot resurrect content it no longer offers.
func TestProbeLoadout_NoLoadoutAnswer_ContributesNothingAndForgets(t *testing.T) {
	envelope := ltkFixture(t)
	_, _ = probeLtk(t, envelope, nil)

	probe, warned := probeLtk(t, nil, exitErr(t, "exit 2"))
	assert.Empty(t, probe.Loadouts, "no loadout support contributes nothing")
	cand, ok := ltkCandidate(probe)
	require.True(t, ok)
	assert.Equal(t, bundles.CandidateNoLoadout, cand.Reason)
	assert.Empty(t, warned, "an answer is ordinary, not a warning")

	later, _ := probeLtk(t, nil, context.DeadlineExceeded)
	assert.Empty(t, later.Loadouts, "the record went with the answer")
}

// TestLastKnownLoadout_RefusesANameThatIsNotABareFileName pins the record's
// keying: a companion name is a directory entry, and the record is one file
// per name, so a name that would leave the record directory is refused.
func TestLastKnownLoadout_RefusesANameThatIsNotABareFileName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := lastKnownLoadoutPath("../escape")
	require.Error(t, err)
	assert.True(t, errors.Is(err, errLoadoutRecordName))
}

// TestCompanionLoadoutOutput_TimeoutIsAFailureNotAnAnswer drives the REAL
// exec seam against a companion that never answers: the timeout's kill must
// classify as a failed probe, or a wedged companion would be read as one
// offering no loadout and its entries stripped.
func TestCompanionLoadoutOutput_TimeoutIsAFailureNotAnAnswer(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "wedged")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755)) //nolint:gosec // an executable fixture
	prevTimeout, prevDelay := companionProbeTimeout, companionProbeWaitDelay
	companionProbeTimeout, companionProbeWaitDelay = 100*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() { companionProbeTimeout, companionProbeWaitDelay = prevTimeout, prevDelay })

	_, err := companionLoadoutOutput(bin)

	require.Error(t, err)
	assert.ErrorIs(t, classifyLoadoutProbe(err), ErrLoadoutProbeFailed)
}

// TestProbeLoadout_RecordFollowsTheLatestAnswer: the record is the loadout
// the companion LAST gave, so a changed answer replaces it and a later failure
// carries the new one, not the first.
func TestProbeLoadout_RecordFollowsTheLatestAnswer(t *testing.T) {
	first := ltkFixture(t)
	_, _ = probeLtk(t, first, nil)
	second, err := fakeCompanionEnvelope(t, "mcp:\n  ltk-other:\n    command: ltk\n")("")
	require.NoError(t, err)
	latest, _ := probeLtk(t, second, nil)
	require.Len(t, latest.Loadouts, 1)

	carried, _ := probeLtk(t, nil, context.DeadlineExceeded)

	require.Len(t, carried.Loadouts, 1)
	assert.Equal(t, latest.Loadouts[0], carried.Loadouts[0])
}
