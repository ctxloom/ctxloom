package isolation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// TestRuntimeAxis_ThreeValuesAndNoAnyContainer pins the axis vocabulary itself:
// exactly three values, and NO "any container" one.
//
// The absent value is the load-bearing half. "container" used to mean "give me
// whichever ownership this host offers", which is precisely the choice a
// workload with a UID-mapping requirement cannot delegate. Reintroducing it —
// as a value, an alias, or a migration — turns this red.
func TestRuntimeAxis_ThreeValuesAndNoAnyContainer(t *testing.T) {
	assert.Equal(t, []string{"host", "container-rootless", "container-rootful"}, RuntimeNames(),
		"the axis vocabulary is exactly these three, in this order (it renders into user-facing fix-it text and shell completion)")

	for _, v := range []RuntimeAxis{RuntimeContainerRootless, RuntimeContainerRootful} {
		assert.True(t, IsContainerRuntimeAxis(v), "%q is a container axis", v)
		assert.True(t, Axes{Runtime: v}.WantsContainer(), "%q must ask for a container boundary", v)
		assert.False(t, Axes{Runtime: v}.Zero(), "%q is a non-zero isolation request", v)
	}
	for _, v := range []RuntimeAxis{"", RuntimeHost, "container", "rootless", "Container-Rootful"} {
		assert.False(t, IsContainerRuntimeAxis(v), "%q must not be a container axis", v)
		assert.False(t, Axes{Runtime: v}.WantsContainer(), "%q must not ask for a container boundary", v)
	}
}

// TestOwnershipAxis_MapsProbedRootlessness pins the single site that turns a
// probed rootless flag into an axis value. It is trivial by design — the value
// of pinning it is that it is the ONLY mapping, so an inverted branch here
// would mislabel every runtime at once rather than in one caller.
func TestOwnershipAxis_MapsProbedRootlessness(t *testing.T) {
	assert.Equal(t, RuntimeContainerRootless, ownershipAxis(true))
	assert.Equal(t, RuntimeContainerRootful, ownershipAxis(false))
}

// TestSelectRuntime_OwnershipMismatchIsNeverSubstituted is the core assertion
// of the two-value split: a run that demands one ownership mode is NEVER handed
// the other, even when the other is reachable, preferred, and the only
// container runtime on the box.
//
// Substitution is the failure this exists to prevent, and it is silent: a
// rootful-demanding workload handed a rootless daemon gets a container whose
// root is the invoking user, does not fail, and produces wrong file ownership
// somewhere much later.
func TestSelectRuntime_OwnershipMismatchIsNeverSubstituted(t *testing.T) {
	t.Run("the only runtime is the wrong ownership: Host{}, not a substitution", func(t *testing.T) {
		stubRuntimeCandidates(t, ownedBy("docker", RuntimeContainerRootless))

		rt := SelectRuntime("", RuntimeContainerRootful)
		assert.IsType(t, Host{}, rt,
			"a rootful demand must not be satisfied by the rootless runtime that happens to be here")
	})

	t.Run("the reverse holds", func(t *testing.T) {
		stubRuntimeCandidates(t, ownedBy("docker", RuntimeContainerRootful))

		rt := SelectRuntime("", RuntimeContainerRootless)
		assert.IsType(t, Host{}, rt)
	})

	t.Run("a matching runtime later in detection order is found past a mismatched one", func(t *testing.T) {
		stubRuntimeCandidates(t,
			ownedBy("docker", RuntimeContainerRootless),
			ownedBy("podman", RuntimeContainerRootful),
		)

		rt := SelectRuntime("", RuntimeContainerRootful)
		require.NotNil(t, rt)
		assert.Equal(t, "podman", rt.Name(),
			"the filter skips the mismatched first candidate rather than stopping at it")

		assert.Equal(t, "docker", SelectRuntime("", RuntimeContainerRootless).Name())
	})

	t.Run("an ownership-mismatched PREFERENCE falls through to a matching runtime, never to itself", func(t *testing.T) {
		stubRuntimeCandidates(t,
			ownedBy("docker", RuntimeContainerRootless),
			ownedBy("podman", RuntimeContainerRootful),
		)

		// docker is preferred by name but is the wrong ownership. The name
		// preference is advisory; the ownership demand is not.
		rt := SelectRuntime("docker", RuntimeContainerRootful)
		assert.Equal(t, "podman", rt.Name())
	})

	t.Run("an unavailable runtime in the right ownership is still not selected", func(t *testing.T) {
		stubRuntimeCandidates(t, runtimeCandidate{
			name: "docker",
			probe: func() (Runtime, RuntimeAxis) {
				return fakeRuntime{name: "docker", binary: "docker"}, RuntimeContainerRootful
			},
		})

		assert.IsType(t, Host{}, SelectRuntime("", RuntimeContainerRootful),
			"ownership is an ADDITIONAL constraint on top of availability, not a replacement for it")
	})
}

