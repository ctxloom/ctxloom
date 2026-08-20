package operations

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// =============================================================================
// The runtime axis is a SECURITY BOUNDARY, so every string that becomes one is
// parsed by agent.ParseRuntimeAxis and an unrecognized spelling is refused —
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
// launch was prepared with and hands back a canned engine, so nothing spawns a
// real plugin. The returned Axes is the zero value when the seam was never
// reached at all, and the returned client's gotReq is nil when no engine ever
// ran — the two effects a refusal has to produce.
func captureRuntimeAxis(t *testing.T) (*isolation.Axes, *stubClient) {
	t.Helper()
	resetStrictness(t)
	got := &isolation.Axes{}
	engine := &stubClient{out: "ran"}
	prev := prepareIsolation
	prepareIsolation = func(_ context.Context, axes isolation.Axes, _ string, _ isolation.ImageConfig, projectDir, _ string, _ isolation.SessionState) (isolation.Policy, isolation.Workspace) {
		*got = axes
		return stubPolicy{mk: func() pb.Client { return engine }}, stubWorkspace{dir: projectDir}
	}
	t.Cleanup(func() { prepareIsolation = prev })
	return got, engine
}

// -----------------------------------------------------------------------------
// validateContainerAuth — the WRITE boundary (`ctxloom agent create/set`).
// -----------------------------------------------------------------------------

