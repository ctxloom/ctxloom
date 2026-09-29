package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/fsstore"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/engines"
)

// =============================================================================
// The runtime axis is a SECURITY BOUNDARY, so every string that becomes one is
// parsed by launch.ParseRuntimeAxis and an unrecognized spelling is refused —
// never warned, never degraded. The tests below cover the three shapes that
// matter at each boundary:
//
//   - a typo is REFUSED and the guarded thing does not happen (no write, no
//     run, no axes prepared);
//   - UNSET is not a typo: it passes through and still resolves to the host
//     default, exactly as it did before any of this parsing existed;
//   - a correctly-spelled non-default value still reaches the axis — the
//     CONTROL, without which a refusal test could pass on an unreachable path.
//
// The direction of the failure is what makes the refusal load-bearing: asserted
// past the parser, an unrecognized spelling reads as NOT-A-CONTAINER, i.e. the
// bare host. A config asking to be inside a container boundary would have run
// outside one with nothing said.
// =============================================================================

// captureRuntimeAxis swaps the isolation seam for one that records the axes a
// launch was prepared with and hands back an inert policy, so nothing starts
// a real runner. The returned Axes is the zero value when the seam was never
// reached at all, and the returned engine's launched() is nil when no run
// ever started — the two effects a refusal has to produce.
func captureRuntimeAxis(t *testing.T) (*isolation.Axes, *stubEngine) {
	t.Helper()
	resetStrictness(t)
	got := &isolation.Axes{}
	engine := &stubEngine{out: "ran"}
	prev := prepareEnvironment
	prepareEnvironment = func(_ context.Context, req launch.CellRequest, _ isolation.Spec) (isolation.Environment, error) {
		*got = req.Axes
		return stubEnvAt(req.ProjectRoot, nil), nil
	}
	t.Cleanup(func() { prepareEnvironment = prev })
	return got, engine
}

// -----------------------------------------------------------------------------
// validateContainerStory — the WRITE boundary (`ctxloom agent create/set`).
// -----------------------------------------------------------------------------

// TestSetAgent_ContainerStoryGateRefusesATypodRuntimeRatherThanPassingItClean
// covers the two runtime sources that reach this gate UNPARSED: the recorded
// binding and the project `runtime:` default. (The third, an explicit
// --runtime on this very call, is parsed by validateAgentAxes before the gate
// runs — see TestSetAgent_RejectsUnknownRuntime.)
//
// Read past the parser, a typo answers "not a container", the gate returns
// clean, and a binding whose engine cannot authenticate inside a container is
// written anyway — a config that looks fine in `agent list` until the first
// launch of it fails. Reading the file back is what proves the refusal: an
// error alone would pass even if the bad binding were written alongside it.
func TestSetAgent_ContainerStoryGateRefusesATypodRuntimeRatherThanPassingItClean(t *testing.T) {
	// "acp" is a registered backend with no container-story mapping, so the
	// container-story refusal is the one thing left for a container axis to
	// fail on. Both controls below depend on that.
	const noAuthLabel = "editor"
	noAuthLabels := map[string]config.LLMConfig{noAuthLabel: {Type: "acp"}}

	newCfg := func(projectRuntime string, existing map[string]agents.Agent) *config.Config {
		return config.NewFixture(config.Fixture{
			LM:      config.LMConfig{Configs: noAuthLabels, Defaults: config.RoleDefaults{Primary: noAuthLabel}},
			Agents:  existing,
			Runtime: projectRuntime,
		})
	}

	t.Run("control: a declared container axis on the project default IS seen by the gate", func(t *testing.T) {
		_, appDir := loadConfigDir(t, "version: 6\n")
		cfg := newCfg(string(isolation.RuntimeContainerRootless), nil)
		require.False(t, isolation.HasContainerStory("acp"),
			"fixture precondition: the label's backend must have no container story")

		_, err := SetAgent(context.Background(), managerFor(t, appDir), cfg, SetAgentRequest{Name: "odd", LLM: ptr(noAuthLabel)})
		require.Error(t, err, "the gate must READ the project default — this is what makes the refusal below meaningful")
		assert.Contains(t, err.Error(), noContainerStory)
		_, ok := readAgentFromDisk(t, appDir, "odd")
		assert.False(t, ok)
	})

	t.Run("control: the other ownership mode is seen too", func(t *testing.T) {
		_, appDir := loadConfigDir(t, "version: 6\n")
		cfg := newCfg(string(isolation.RuntimeContainerRootful), nil)

		_, err := SetAgent(context.Background(), managerFor(t, appDir), cfg, SetAgentRequest{Name: "odd", LLM: ptr(noAuthLabel)})
		require.Error(t, err, "rootful and rootless are two members, not one; a gate that only saw one would answer host for the other")
		assert.Contains(t, err.Error(), noContainerStory)
	})

	t.Run("a typo'd project runtime default is refused and nothing is written", func(t *testing.T) {
		_, appDir := loadConfigDir(t, "version: 6\n")
		cfg := newCfg("contianer-rootless", nil)

		_, err := SetAgent(context.Background(), managerFor(t, appDir), cfg, SetAgentRequest{Name: "odd", LLM: ptr(noAuthLabel)})
		require.Error(t, err, "a typo must not read as host and slip past the container-story gate")
		assert.Contains(t, err.Error(), "contianer-rootless")
		assert.Contains(t, err.Error(), "host|container-rootless|container-rootful",
			"the refusal names the legal values, not just the bad one")
		_, ok := readAgentFromDisk(t, appDir, "odd")
		assert.False(t, ok, "a refused write must leave no agent behind")
	})

	t.Run("a typo'd RECORDED runtime is refused and the binding is not mutated", func(t *testing.T) {
		_, appDir := loadConfigDir(t, "version: 6\n")
		cfg := newCfg("", map[string]agents.Agent{
			"odd": {LLM: noAuthLabel, Runtime: "contianer-rootful"},
		})

		_, err := SetAgent(context.Background(), managerFor(t, appDir), cfg, SetAgentRequest{Name: "odd", Profiles: ptr([]string{"p1"})})
		require.Error(t, err, "a hand-edited binding's typo must be refused by the next write that touches it")
		assert.Contains(t, err.Error(), "contianer-rootful")
		_, ok := readAgentFromDisk(t, appDir, "odd")
		assert.False(t, ok, "the refused write must not half-apply")
	})

	t.Run("UNSET is not a typo: it still writes exactly as before", func(t *testing.T) {
		_, appDir := loadConfigDir(t, "version: 6\n")
		cfg := newCfg("", nil)

		_, err := SetAgent(context.Background(), managerFor(t, appDir), cfg, SetAgentRequest{Name: "plain", LLM: ptr(noAuthLabel)})
		require.NoError(t, err, "an agent that names no runtime resolves to host downstream — refusing it would refuse every default config")
		written, ok := readAgentFromDisk(t, appDir, "plain")
		require.True(t, ok)
		assert.Empty(t, written.Runtime, "unset stays unset; it is not rewritten as a literal host")
	})
}