// TestSelectRuntime_NonContainerDemandNeverYieldsAContainer pins SelectRuntime's
// fail-safe totality: it is defined for every RuntimeAxis, and every value that
// is not one of the two container ones answers Host{} WITHOUT probing.
//
// This is what makes a forgotten demand harmless. A caller that reaches
// SelectRuntime with a zero RuntimeAxis has not expressed a container request,
// and the safe reading of "no request" is the host — never "whatever is
// reachable", which is how a run acquires a boundary nobody asked for and, more
// importantly, how it acquires the WRONG ownership.
func TestSelectRuntime_NonContainerDemandNeverYieldsAContainer(t *testing.T) {
	probed := false
	stubRuntimeCandidates(t, runtimeCandidate{
		name: "docker",
		probe: func() (Runtime, RuntimeAxis) {
			probed = true
			return fakeRuntime{name: "docker", binary: "docker", available: true}, RuntimeContainerRootless
		},
	})

	for _, want := range []RuntimeAxis{"", RuntimeHost, "container", "nonsense"} {
		assert.IsType(t, Host{}, SelectRuntime("", want), "want=%q", want)
		assert.IsType(t, Host{}, SelectRuntime("docker", want), "want=%q with an explicit preference", want)
	}
	assert.False(t, probed, "a non-container demand must not even probe for a runtime")
}

// TestProbeRuntime_IsUnconstrainedByOwnership pins the OTHER entry point: the
// "what container runtime is reachable here?" question that diagnostics
// (`container check`) and an image BUILD ask, neither of which commits a run to
// an isolation boundary.
//
// It must accept either ownership — an image is ownership-agnostic, and a
// diagnosis that hid the rootless daemon on the box because nobody named an
// ownership would be reporting a fiction. The two entry points are separate
// FUNCTIONS precisely so this permissiveness can never leak onto a run path by
// someone passing a convenient zero value.
func TestProbeRuntime_IsUnconstrainedByOwnership(t *testing.T) {
	for _, owns := range []RuntimeAxis{RuntimeContainerRootless, RuntimeContainerRootful} {
		stubRuntimeCandidates(t, ownedBy("docker", owns))

		rt := ProbeRuntime("")
		require.NotNil(t, rt)
		assert.Equal(t, "docker", rt.Name(), "ownership %q must not be filtered out by a probe", owns)
	}

	stubRuntimeCandidates(t)
	assert.IsType(t, Host{}, ProbeRuntime(""), "no candidate at all is still Host{}")
}

