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
//   - drop the credential refusal and a cell runs with HOME="", so production's
//     credential paths resolve to nothing and the engine's failure is entirely
//     the harness's doing;
//   - turn a malformed cell into a skip and a typo'd axis or an unregistered
//     engine reads as coverage for the rest of time.
//
// None of that needs a live engine to check, so none of it waits for one.
package acceptance

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			name: "codex", available: false, reason: "binary not on PATH",
		})
		require.Equal(t, "binary not on PATH", skip,
			"an unavailable engine must skip, carrying production's own reason — a cell that runs anyway buys a paid turn and reds for a reason that is not about the claim")
		require.NotEmpty(t, report,
			"even a skipped cell records what the gate saw, or its evidence sidecar cannot say why it declined")
	})

	t.Run("available engine yields no skip", func(t *testing.T) {
		report, skip := probeCellDecide(engineStatus{
			name: "codex", available: true, reason: "",
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
	report, skip := probeCellDecide(engineStatus{name: "kiro", available: false})
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
		{"empty runtime axis", gateCell("claude-code", "", "none"), "unknown runtime axis"},
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
func TestProbeCellResolve_AcceptsEveryDeclaredAxisAndEngine(t *testing.T) {
	for _, engine := range probeEngines {
		for _, runtime := range []string{"host", "container", "container-rootless", "container-rootful"} {
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

// --- the skip line ------------------------------------------------------------

// TestProbeCellSkip_NamesTheFamilyAndTheWholeCell. The line is the only trace a
// skipped cell leaves, and P4 is why the variant is in it: its plan arm and its
// bypass control differ by nothing else, so a line without the variant cannot
// say which half of a pair declined — and a pair with one half missing is a
// provisional note, not a measurement.
func TestProbeCellSkip_NamesTheFamilyAndTheWholeCell(t *testing.T) {
	err := probeCellSkip("plan-sentinel",
		probeCellID{Probe: probeP4, Engine: "kiro", Runtime: "host", Workspace: "none", Variant: "control"},
		"kiro is not authenticated here")
	require.ErrorIs(t, err, godog.ErrSkip, "a gate refusal is a skip, never a pass and never a red")
}

// TestProbeCellSkip_DropsTheProbeFieldSoTheLineNamesOneIdentifier. The family is
// already the first word of the line; stamping the probe name again inside the
// brackets reads as two different identifiers for one cell.
func TestProbeCellSkip_DropsTheProbeFieldSoTheLineNamesOneIdentifier(t *testing.T) {
	cell := probeCellID{Probe: probeP4, Engine: "kiro", Runtime: "host", Workspace: "none", Variant: "control"}
	_ = probeCellSkip("plan-sentinel", cell, "unauthenticated")
	require.Equal(t, probeP4, cell.Probe,
		"probeCellSkip must not mutate its caller's cell — the caller goes on to use it as a ledger key")
}

// --- the credential posture ---------------------------------------------------

// TestProbeHostCredentialEnv_RemovesTheIsolatedEntriesRatherThanAppendingPastThem
// is the glibc footgun, pinned. getenv returns the FIRST match for a duplicated
// key, so appending the real HOME after testenv's fake one would leave the fake
// one winning — and every credential path in production starts at that value.
func TestProbeHostCredentialEnv_RemovesTheIsolatedEntriesRatherThanAppendingPastThem(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"HOME=/tmp/fake-home",
		"XDG_CONFIG_HOME=/tmp/fake-home/.config",
		"XDG_DATA_HOME=/tmp/fake-home/.local/share",
		"USERPROFILE=/tmp/fake-home",
		"CTXLOOM_THING=keep-me",
	}
	out := probeHostCredentialEnv(env, "/home/real")

	for _, kv := range out {
		assert.NotContains(t, kv, "/tmp/fake-home",
			"an isolated credential entry survived: glibc's getenv returns the FIRST match, so the fake value would win and every production credential path would resolve to an empty home")
	}
	assert.Contains(t, out, "PATH=/usr/bin", "unrelated entries are passed through untouched")
	assert.Contains(t, out, "CTXLOOM_THING=keep-me")
	assert.Contains(t, out, "HOME=/home/real")
	assert.Contains(t, out, "USERPROFILE=/home/real")
	assert.Contains(t, out, "XDG_CONFIG_HOME=/home/real/.config")
	assert.Contains(t, out, "XDG_DATA_HOME=/home/real/.local/share")

	seen := map[string]int{}
	for _, kv := range out {
		if k, _, ok := strings.Cut(kv, "="); ok {
			seen[k]++
		}
	}
	for _, k := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_DATA_HOME"} {
		assert.Equal(t, 1, seen[k], "%s appears %d times — a duplicate is exactly the state this function exists to prevent", k, seen[k])
	}
}

// TestProbeCellCredentialEnv_RefusesRatherThanExportingAnEmptyHome. Without the
// refusal the run STARTS, with HOME="" and XDG_CONFIG_HOME="/.config": the
// engine finds no credential where production looks for one, and the cell
// reports an engine failure that is entirely the harness's doing. Every probe
// had typed this guard inline, which was one chance per probe to forget it.
//
// realHomeDir is set by TestMain, which is acceptance-tagged, so it is empty in
// an untagged build and populated in a tagged one. The test pins BOTH branches
// explicitly rather than reading whichever the build happens to give it — a
// test that only ever exercised the branch its build was born into would go
// green in one build while asserting nothing in the other.
func TestProbeCellCredentialEnv_RefusesRatherThanExportingAnEmptyHome(t *testing.T) {
	saved := realHomeDir
	t.Cleanup(func() { realHomeDir = saved })

	t.Run("no real home: refuse, and leave the command untouched", func(t *testing.T) {
		realHomeDir = ""
		cmd := exec.Command("true")
		cmd.Env = []string{"HOME=/tmp/fake-home"}
		err := probeCellCredentialEnv("p-test", cmd)

		require.Error(t, err, "a cell with no real home must refuse, not run with HOME=\"\" and blame the engine for what it then cannot find")
		require.False(t, errors.Is(err, godog.ErrSkip),
			"this is the harness failing to capture something, not this box lacking a capability — it may not be filed as a skip")
		assert.Contains(t, err.Error(), "p-test", "the refusal names which probe's cell it stopped")
		assert.Equal(t, []string{"HOME=/tmp/fake-home"}, cmd.Env,
			"a refused cell's command must be left untouched: a half-rewritten environment on an error path is how a later refactor ends up running it anyway")
	})

	t.Run("real home captured: the isolated entries are replaced", func(t *testing.T) {
		realHomeDir = "/home/real"
		cmd := exec.Command("true")
		cmd.Env = []string{"HOME=/tmp/fake-home", "PATH=/usr/bin"}
		require.NoError(t, probeCellCredentialEnv("p-test", cmd))

		assert.Contains(t, cmd.Env, "HOME=/home/real")
		assert.Contains(t, cmd.Env, "PATH=/usr/bin")
		assert.NotContains(t, cmd.Env, "HOME=/tmp/fake-home",
			"the isolated home must be REMOVED, not shadowed: glibc's getenv returns the first match")
	})
}