// -----------------------------------------------------------------------------
// RunOneshot — the single-profile launch boundary.
// -----------------------------------------------------------------------------

// TestRunOneshot_RuntimeAxisIsParsedNotAsserted pins the oneshot's `runtime:`
// read. The oneshot has no agent binding to declare an axis, so the project
// default IS the axis — asserted past the parser it would have read as the
// host, launching an engine outside the container boundary the project asked
// for.
// testOneShotOn is testOneShot with the isolation seam the caller already
// installed (captureRuntimeAxis) left in place; it resolves and drives one
// turn.
func testOneShotOn(t *testing.T, cfg *config.Config, pipe *bundles.Pipeline, stub *stubEngine, src launch.Source) (string, error) {
	t.Helper()
	deps := launch.Deps{
		SessionClaims: fsstore.SessionClaims,
		Snapshot:      &config.Snapshot{Config: cfg},
		Engines:       engines.Registry(),
		Assembler:     &assembler{pipe: pipe, engines: engines.Registry()},
		Cells:         Cells{engines: engines.Registry(), cfg: cfg},
		Endpoints:     endpointMinter{},
		Sessions:      sessions.NewMemStore(),
		Host:          launch.HostFacts{Home: t.TempDir(), CtxloomHome: t.TempDir(), Binary: "ctxloom"},
	}
	src.WorkDir = t.TempDir()
	_, hosts := hostsFor(deps, stub)
	o, err := StartOneShot(context.Background(), deps, hosts, sessions.Seed{ProjectDir: src.WorkDir}, src)
	if err != nil {
		return "", err
	}
	defer o.End()
	return o.Turn(context.Background(), "t")
}

