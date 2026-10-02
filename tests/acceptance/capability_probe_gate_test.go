// Untagged like capability_probe_gate.go itself: the shared cell gate is what
// stands between a feature file and a paid subscription turn, and it must be
// checkable without one.
//
// WHAT THESE DEFEND. The gate was extracted after four probes had each typed it
// inline, and the reason a copy is dangerous is not duplication — it is that
// every one of its decisions FAILS SILENTLY when it is wrong:
//
//   - invert the availability test and an unavailable engine stops skipping. It
//     buys a turn, fails for a reason that has nothing to do with the claim, and
//     the red gets read as a capability finding;
//   - invert it the other way and an available engine skips forever, which is
//     the ladder's own definition of work that never ran being indistinguishable
//     from work that passed;
//   - drop the credential refusal and a cell runs without the token, so the
//     engine's authentication failure is entirely the harness's doing;
//   - turn a malformed cell into a skip and a typo'd axis or an unregistered
//     engine reads as coverage for the rest of time.
//
// None of that needs a live engine to check, so none of it waits for one.
package acceptance

import (
	"errors"
	"os/exec"
	"testing"

	"github.com/cucumber/godog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/engines/claude"
)

func gateCell(engine, runtime, workspace string) probeCellID {
	return probeCellID{Probe: "p-test", Engine: engine, Runtime: runtime, Workspace: workspace}
}

// --- the availability fold ----------------------------------------------------

// TestProbeCellDecide_UnavailableEngineSkipsAndAvailableOneProceeds pins the one
// line whose inversion is invisible in a green run, in BOTH directions — a test
// that only checked the skip would still pass with the branch reversed if it
// never asserted the other side.
func TestProbeCellDecide_UnavailableEngineSkipsAndAvailableOneProceeds(t *testing.T) {
	t.Run("unavailable engine yields the skip reason", func(t *testing.T) {
		report, skip := probeCellDecide(engineStatus{
			name: "mock", available: false, reason: "binary not on PATH",
		})
		require.Equal(t, "binary not on PATH", skip,
			"an unavailable engine must skip, carrying production's own reason — a cell that runs anyway buys a paid turn and reds for a reason that is not about the claim")
		require.NotEmpty(t, report,
			"even a skipped cell records what the gate saw, or its evidence sidecar cannot say why it declined")
	})

	t.Run("available engine yields no skip", func(t *testing.T) {
		report, skip := probeCellDecide(engineStatus{
			name: "mock", available: true, reason: "",
		})
		require.Empty(t, skip,
			"an available engine must proceed; a gate that skips it makes the cell indistinguishable from one nobody wrote")
		require.NotEmpty(t, report, "the availability report is recorded on every path")
	})
}

// TestProbeCellDecide_ReportsEvenWhenTheReasonIsEmpty covers the shape that
// would otherwise let a skip go out with nothing attached: an unavailable status
// whose reason nobody filled in. The gate still skips — availability is the
// fact — and the standing rule that a blank cell always carries a reason is
// enforced upstream, where the reason is produced.
func TestProbeCellDecide_ReportsEvenWhenTheReasonIsEmpty(t *testing.T) {
	report, skip := probeCellDecide(engineStatus{name: "mock", available: false})
	assert.Empty(t, skip, "an unavailable status with no reason still must not proceed to buy a turn")
	assert.NotEmpty(t, report)
}

// --- the cell resolution ------------------------------------------------------

// TestProbeCellResolve_MalformedCellsAreHardErrorsNeverSkips is the ladder's
// central distinction made hermetic. A fact about THIS BOX (no engine installed)
// is a skip; a fact about the FEATURE FILE (a typo'd axis, an engine no
// liveAgents row covers) is a bug, and a bug that skips would read as coverage
// forever.
func TestProbeCellResolve_MalformedCellsAreHardErrorsNeverSkips(t *testing.T) {
	cases := []struct {
		name  string
		cell  probeCellID
		wants string
	}{
		{"unknown runtime axis", gateCell("claude-code", "vm", "none"), "unknown runtime axis"},
		// The retired undifferentiated "container" (task unwatched-discharge
		// split it into container-rootless/container-rootful) is exactly as
		// malformed as a typo now: probeCellResolve parses cell.Runtime via
		// the same launch.ParseRuntimeAxis every production boundary uses, and
		// that function declares no "any container" value.
		{"retired undifferentiated container axis", gateCell("claude-code", "container", "none"), "unknown runtime axis"},
		{"unknown workspace axis", gateCell("claude-code", "host", "sandbox"), "unknown workspace axis"},
		{"empty workspace axis", gateCell("claude-code", "host", ""), "unknown workspace axis"},
		{"engine no liveAgents row covers", gateCell("cursor", "host", "none"), "is not registered in liveAgents"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := probeCellResolve("p-test", tc.cell)
			require.Error(t, err, "a malformed cell must stop the suite; skipping it would make the row read as coverage forever")
			require.False(t, errors.Is(err, godog.ErrSkip),
				"this is a fact about the feature file, not about this box — it may never come back as a skip")
			assert.Contains(t, err.Error(), tc.wants)
			assert.Contains(t, err.Error(), "p-test",
				"the family names which probe refused, because the ladder runs many probes over one engine × axis grid")
		})
	}
}

