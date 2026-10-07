package companions

import (
	"bytes"
	"context"
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
	admitEveryDiscoveredCompanion(t)
	t.Cleanup(SetLookPathForTesting(lookPathOnly(map[string]string{"ltk": "/fake/ltk"})))
	t.Cleanup(SetCompanionLoadoutOutputForTesting(func(string) ([]byte, error) { return out, err }))
	var warnings bytes.Buffer
	restore := clidiag.SetSink(&warnings)
	defer restore()
	probe, perr := Prober{}.ProbeCompanionLoadouts(context.Background())
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

// TestProbeLoadout_FailedProbe_WarnsAndContributesNothing is unread-spectrum's
// follow-on: a VERIFIED companion whose probe errors or times out never
// answered, so what it contributes is UNKNOWN. It contributes nothing this
// time, and the user is told which companion and what must answer.
func TestProbeLoadout_FailedProbe_WarnsAndContributesNothing(t *testing.T) {
	cases := []struct {
		name    string
		failure func(t *testing.T) ([]byte, error)
	}{
		{"timeout", func(*testing.T) ([]byte, error) { return nil, fmt.Errorf("%w", context.DeadlineExceeded) }},
		{"exec error", func(*testing.T) ([]byte, error) { return nil, exec.ErrNotFound }},
		{"signal", func(t *testing.T) ([]byte, error) { return nil, exitErr(t, "kill -9 $$") }},
		{"printed nothing", func(*testing.T) ([]byte, error) { return nil, nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, failure := tc.failure(t)
			probe, warned := probeLtk(t, out, failure)

			assert.Empty(t, probe.Loadouts)
			cand, ok := ltkCandidate(probe)
			require.True(t, ok)
			assert.Equal(t, bundles.CandidateProbeFailed, cand.Reason)
			assert.Contains(t, warned, `companion "ltk"`)
			assert.Contains(t, warned, "/fake/ltk loadout --format yaml", "the remedy names the command that must answer")
		})
	}
}

// TestProbeLoadout_NoLoadoutAnswer_ContributesNothingQuietly: a clean "no
// loadout support" answer is a FACT about the companion — it contributes
// nothing, and that is not worth a warning.
func TestProbeLoadout_NoLoadoutAnswer_ContributesNothingQuietly(t *testing.T) {
	probe, warned := probeLtk(t, nil, exitErr(t, "exit 2"))
	assert.Empty(t, probe.Loadouts)
	cand, ok := ltkCandidate(probe)
	require.True(t, ok)
	assert.Equal(t, bundles.CandidateNoLoadout, cand.Reason)
	assert.Empty(t, warned, "an answer is ordinary, not a warning")
}

// TestCompanionLoadoutOutput_TimeoutIsAFailureNotAnAnswer drives the REAL
// exec seam against a companion that never answers: the timeout's kill must
// classify as a failed probe, or a wedged companion would be read as one
// offering no loadout and go unreported.
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