func TestOneShot_RuntimeAxisIsParsedNotAsserted(t *testing.T) {
	oneshotCfg := func(t *testing.T, runtime string) *config.Config {
		t.Helper()
		base := oneshotTestConfig(t)
		f := base.ToFixture()
		f.Runtime = runtime
		out := config.NewFixture(f)
		// NewFixture does not carry the injected filesystem, and the profile
		// this config selects is a FILE on it now.
		out.SetFS(base.FS())
		return out
	}

	t.Run("control: a declared container axis reaches the member's isolation request", func(t *testing.T) {
		_, loader := setupContextTestFS(t)
		got, engine := captureRuntimeAxis(t)
		cfg := oneshotCfg(t, string(isolation.RuntimeContainerRootless))

		_, err := testOneShotOn(t, cfg, opPipe(cfg, loader), engine, launch.Source{Profiles: []string{"rev"}})
		require.NoError(t, err)
		require.NotNil(t, engine.launched(), "sanity: the control really did launch an engine")
		assert.Equal(t, isolation.RuntimeContainerRootless, got.Runtime,
			"the project runtime default really does drive the member's axes — without this the refusal below could pass on a dead path")
		assert.True(t, got.WantsContainer())
	})

	t.Run("control: the other ownership mode reaches it too", func(t *testing.T) {
		_, loader := setupContextTestFS(t)
		got, engine := captureRuntimeAxis(t)
		cfg := oneshotCfg(t, string(isolation.RuntimeContainerRootful))

		_, err := testOneShotOn(t, cfg, opPipe(cfg, loader), engine, launch.Source{Profiles: []string{"rev"}})
		require.NoError(t, err)
		require.NotNil(t, engine.launched(), "sanity: the control really did launch an engine")
		assert.Equal(t, isolation.RuntimeContainerRootful, got.Runtime)
	})

	t.Run("a typo'd runtime refuses and no engine ever runs", func(t *testing.T) {
		_, loader := setupContextTestFS(t)
		got, engine := captureRuntimeAxis(t)
		cfg := oneshotCfg(t, "contianer-rootless")

		_, err := testOneShotOn(t, cfg, opPipe(cfg, loader), engine, launch.Source{Profiles: []string{"rev"}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "contianer-rootless")
		assert.Contains(t, err.Error(), "host|container-rootless|container-rootful")
		assert.Nil(t, engine.launched(), "THE POINT: the engine must never have run")
		assert.Equal(t, isolation.Axes{}, *got, "no isolation was prepared at all")
	})

	t.Run("UNSET still resolves to the existing host default", func(t *testing.T) {
		_, loader := setupContextTestFS(t)
		got, engine := captureRuntimeAxis(t)
		cfg := oneshotCfg(t, "")

		_, err := testOneShotOn(t, cfg, opPipe(cfg, loader), engine, launch.Source{Profiles: []string{"rev"}})
		require.NoError(t, err, "a project that declares no runtime must behave exactly as it did before this key existed")
		require.NotNil(t, engine.launched(), "and the engine still ran")
		assert.Equal(t, launch.RuntimeHost, got.Runtime, "unset resolves to the host")
		assert.False(t, got.WantsContainer(), "and still means the host")
	})
}

// -----------------------------------------------------------------------------
// RuntimeOffer — the interview's menu.
// -----------------------------------------------------------------------------

// TestAgentRuntimeOffer_MenuCanOnlyHoldDeclaredMembers is this boundary's form
// of the same guarantee. The offer is not a place a user string arrives: every
// element is minted from the vocabulary's own constants, and the field is
// TYPED so nothing else can be put there. What this pins is that the menu and
// the parser cannot drift — an offer the writer's own parser would refuse is a
// decision collected and then thrown away.
func TestAgentRuntimeOffer_MenuCanOnlyHoldDeclaredMembers(t *testing.T) {
	cfg, _ := loadConfigDir(t, "version: 6\n")
	names := EngineNames(engines.Registry())
	require.NotEmpty(t, names)

	sawContainer := false
	for _, backend := range names {
		offer := AgentRuntimeOffer(engines.Registry(), cfg, backend)
		require.NotEmpty(t, offer.Runtimes, "%s: host is always offered", backend)
		for _, r := range offer.Runtimes {
			parsed, err := launch.ParseRuntimeAxis(string(r))
			require.NoError(t, err, "%s: the menu offered %q, which the writer's own parser refuses", backend, r)
			assert.Equal(t, r, parsed)
			assert.NotEmpty(t, string(r), "an empty axis is not an offer — the menu names what to pick")
		}
		if offer.OffersContainer() {
			sawContainer = true
		}
	}
	assert.True(t, sawContainer,
		"vacuity guard: at least one engine must have been offered a container axis, or this scan proves nothing about container spellings")
}

// TestRuntimeOffer_OffersContainerReadsBothOwnershipModes pins that the
// predicate answers on BOTH container members. An equality test against one
// const would silently answer "host" for the other ownership mode, which is
// the bug the three-value split exists to prevent.
func TestRuntimeOffer_OffersContainerReadsBothOwnershipModes(t *testing.T) {
	for _, axis := range []isolation.RuntimeAxis{isolation.RuntimeContainerRootless, isolation.RuntimeContainerRootful} {
		offer := RuntimeOffer{Runtimes: []isolation.RuntimeAxis{isolation.RuntimeHost, axis}}
		assert.True(t, offer.OffersContainer(), "%s is a container axis", axis)
	}
	assert.False(t, RuntimeOffer{Runtimes: []isolation.RuntimeAxis{isolation.RuntimeHost}}.OffersContainer())
	assert.False(t, RuntimeOffer{}.OffersContainer(), "an empty menu offers no container")
	assert.False(t, RuntimeOffer{Runtimes: []isolation.RuntimeAxis{""}}.OffersContainer(),
		"unset is not a container offer")
}
