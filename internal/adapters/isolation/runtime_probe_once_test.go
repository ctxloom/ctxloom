package isolation

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// countingRuntime is a launchable fake whose LIVE probe (Available) counts
// every call: a launch that asks the engine again after selection answered is
// the re-probe this file exists to forbid.
type countingRuntime struct {
	fakeRuntime
	live *atomic.Int32
}

func (r countingRuntime) Available() bool { r.live.Add(1); return r.fakeRuntime.available }

// countedCandidate is a candidate whose selection probe counts its calls and
// answers a launchable countingRuntime owned in mode owns.
func countedCandidate(name string, owns RuntimeAxis, probes, live *atomic.Int32) runtimeCandidate {
	rt := countingRuntime{fakeRuntime: fakeRuntime{name: name, binary: "true", available: true}, live: live}
	return runtimeCandidate{name: name, probe: func() (Runtime, RuntimeAxis) {
		probes.Add(1)
		return rt, owns
	}}
}

// TestLaunch_ProbesTheRuntimeOnce: one container launch asks the engine ONCE.
// Selection's verdict rides into prepare, so the container gate (launchGate)
// reads it instead of asking again — every extra ask is a window for a slow
// engine to flip the answer between selecting a runtime and launching on it.
// Driven through the exported Prepare and Preview, not the pieces.
func TestLaunch_ProbesTheRuntimeOnce(t *testing.T) {
	for name, launchIt := range map[string]func(t *testing.T, s Spec){
		"prepare": func(t *testing.T, s Spec) { prepared(t, s) },
		"preview": func(t *testing.T, s Spec) { Preview(context.Background(), s) },
	} {
		t.Run(name, func(t *testing.T) {
			resetStrictness(t)
			home := fakeHostHome(t, tokenFixture)
			passSharedFS(t)
			var probes, live atomic.Int32
			stubRuntimeCandidates(t, countedCandidate("docker", RuntimeContainerRootless, &probes, &live))

			launchIt(t, envSpec(t, containerAxes, claudeEngine(t), home, t.TempDir()))

			assert.Equal(t, int32(1), probes.Load(), "selection probes the engine exactly once per launch")
			assert.Zero(t, live.Load(), "nothing after selection re-probes the engine it selected")
		})
	}
}

// TestChainFor_RefusalProbesEachEngineOnce: a refused launch names the
// ownership the host CAN give, which asks about the other mode — from the
// same probes selection already took, not fresh ones.
func TestChainFor_RefusalProbesEachEngineOnce(t *testing.T) {
	resetStrictness(t)
	var dockerProbes, podmanProbes, live atomic.Int32
	stubRuntimeCandidates(t,
		countedCandidate("docker", RuntimeContainerRootless, &dockerProbes, &live),
		countedCandidate("podman", RuntimeContainerRootless, &podmanProbes, &live),
	)

	chainFor(surveyRuntimes(), Axes{Runtime: RuntimeContainerRootful}, "claude-code", ImageConfig{})

	found := strictness.All()
	require.Len(t, found, 1)
	assert.Contains(t, found[0].Text, "container-rootless (docker)", "the hint still names what the host can give")
	assert.Equal(t, int32(1), dockerProbes.Load(), "docker is probed once for the whole launch")
	assert.Equal(t, int32(1), podmanProbes.Load(), "podman is probed once for the whole launch")
	assert.Zero(t, live.Load())
}

// engineAnswers stubs the engine exec seam: plain `info` answers reachable
// (nil) or not, and the ownership query answers ownership. It counts each.
type engineAnswers struct {
	reachable error
	ownership func() (string, error)
	reach     atomic.Int32
	owns      atomic.Int32
}

func stubEngine(t *testing.T, a *engineAnswers) {
	t.Helper()
	prev, prevSelf := engineInfo, findSelf
	engineInfo = func(_ context.Context, _ string, format string) (string, error) {
		if format == "" {
			a.reach.Add(1)
			return "", a.reachable
		}
		a.owns.Add(1)
		return a.ownership()
	}
	findSelf = func(context.Context, Runtime) (selfContainer, bool, error) { return selfContainer{}, false, nil }
	t.Cleanup(func() { engineInfo, findSelf = prev, prevSelf })
}

// candidateNamed is the PRODUCTION candidate for bin (read before any stub).
func candidateNamed(t *testing.T, bin string) runtimeCandidate {
	t.Helper()
	for _, c := range runtimeCandidates() {
		if c.name == bin {
			return c
		}
	}
	t.Fatalf("no production candidate %q", bin)
	return runtimeCandidate{}
}

// probeNamed runs the PRODUCTION candidate probe for bin.
func probeNamed(t *testing.T, bin string) (Runtime, RuntimeAxis) {
	t.Helper()
	return candidateNamed(t, bin).probe()
}

// isRootless reads the rootless flag a probed engine builds its argv on.
func isRootless(rt Runtime) bool {
	switch r := rt.(type) {
	case Docker:
		return r.rootless
	case Podman:
		return r.rootless
	}
	panic("not an engine")
}

// engineOwnershipAnswers is each engine's `info --format` answer for a
// rootless and a rootful engine.
var engineOwnershipAnswers = map[string]map[RuntimeAxis]string{
	"docker": {RuntimeContainerRootless: "[name=rootless name=seccomp]", RuntimeContainerRootful: "[name=seccomp]"},
	"podman": {RuntimeContainerRootless: "true pasta", RuntimeContainerRootful: "false "},
}

