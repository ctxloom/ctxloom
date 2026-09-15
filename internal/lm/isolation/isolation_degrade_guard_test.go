package isolation

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// TestDegradedNeverBypassesIsolation is the guard the degradation audit
// (obstinate-judiciary, ruled 2026-09-14) leaves behind so the next author
// inherits the rule instead of re-deciding it.
//
// THE RULE: degrading may reduce what a run delivers. It may never damage
// anything and may never grant a security bypass.
//
// THE TEST FOR WHICH SIDE A SITE IS ON is strictness.FailAlways's own — DOES
// LAUNCHING CAUSE THE HARM? It is deliberately NOT "does it fail silently",
// which classifies nothing: strictness.Fail wraps clidiag.Warn, so every
// finding in this package streams a warning in both modes and "silent"
// separates none of them. The audit's first pass nearly mis-sorted the whole
// surface on exactly that mistake.
//
// WHY A TEST AND NOT JUST A COMMENT: the gap this closes was not an unknown
// rule, it was a known rule that drifted. chainFor's own comment already
// refused to substitute the other OWNERSHIP mode "in strict mode or under
// --degraded" while the code ten lines below dropped the entire container and
// ran on the host. A comment could not tell anyone that had happened. These
// assertions can, and 40a9dbe43 landing a fresh degraded site two days after
// the principle was first ruled on is the evidence that they are needed.
func TestDegradedNeverBypassesIsolation(t *testing.T) {
	// Every case below runs WITH --degraded on. That is the whole point: in
	// strict mode these already refused, and the bug was only ever visible
	// through the flag.

	t.Run("a requested container with no usable runtime refuses", func(t *testing.T) {
		resetStrictness(t)
		strictness.SetDegraded(true)
		// Host{} is what SelectRuntime returns both for "no runtime at all" and
		// for "no runtime with the demanded ownership" — chainFor treats them
		// identically, and so does this guard.
		stubRuntimeProbe(t, Host{})

		chain := chainFor(Axes{Runtime: RuntimeContainerRootless}, "claude", ImageConfig{})
		require.NotEmpty(t, chain)

		assertRefusesUnderDegraded(t,
			"a container was explicitly requested and cannot be provided; running on the host would be the bypass")
	})

	t.Run("a requested container+worktree refuses rather than keeping only the worktree", func(t *testing.T) {
		resetStrictness(t)
		strictness.SetDegraded(true)
		stubRuntimeProbe(t, Host{})

		chainFor(Axes{Runtime: RuntimeContainerRootless, Workspace: WorkspaceWorktree}, "claude", ImageConfig{})

		// A surviving worktree is NOT a substitute for the container: it is a
		// workspace, not a boundary. Keeping it must not soften the refusal.
		assertRefusesUnderDegraded(t,
			"the worktree survives, but the requested BOUNDARY does not")
	})

	t.Run("an unrecognised runtime axis refuses instead of landing on the host", func(t *testing.T) {
		resetStrictness(t)
		strictness.SetDegraded(true)

		warnUnknownAxes(Axes{Runtime: "contaienr"})

		assertRefusesUnderDegraded(t,
			"a typo must not be able to remove the sandbox, and --degraded is not a way to spell one")
	})

	t.Run("an unrecognised WORKSPACE axis still degrades quietly", func(t *testing.T) {
		resetStrictness(t)
		strictness.SetDegraded(true)

		warnUnknownAxes(Axes{Workspace: "wurktree"})

		// The negative half, and the reason this guard cannot be satisfied by
		// making everything non-degradable. The workspace axis is a
		// CONVENIENCE: losing it costs a checkout, not a boundary. If this ever
		// starts recording a finding, the rule has been over-applied and
		// --degraded has stopped being useful for the faults it exists for.
		assert.Empty(t, strictness.All(),
			"the workspace axis is a convenience, not a boundary — it must keep degrading")
	})

	t.Run("a DECLARED build base that cannot be used refuses", func(t *testing.T) {
		// A different class from everything above, and the rule statement in
		// isolation.go says so explicitly: this one does NOT follow from "does
		// launching cause the harm?" — nothing is exposed, the container still
		// runs and still drops privileges. It refuses because ctxloom cannot
		// READ the Containerfile, so it cannot know the substitution was safe;
		// making it silently is the program asserting knowledge it lacks.
		// Ruled 2026-09-15.
		for _, tc := range []struct {
			name string
			src  buildSource
		}{
			{"user-configured base Containerfile", buildSource{desc: "d", base: &baseStage{kind: baseStageKindUser}}},
			{"auto-detected project devcontainer", buildSource{desc: "d", base: &baseStage{kind: baseStageKindDevcontainer}}},
		} {
			resetStrictness(t)
			strictness.SetDegraded(true)
			recordBuildSourceFailure(tc.src, errors.New("build failed"))
			assertRefusesUnderDegraded(t, "a declared base ctxloom cannot use must not be silently substituted: "+tc.name)
		}
	})

	t.Run("an UNDECLARED build base still falls through quietly", func(t *testing.T) {
		resetStrictness(t)
		strictness.SetDegraded(true)

		// The negative half of the rule above, and the reason it is about
		// DECLARATION rather than about build failures in general. ctxloom's
		// own embedded default base was chosen by nobody, so falling past it
		// substitutes nothing the project asked for and must stay a warning.
		// Without this case the rule could be "satisfied" by refusing every
		// failed build, which would break first-run image composition outright.
		recordBuildSourceFailure(buildSource{desc: "embedded default base"}, errors.New("build failed"))

		assert.Empty(t, strictness.All(),
			"nothing was declared about the default base, so nothing is being substituted")
	})

	t.Run("the container argv never carries the run-as-root escape hatch", func(t *testing.T) {
		resetStrictness(t)
		strictness.SetDegraded(true)

		// The narrowest, highest-value assertion in the file: CTXLOOM_ALLOW_ROOT
		// used to be appended here under exactly this condition, telling the
		// image entrypoint to downgrade its OWN refusal to run as root into
		// warn-and-run-as-root — with the user's project bind-mounted, so
		// everything the run touched came back root-owned on the host.
		argv := strings.Join(identityEnvArgs(), " ")
		assert.NotContains(t, argv, "CTXLOOM_ALLOW_ROOT",
			"--degraded means 'I accept a thinner run', never 'I accept running as root'")
		// Positive control: prove the function still does its real job, so a
		// future refactor that returns nothing cannot pass the check above by
		// emitting no argv at all.
		assert.Contains(t, argv, "PUID=", "the identity remap itself must survive")
		assert.Contains(t, argv, "PGID=", "the identity remap itself must survive")
	})
}

