package companions

import (
	"context"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// selfAt is a Prober.Self resolver answering with a fixed path — what the
// composition root's selfexec.Path would answer for the running binary.
func selfAt(path string) func() string { return func() string { return path } }

// TestProbeCompanionLoadouts_ProbesItselfThroughSelfexec: ctxloom is its own
// companion. Its loadout is obtained the way every companion's is — by
// exec'ing `<bin> loadout --format json` — but the binary is THIS one, at
// the path the injected resolver answers (selfexec.Path in production),
// never a PATH lookup of "ctxloom" (a stale install earlier on PATH would
// then speak for the running build) and never a raw os.Executable (which
// goes stale after an in-place upgrade). No signature beside the binary is
// consulted: the running process is already executing, so exec consent is
// not a question.
func TestProbeCompanionLoadouts_ProbesItselfThroughSelfexec(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var lookedUp []string
	t.Cleanup(SetLookPathForTesting(func(bin string) (string, error) {
		lookedUp = append(lookedUp, bin)
		return "", exec.ErrNotFound
	}))
	var execd []string
	envelope, err := signing.EncodeLoadoutEnvelope(testsupport.RunLoadout("version: 1.0.0\nfragments:\n  isolation-axes:\n    content: SELF\n"), nil, "")
	require.NoError(t, err)
	t.Cleanup(SetCompanionLoadoutOutputForTesting(func(path string) ([]byte, error) {
		execd = append(execd, path)
		return envelope, nil
	}))

	probe, err := Prober{Self: selfAt("/opt/build/ctxloom")}.ProbeCompanionLoadouts(context.Background(), nil)
	require.NoError(t, err)

	require.Len(t, probe.Loadouts, 1, "only ctxloom itself answers when nothing else is installed")
	assert.Len(t, probe.Candidates, len(FirstPartyCompanionNames()), "the first-party names are absent candidates; ctxloom is not among them")
	self := probe.Loadouts[0]
	assert.Equal(t, SelfCompanion, self.Bin)
	assert.Equal(t, "/opt/build/ctxloom", self.Path)
	assert.True(t, self.Self, "the reader must know this loadout is ctxloom's own")
	assert.Equal(t, []string{"/opt/build/ctxloom"}, execd, "the self-probe execs the resolver's path")
	assert.NotContains(t, lookedUp, SelfCompanion, "ctxloom is never resolved through PATH")
	for _, c := range probe.Candidates {
		assert.NotEqual(t, SelfCompanion, c.Bin, "ctxloom is never a candidate: it is here by definition")
	}
}

// TestProbeCompanionLoadouts_SelfProbeFailureIsACandidate: a self-probe that
// breaks (a test binary that has no loadout command, a wedged exec) is
// reported like any other failed probe — never fatal, never silent.
func TestProbeCompanionLoadouts_SelfProbeFailureIsACandidate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(SetLookPathForTesting(func(string) (string, error) { return "", exec.ErrNotFound }))
	t.Cleanup(SetCompanionLoadoutOutputForTesting(func(string) ([]byte, error) { return nil, &exec.ExitError{} }))

	probe, err := Prober{Self: selfAt("/opt/build/ctxloom")}.ProbeCompanionLoadouts(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, probe.Loadouts)
	assert.Contains(t, probe.Candidates, bundles.CompanionCandidate{Bin: SelfCompanion, Path: "/opt/build/ctxloom", Reason: bundles.CandidateProbeFailed})
}

// TestProbeCompanionLoadouts_DisabledStillProbesItself: --no-companions /
// CTXLOOM_NO_COMPANIONS exists so a run does not depend on what the HOST has
// installed — no discovered binary is executed. ctxloom's own loadout is not
// one of those: it is the running binary's, so it is probed regardless, and a
// CI run keeps ctxloom's MCP server and its always-on guidance. Nothing
// discovered is exec'd.
func TestProbeCompanionLoadouts_DisabledStillProbesItself(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(SetLookPathForTesting(func(bin string) (string, error) { return "/fake/" + bin, nil }))
	t.Cleanup(AdmitEveryDiscoveredCompanionForTesting())
	envelope, err := signing.EncodeLoadoutEnvelope(testsupport.RunLoadout("version: 1.0.0\n"), nil, "")
	require.NoError(t, err)
	var execd []string
	t.Cleanup(SetCompanionLoadoutOutputForTesting(func(path string) ([]byte, error) {
		execd = append(execd, path)
		return envelope, nil
	}))

	probe, err := Prober{Disabled: true, Self: selfAt("/opt/build/ctxloom")}.ProbeCompanionLoadouts(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, probe.Loadouts, 1, "only ctxloom itself")
	assert.True(t, probe.Loadouts[0].Self)
	assert.Equal(t, []string{"/opt/build/ctxloom"}, execd, "no discovered companion is exec'd; the self-probe still runs")
	assert.Empty(t, probe.Candidates, "nothing was discovered, so nothing is reported as withheld")
}

// TestProbeCompanionLoadouts_UnarmedNeverProbesItself: a process composed
// without an embedded loadout (any test process) has nothing to emit and
// must never exec its own binary as if it were ctxloom.
func TestProbeCompanionLoadouts_UnarmedNeverProbesItself(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(SetLookPathForTesting(func(string) (string, error) { return "", exec.ErrNotFound }))
	t.Cleanup(SetCompanionLoadoutOutputForTesting(func(path string) ([]byte, error) {
		t.Fatalf("an unarmed prober exec'd %s", path)
		return nil, nil
	}))
	probe, err := Prober{}.ProbeCompanionLoadouts(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, probe.Loadouts)
	for _, c := range probe.Candidates {
		assert.NotEqual(t, SelfCompanion, c.Bin)
	}
}