// TestSetAgent_ContainerAuthGateRefusesATypodRuntimeRatherThanPassingItClean
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
func TestSetAgent_ContainerAuthGateRefusesATypodRuntimeRatherThanPassingItClean(t *testing.T) {
	// "acp" is a registered backend with no container-auth mapping, so the
	// container-auth refusal is the one thing left for a container axis to
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
		require.False(t, isolation.HasContainerAuth("acp"),
			"fixture precondition: the label's backend must have no container auth")

		_, err := SetAgent(managerFor(appDir), cfg, SetAgentRequest{Name: "odd", LLM: ptr(noAuthLabel)})
		require.Error(t, err, "the gate must READ the project default — this is what makes the refusal below meaningful")
		assert.Contains(t, err.Error(), "container auth")
		_, ok := readAgentFromDisk(t, appDir, "odd")
		assert.False(t, ok)
	})

	t.Run("control: the other ownership mode is seen too", func(t *testing.T) {
		_, appDir := loadConfigDir(t, "version: 6\n")
		cfg := newCfg(string(isolation.RuntimeContainerRootful), nil)

		_, err := SetAgent(managerFor(appDir), cfg, SetAgentRequest{Name: "odd", LLM: ptr(noAuthLabel)})
		require.Error(t, err, "rootful and rootless are two members, not one; a gate that only saw one would answer host for the other")
		assert.Contains(t, err.Error(), "container auth")
	})

	t.Run("a typo'd project runtime default is refused and nothing is written", func(t *testing.T) {
		_, appDir := loadConfigDir(t, "version: 6\n")
		cfg := newCfg("contianer-rootless", nil)

		_, err := SetAgent(managerFor(appDir), cfg, SetAgentRequest{Name: "odd", LLM: ptr(noAuthLabel)})
		require.Error(t, err, "a typo must not read as host and slip past the container-auth gate")
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

		_, err := SetAgent(managerFor(appDir), cfg, SetAgentRequest{Name: "odd", Profiles: ptr([]string{"p1"})})
		require.Error(t, err, "a hand-edited binding's typo must be refused by the next write that touches it")
		assert.Contains(t, err.Error(), "contianer-rootful")
		_, ok := readAgentFromDisk(t, appDir, "odd")
		assert.False(t, ok, "the refused write must not half-apply")
	})

	t.Run("UNSET is not a typo: it still writes exactly as before", func(t *testing.T) {
		_, appDir := loadConfigDir(t, "version: 6\n")
		cfg := newCfg("", nil)

		_, err := SetAgent(managerFor(appDir), cfg, SetAgentRequest{Name: "plain", LLM: ptr(noAuthLabel)})
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
func TestRunOneshot_RuntimeAxisIsParsedNotAsserted(t *testing.T) {
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

		_, err := RunOneshot(context.Background(), cfg, RunOneshotRequest{
			Profile: "rev", Task: "t", WorkDir: t.TempDir(), Pipeline: opPipe(cfg, loader),
		})
		require.NoError(t, err)
		require.NotNil(t, engine.gotReq, "sanity: the control really did launch an engine")
		assert.Equal(t, isolation.RuntimeContainerRootless, got.Runtime,
			"the project runtime default really does drive the member's axes — without this the refusal below could pass on a dead path")
		assert.True(t, got.WantsContainer())
	})

	t.Run("control: the other ownership mode reaches it too", func(t *testing.T) {
		_, loader := setupContextTestFS(t)
		got, engine := captureRuntimeAxis(t)
		cfg := oneshotCfg(t, string(isolation.RuntimeContainerRootful))

		_, err := RunOneshot(context.Background(), cfg, RunOneshotRequest{
			Profile: "rev", Task: "t", WorkDir: t.TempDir(), Pipeline: opPipe(cfg, loader),
		})
		require.NoError(t, err)
		require.NotNil(t, engine.gotReq, "sanity: the control really did launch an engine")
		assert.Equal(t, isolation.RuntimeContainerRootful, got.Runtime)
	})

	t.Run("a typo'd runtime refuses and no engine ever runs", func(t *testing.T) {
		_, loader := setupContextTestFS(t)
		got, engine := captureRuntimeAxis(t)
		cfg := oneshotCfg(t, "contianer-rootless")

		_, err := RunOneshot(context.Background(), cfg, RunOneshotRequest{
			Profile: "rev", Task: "t", WorkDir: t.TempDir(), Pipeline: opPipe(cfg, loader),
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "contianer-rootless")
		assert.Contains(t, err.Error(), "host|container-rootless|container-rootful")
		assert.Contains(t, err.Error(), "`runtime:`", "the refusal says which key to fix")
		assert.Nil(t, engine.gotReq, "THE POINT: the engine must never have run")
		assert.Equal(t, isolation.Axes{}, *got, "no isolation was prepared at all")
	})

	t.Run("UNSET still resolves to the existing host default", func(t *testing.T) {
		_, loader := setupContextTestFS(t)
		got, engine := captureRuntimeAxis(t)
		cfg := oneshotCfg(t, "")

		_, err := RunOneshot(context.Background(), cfg, RunOneshotRequest{
			Profile: "rev", Task: "t", WorkDir: t.TempDir(), Pipeline: opPipe(cfg, loader),
		})
		require.NoError(t, err, "a project that declares no runtime must behave exactly as it did before this key existed")
		require.NotNil(t, engine.gotReq, "and the engine still ran")
		assert.Equal(t, isolation.RuntimeAxis(""), got.Runtime, "unset passes through as unset")
		assert.False(t, got.WantsContainer(), "and still means the host")
	})
}

// -----------------------------------------------------------------------------
// delegatedAxes — the delegated-child boundary (agent_run).
// -----------------------------------------------------------------------------

// TestPrepareAgentChat_RuntimeAxisArrivesAlreadyParsed pins where the
// delegated child's runtime axis is decided. ResolvedAgent.Runtime is TYPED —
// resolveAgentBinding produced it from agent.ParseRuntimeAxis over the two
// string sources (the binding, the project default) — so delegatedAxes carries
// the typed value through rather than re-converting it. A typo on either
// source is refused at resolution, before any child is prepared.
func TestPrepareAgentChat_RuntimeAxisArrivesAlreadyParsed(t *testing.T) {
	root := t.TempDir()
	writeAgentProfileFixture(t, root)

	bindingCfg := func(runtime, projectRuntime string) *config.Config {
		return config.NewFixture(config.Fixture{
			AppPaths: []string{filepath.Join(root, ".ctxloom")},
			LM: config.LMConfig{
				Configs:  map[string]config.LLMConfig{"fast": {Type: "mock"}},
				Defaults: config.RoleDefaults{Primary: "fast"},
			},
			Agents:  map[string]agents.Agent{"builder": {LLM: "fast", Profiles: []string{"p1"}, Runtime: runtime}},
			Runtime: projectRuntime,
		})
	}

	for _, axis := range []isolation.RuntimeAxis{isolation.RuntimeContainerRootless, isolation.RuntimeContainerRootful} {
		t.Run("control: "+string(axis)+" resolves and reaches the child's axes", func(t *testing.T) {
			got, _ := captureRuntimeAxis(t)
			cfg := bindingCfg(string(axis), "")

			rs, err := ResolveAgent(context.Background(), cfg, "builder", "")
			require.NoError(t, err)
			require.Equal(t, axis, rs.Runtime, "sanity: the binding's axis survived resolution typed")

			p, err := PrepareAgentChat(context.Background(), cfg, AgentChatRequest{Resolved: rs, WorkDir: t.TempDir()})
			require.NoError(t, err)
			t.Cleanup(p.Abort)
			assert.Equal(t, axis, got.Runtime, "the resolved axis drives the child's isolation chain")
		})
	}

	t.Run("a typo'd binding runtime is refused; no child is ever prepared", func(t *testing.T) {
		got, engine := captureRuntimeAxis(t)
		cfg := bindingCfg("contianer-rootless", "")

		rs, err := ResolveAgent(context.Background(), cfg, "builder", "")
		require.Error(t, err, "a typo must not ride into ResolvedAgent.Runtime as an unread string")
		assert.Contains(t, err.Error(), "contianer-rootless")
		assert.Contains(t, err.Error(), "host|container-rootless|container-rootful")
		assert.Nil(t, rs, "THE POINT: there is no resolved agent to hand to PrepareAgentChat")
		assert.Equal(t, isolation.Axes{}, *got, "so no isolation was prepared")
		assert.Nil(t, engine.gotReq, "and no engine ever ran")
	})

	t.Run("a typo'd project runtime default is refused the same way", func(t *testing.T) {
		cfg := bindingCfg("", "contianer-rootful")

		rs, err := ResolveAgent(context.Background(), cfg, "builder", "")
		require.Error(t, err, "the binding's fallback source is parsed too, not only its own key")
		assert.Contains(t, err.Error(), "contianer-rootful")
		assert.Nil(t, rs)
	})

	t.Run("UNSET still resolves to the existing host default", func(t *testing.T) {
		got, _ := captureRuntimeAxis(t)
		cfg := bindingCfg("", "")

		rs, err := ResolveAgent(context.Background(), cfg, "builder", "")
		require.NoError(t, err)
		require.Equal(t, isolation.RuntimeAxis(""), rs.Runtime, "an agent naming no runtime keeps saying nothing")

		p, err := PrepareAgentChat(context.Background(), cfg, AgentChatRequest{Resolved: rs, WorkDir: t.TempDir()})
		require.NoError(t, err)
		t.Cleanup(p.Abort)
		assert.Equal(t, isolation.RuntimeAxis(""), got.Runtime, "unset reaches the axes as unset")
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
	names := backends.List()
	require.NotEmpty(t, names)

	sawContainer := false
	for _, backend := range names {
		offer := AgentRuntimeOffer(cfg, backend)
		require.NotEmpty(t, offer.Runtimes, "%s: host is always offered", backend)
		for _, r := range offer.Runtimes {
			parsed, err := agent.ParseRuntimeAxis(string(r))
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