// TestEngineProbe_AsksReachabilityAndOwnershipOnceEach: the production probe
// of each engine execs `info` once for reachability and once for ownership —
// and an unreachable engine is never asked its ownership, so it can never
// manufacture an ownership finding on a host it plays no part in.
func TestEngineProbe_AsksReachabilityAndOwnershipOnceEach(t *testing.T) {
	for _, bin := range []string{"docker", "podman"} {
		t.Run(bin, func(t *testing.T) {
			resetStrictness(t)
			answer := ""
			a := &engineAnswers{ownership: func() (string, error) { return answer, nil }}
			stubEngine(t, a)

			for i, mode := range []RuntimeAxis{RuntimeContainerRootful, RuntimeContainerRootless} {
				answer = engineOwnershipAnswers[bin][mode]
				rt, owns := probeNamed(t, bin)
				assert.Equal(t, mode, owns, "%s answers %q", bin, answer)
				assert.Equal(t, mode, ownershipAxis(isRootless(rt)),
					"the argv is built on the same answer selection filtered on")
				assert.True(t, rt.launchable(), "a reachable engine carries its verdict")
				assert.Equal(t, int32(i+1), a.reach.Load(), "one reachability ask per probe")
				assert.Equal(t, int32(i+1), a.owns.Load(), "one ownership ask per probe")
			}

			a.reachable = errors.New("daemon down")
			rt, _ := probeNamed(t, bin)
			assert.False(t, rt.launchable())
			assert.Equal(t, int32(2), a.owns.Load(), "an unreachable engine is not asked its ownership")
			assert.Empty(t, strictness.All())
		})
	}
}

// TestEngineProbe_UndecidedOwnershipIsOneFindingForEveryEngine: an ownership
// check that fails, times out, or answers something unreadable leaves the
// ownership UNDECIDED — for docker and podman alike, through one path, with
// the same finding. No engine assumes a mode: an assumed mode is how a
// rootless request gets served by a rootful engine (or the reverse), the
// substitution the ownership rule forbids. So an undecided engine serves NO
// ownership demand.
func TestEngineProbe_UndecidedOwnershipIsOneFindingForEveryEngine(t *testing.T) {
	failures := map[string]func() (string, error){
		"fails":      func() (string, error) { return "", errors.New("exit status 125") },
		"times out":  func() (string, error) { return "", context.DeadlineExceeded },
		"unreadable": func() (string, error) { return "maybe", nil },
	}
	for how, answer := range failures {
		t.Run(how, func(t *testing.T) {
			texts := map[string]report.Finding{}
			production := map[string]runtimeCandidate{"docker": candidateNamed(t, "docker"), "podman": candidateNamed(t, "podman")}
			for _, bin := range []string{"docker", "podman"} {
				resetStrictness(t)
				stubEngine(t, &engineAnswers{ownership: answer})
				if bin == "docker" && how == "unreadable" {
					// docker's answer is a list that either names rootless or
					// does not: every answer it gives is readable.
					continue
				}

				rt, owns := production[bin].probe()
				assert.Equal(t, ownershipUndecided, owns, "%s: an unanswered ownership check decides nothing", bin)
				assert.True(t, rt.launchable(), "%s: the engine is reachable; only its ownership is unknown", bin)

				found := strictness.All()
				require.Len(t, found, 1, bin)
				assert.Equal(t, report.KindIsolation, found[0].Kind)
				assert.False(t, found[0].NonDegradable, "undecided is a degradable finding; the refusal of the run is chainFor's")
				texts[bin] = found[0]

				stubRuntimeCandidates(t, runtimeCandidate{name: bin, probe: func() (Runtime, RuntimeAxis) { return rt, owns }})
				for _, want := range []RuntimeAxis{RuntimeContainerRootless, RuntimeContainerRootful} {
					assert.IsType(t, Host{}, SelectRuntime("", want), "%s: an undecided engine must not serve %s", bin, want)
				}
				assert.Equal(t, bin, ProbeRuntime("").Name(), "%s: an ownership-free question still sees the engine", bin)
			}
			if d, ok := texts["docker"]; ok {
				p := texts["podman"]
				asDocker := strings.NewReplacer(podmanOwnershipFormat, dockerOwnershipFormat, "podman", "docker")
				assert.Equal(t, d.Remedy, asDocker.Replace(p.Remedy), "one remedy for both engines, naming each its own query")
				assert.Equal(t, d.Text, strings.ReplaceAll(p.Text, "podman", "docker"), "one finding for both engines")
			}
		})
	}
}

// TestSelectRuntime_RootlessRequestOnRootfulOnlyHostIsRefused drives the REAL
// production probes (only the exec is faked): a host whose only engine is
// rootful podman refuses a rootless request rather than serving it, and a
// failed podman ownership check no longer reads as "rootless".
func TestSelectRuntime_RootlessRequestOnRootfulOnlyHostIsRefused(t *testing.T) {
	resetStrictness(t)
	stubEngine(t, &engineAnswers{ownership: func() (string, error) { return "false ", nil }})
	stubRuntimeCandidates(t, candidateNamed(t, "podman"))

	assert.IsType(t, Host{}, SelectRuntime("", RuntimeContainerRootless), "rootful podman must not serve a rootless request")
	assert.Equal(t, "podman", SelectRuntime("", RuntimeContainerRootful).Name())

	chain := chainFor(surveyRuntimes(), Axes{Runtime: RuntimeContainerRootless}, "claude-code", ImageConfig{})
	require.Len(t, chain, 1)
	assert.IsType(t, None{}, chain[0])
	found := strictness.All()
	require.Len(t, found, 1)
	assert.True(t, found[0].NonDegradable, "refused under --degraded too")
}