// TestProbeCellResolve_AcceptsEveryDeclaredAxisAndEngine is the other side: the
// vocabulary the registry actually uses must all pass the gate. Without this,
// tightening the switch above — say to host-only — would look like a green
// hardening while quietly making four registry-declared cells unrunnable.
//
// "container" is deliberately NOT in this list: task unwatched-discharge
// retired the undifferentiated axis (see the malformed-cells test above), so
// it is no longer declared vocabulary — the registry's own P0 rows only ever
// name host/container-rootless/container-rootful (engine_isolation_matrix.
// feature's header).
func TestProbeCellResolve_AcceptsEveryDeclaredAxisAndEngine(t *testing.T) {
	for _, engine := range probeEngines {
		for _, runtime := range []string{"host", "container-rootless", "container-rootful"} {
			for _, workspace := range []string{"none", "worktree"} {
				cell := gateCell(engine, runtime, workspace)
				a, key, err := probeCellResolve("p-test", cell)
				require.NoError(t, err, "%s is declared vocabulary in the probe registry and must resolve", cell)
				assert.NotEmpty(t, key, "%s: the liveAgents key is what a cell's config selects as its llm label", cell)
				assert.Equal(t, backendTypeToLiveKey(engine), key)
				assert.NotEmpty(t, a.binary, "%s: the resolved row must be the real one, not a zero value", cell)
			}
		}
	}
}

// TestProbeCellResolve_EmptyRuntimeAxisIsHost pins the one deliberate exception
// to "every value not in the vocabulary is refused": an empty runtime string
// parses as the host default, exactly like every other launch.ParseRuntimeAxis
// boundary in production (an agent/project with no `runtime:` declared). This
// is NOT a local default arm here — probeCellResolve makes no empty-string
// special case of its own; it falls out of calling the shared parser once, the
// same as the retired-"container" refusal above falls out of calling it once.
func TestProbeCellResolve_EmptyRuntimeAxisIsHost(t *testing.T) {
	cell := gateCell("claude-code", "", "none")
	a, key, err := probeCellResolve("p-test", cell)
	require.NoError(t, err, "an empty runtime axis parses as host, the same as an unset agent/project runtime: default")
	assert.NotEmpty(t, key)
	assert.NotEmpty(t, a.binary)
}

// --- the skip line ------------------------------------------------------------

// TestProbeCellSkip_NamesTheFamilyAndTheWholeCell. The line is the only trace a
// skipped cell leaves, and P4 is why the variant is in it: its plan arm and its
// bypass control differ by nothing else, so a line without the variant cannot
// say which half of a pair declined — and a pair with one half missing is a
// provisional note, not a measurement.
func TestProbeCellSkip_NamesTheFamilyAndTheWholeCell(t *testing.T) {
	err := probeCellSkip("plan-sentinel",
		probeCellID{Probe: probeP4, Engine: "mock", Runtime: "host", Workspace: "none", Variant: "control"},
		"codex is not authenticated here")
	require.ErrorIs(t, err, godog.ErrSkip, "a gate refusal is a skip, never a pass and never a red")
}

// TestProbeCellSkip_DropsTheProbeFieldSoTheLineNamesOneIdentifier. The family is
// already the first word of the line; stamping the probe name again inside the
// brackets reads as two different identifiers for one cell.
func TestProbeCellSkip_DropsTheProbeFieldSoTheLineNamesOneIdentifier(t *testing.T) {
	cell := probeCellID{Probe: probeP4, Engine: "mock", Runtime: "host", Workspace: "none", Variant: "control"}
	_ = probeCellSkip("plan-sentinel", cell, "unauthenticated")
	require.Equal(t, probeP4, cell.Probe,
		"probeCellSkip must not mutate its caller's cell — the caller goes on to use it as a ledger key")
}

// --- the credential posture ---------------------------------------------------

// TestProbeCellCredentialEnv_TheTokenIsTheWholeCredential is the ruled posture:
// the cell's run is a ctxloom run, so it authenticates with the token alone,
// inside testenv's isolated HOME. A captured API key is not handed over (the
// run would unset it), and the real home is neither pointed at nor needed.
func TestProbeCellCredentialEnv_TheTokenIsTheWholeCredential(t *testing.T) {
	withLaunchCredentials(t, map[string]string{claude.OAuthTokenEnv: "fake-token", claude.APIKeyEnv: "fake-key"})

	cmd := exec.Command("true")
	cmd.Env = []string{"HOME=/tmp/fake-home", "PATH=/usr/bin", claude.OAuthTokenEnv + "=stale"}
	home, err := probeCellCredentialEnv("p-test", "claude-code", cmd)
	require.NoError(t, err)

	assert.Equal(t, "/tmp/fake-home", home, "the run keeps the isolated home")
	assert.Equal(t, []string{"HOME=/tmp/fake-home", "PATH=/usr/bin", claude.OAuthTokenEnv + "=fake-token"}, cmd.Env,
		"the token is the ONE addition, and a namesake ahead of it is removed (glibc's getenv returns the first match)")
}

// TestProbeCellCredentialEnv_NoTokenRefuses. The gate skips a cell without the
// token before this is reached, so reaching it without one is the harness's
// fault: an error naming the cell's family and the fix, never a run on an API
// key or the real home's login, and the command is left untouched.
func TestProbeCellCredentialEnv_NoTokenRefuses(t *testing.T) {
	for name, creds := range map[string]map[string]string{
		"nothing captured": nil,
		"API key alone":    {claude.APIKeyEnv: "fake-key"},
	} {
		t.Run(name, func(t *testing.T) {
			withLaunchCredentials(t, creds)
			cmd := exec.Command("true")
			cmd.Env = []string{"HOME=/tmp/fake-home"}
			_, err := probeCellCredentialEnv("p-test", "claude-code", cmd)

			require.Error(t, err)
			require.False(t, errors.Is(err, godog.ErrSkip), "the gate already decided; this is the harness failing, not this box lacking a capability")
			assert.Contains(t, err.Error(), "p-test", "the refusal names which probe's cell it stopped")
			assert.Contains(t, err.Error(), "claude setup-token")
			assert.Equal(t, []string{"HOME=/tmp/fake-home"}, cmd.Env, "a refused cell's command is left untouched")
		})
	}
}