// assertRefusesUnderDegraded is the shared shape of every positive case: the
// site recorded a ClassIsolation finding, that finding is NonDegradable, and it
// therefore SURVIVES strictness.Actionable while --degraded is on.
//
// The Actionable assertion is the one that matters and the one a
// straightforward rewrite would omit. Recording a finding is not refusing:
// before this audit every site here recorded faithfully and --degraded threw
// the record away at the gate. Asserting only "a finding exists" would have
// passed against the bypassing code.
func assertRefusesUnderDegraded(t *testing.T, because string) {
	t.Helper()
	require.True(t, strictness.Degraded(), "guard precondition: these cases only mean something with --degraded on")

	all := strictness.All()
	require.NotEmpty(t, all, "the site must record a finding at all: "+because)

	var iso []strictness.Finding
	for _, f := range all {
		if f.Class == strictness.ClassIsolation {
			iso = append(iso, f)
		}
	}
	require.NotEmpty(t, iso, "the finding must be ClassIsolation so the isolation gates see it: "+because)

	for _, f := range iso {
		assert.True(t, f.NonDegradable,
			"finding must be raised with strictness.FailAlways, not Fail — "+because+": "+f.Message)
		require.NotEmpty(t, f.FixIt, "a refusal must carry the fix, not just deny: "+f.Message)
		// A non-degradable finding that points at --degraded sends the user
		// round a loop that ends back at the same refusal. Three fix-it
		// constants in this package did exactly that before the audit.
		assert.NotContains(t, f.FixIt, "--degraded",
			"a non-degradable finding must not offer --degraded as its remedy: "+f.FixIt)
	}

	assert.NotEmpty(t, strictness.Actionable(iso),
		"THE ASSERTION THAT MATTERS: the finding must survive Actionable under --degraded, "+
			"or the gate drops it and the run proceeds unsandboxed anyway — "+because)
}