// TestChainFor_OwnershipMismatch_Fatal is the run-path half of the contract,
// driven through the REAL SelectRuntime (the runtimeCandidates seam, not the
// surveyRuntimes one) so the ownership filter actually runs.
//
// Three things are asserted that no other test covers:
//
//   - An ownership mismatch takes the SAME fatal path as "no runtime at all":
//     the container tier never enters the chain, and one non-degradable
//     ClassIsolation finding is recorded that the choke owner aborts on.
//   - The refusal names the container this host CAN give and the explicit
//     selection that opts into it: choosing the other ownership is the
//     user's decision, never ours.
//   - --degraded changes neither: the other ownership mode is reachable in
//     this fixture and is still not chosen, and the gate still aborts.
func TestChainFor_OwnershipMismatch_Fatal(t *testing.T) {
	// Only a ROOTLESS runtime is reachable, and the run demands ROOTFUL.
	onlyRootless := func(t *testing.T) { stubRuntimeCandidates(t, ownedBy("docker", RuntimeContainerRootless)) }

	t.Run("strict: fatal, and the chain drops to the host tier", func(t *testing.T) {
		resetStrictness(t)
		onlyRootless(t)

		chain := chainFor(surveyRuntimes(), Axes{Runtime: RuntimeContainerRootful}, "claude-code", ImageConfig{})
		require.Len(t, chain, 1)
		assert.IsType(t, None{}, chain[0],
			"the reachable ROOTLESS runtime must not enter the chain for a ROOTFUL request")

		findings := strictness.All()
		require.Len(t, findings, 1)
		assert.Equal(t, report.KindIsolation, findings[0].Kind)
		assert.Contains(t, findings[0].Text, string(RuntimeContainerRootful),
			"the finding must name the ownership that was demanded, or the user cannot tell which half failed")
		assert.Contains(t, findings[0].Text, "ownership")
		assert.Contains(t, findings[0].Text, "container-rootless (docker)",
			"the refusal names the container this host CAN give")
		assert.Contains(t, findings[0].Text, "ctxloom agent edit <agent> --runtime container-rootless",
			"and the explicit selection that opts into it")
	})

	t.Run("strict {worktree,rootful}: the runtime axis degrades ALONE", func(t *testing.T) {
		resetStrictness(t)
		onlyRootless(t)

		chain := chainFor(surveyRuntimes(), Axes{Workspace: WorkspaceWorktree, Runtime: RuntimeContainerRootful}, "claude-code", ImageConfig{})
		require.NotEmpty(t, chain)
		assert.IsType(t, Worktree{}, chain[0], "the requested worktree survives an ownership mismatch")

		findings := strictness.All()
		require.Len(t, findings, 1)
		assert.Equal(t, report.KindIsolation, findings[0].Kind)
	})

	t.Run("degraded: still refused, and never satisfied by the other ownership mode", func(t *testing.T) {
		resetStrictness(t)
		onlyRootless(t)

		chain := chainFor(surveyRuntimes(), Axes{Runtime: RuntimeContainerRootful}, "claude-code", ImageConfig{})
		require.Len(t, chain, 1)
		assert.IsType(t, None{}, chain[0],
			"--degraded does not authorize the OTHER ownership mode, which is the same silent substitution wearing a flag")
		all := strictness.All()
		require.NotEmpty(t, all)
		assert.NotEmpty(t, strictness.Mode{Degraded: true}.Actionable(all),
			"--degraded must not turn a refused boundary into a host run: the gate still aborts")
	})

	t.Run("a MATCHING ownership still builds the container tier", func(t *testing.T) {
		resetStrictness(t)
		stubRuntimeCandidates(t, ownedBy("docker", RuntimeContainerRootful))

		chain := chainFor(surveyRuntimes(), Axes{Runtime: RuntimeContainerRootful}, "claude-code", ImageConfig{})
		require.Len(t, chain, 2)
		assert.True(t, IsContainerPolicyName(chain[0].Name()),
			"sanity: the fixture CAN produce a container, so the mismatch cases above are about ownership and not about a broken stub")
		assert.Empty(t, strictness.All())
	})
}

// TestSelectRuntime_SlowEngineIsStillSelected forces the interleaving a loaded
// host produces: the engine is live and answers every probe correctly, just
// slowly. Selection gates a run FATALLY — a container request that lands on
// Host{} is refused outright — so a probe that reads "slow" as "absent" refuses
// a run the engine would have served. The shim is the only thing on PATH, so
// the answer cannot come from a real runtime, and the delay is chosen to be
// well past what a loaded host has been measured taking yet far below a hang.
func TestSelectRuntime_SlowEngineIsStillSelected(t *testing.T) {
	dir := t.TempDir()
	shim := "#!/bin/sh\n" +
		"case \"$1\" in info) /bin/sleep 6 ;; *) exit 0 ;; esac\n" +
		"case \"$*\" in *--format*) echo 'true pasta' ;; esac\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "podman"), []byte(shim), 0o755))
	t.Setenv("PATH", dir)

	rt := SelectRuntime("", RuntimeContainerRootless)
	assert.IsType(t, Podman{}, rt,
		"a live rootless podman that answers `info` slowly must be selected, not reported absent")
}
