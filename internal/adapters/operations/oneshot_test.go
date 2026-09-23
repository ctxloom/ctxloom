package operations

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/engines"
)

func oneshotTestConfig(t *testing.T) *config.Config {
	return cfgWithDirProfiles(t, afero.NewMemMapFs(), testBaseDir, map[string]config.Profile{
		"rev": {
			LLM:       "agy-code",
			Fragments: []config.FragmentRef{{Name: "dev#fragments/go-patterns"}},
		},
	}, config.Fixture{
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				// bypass: these are the profile/context/output-flow tests,
				// not permission-resolution tests (the launch package pins
				// the floor).
				"claude-fast": {Type: "claude-code", Permissions: "bypass"},
				"agy-code":    {Type: "mock", Permissions: "bypass"},
			},
			Defaults: config.RoleDefaults{Primary: "claude-fast"},
		},
	})
}

// testLaunchDeps composes the resolver's ports over cfg with stateless
// doubles: an in-memory session store, the isolation seam stubbed to a host
// workspace, the pipeline seam for the assembler. HOME is isolated by the
// callers' fixture setup.
func testLaunchDeps(t *testing.T, cfg *config.Config, pipe *bundles.Pipeline) launch.Deps {
	t.Helper()
	stubPrepareIsolation(t, nil)
	return launch.Deps{
		Snapshot:  &config.Snapshot{Config: cfg},
		Engines:   engines.Registry(),
		Assembler: &assembler{pipe: pipe},
		Cells:     Cells{cfg: cfg},
		Endpoints: endpointMinter{},
		Sessions:  sessions.NewMemStore(),
		Host:      launch.HostFacts{Home: t.TempDir(), CtxloomHome: t.TempDir(), Binary: "ctxloom"},
	}
}

// testOneShot resolves a one-shot session over cfg on a fake host whose
// runs answer from stub.
func testOneShot(t *testing.T, cfg *config.Config, pipe *bundles.Pipeline, stub *stubEngine, src launch.Source) (*OneShot, error) {
	t.Helper()
	deps := testLaunchDeps(t, cfg, pipe)
	if src.WorkDir == "" {
		src.WorkDir = t.TempDir()
	}
	_, hosts := hostsFor(deps, stub)
	o, err := StartOneShot(context.Background(), deps, hosts, sessions.Seed{ProjectDir: src.WorkDir}, src, 0)
	if err != nil {
		return nil, err
	}
	t.Cleanup(o.End)
	return o, nil
}

// TestOneShot_ProfileLLMAndContextFlow: a profile set resolves through the
// one resolver — the profile's llm picks the engine, its context leads the
// turn, the prompt is the turn — and the answer is captured, trimmed.
func TestOneShot_ProfileLLMAndContextFlow(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := oneshotTestConfig(t)
	stub := &stubEngine{out: "  REVIEW FINDINGS  \n"}

	o, err := testOneShot(t, cfg, opPipe(cfg, loader), stub, launch.Source{Profiles: []string{"rev"}})
	require.NoError(t, err)
	assert.Equal(t, "agy-code", o.Launch.Label.Label, "the profile's llm label")
	assert.Equal(t, "mock", string(o.Launch.Engine))

	out, err := o.Turn(context.Background(), "review this diff")
	require.NoError(t, err)
	assert.Equal(t, "REVIEW FINDINGS", out)

	assert.Equal(t, "review this diff", stub.prompt())
	l := stub.launched()
	require.NotNil(t, l, "the run started on the host")
	assert.Equal(t, engine.Structured, l.Mode, "a one-shot is driven structured")
	assert.Equal(t, o.Launch.Identity.Harp, l.Identity.Harp, "the run carries the session's identity")

	// The profile's context leads the turn: it rides the launch's package.
	ctx := &stubEngine{emitContext: true}
	o2, err := testOneShot(t, cfg, opPipe(cfg, loader), ctx, launch.Source{Profiles: []string{"rev"}})
	require.NoError(t, err)
	lead, err := o2.Turn(context.Background(), "review this diff")
	require.NoError(t, err)
	assert.Contains(t, lead, "Go Patterns")
}

// TestOneShot_TurnsShareOneSession: every turn rides the same harp and the
// same endpoint; the prompt is what changes.
func TestOneShot_TurnsShareOneSession(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := oneshotTestConfig(t)
	stub := &stubEngine{echo: true}

	deps := testLaunchDeps(t, cfg, opPipe(cfg, loader))
	host, hosts := hostsFor(deps, stub)
	o, err := StartOneShot(context.Background(), deps, hosts, sessions.Seed{ProjectDir: t.TempDir()}, launch.Source{Profiles: []string{"rev"}, WorkDir: t.TempDir()}, 0)
	require.NoError(t, err)
	t.Cleanup(o.End)
	first, err := o.Turn(context.Background(), "one")
	require.NoError(t, err)
	second, err := o.Turn(context.Background(), "two")
	require.NoError(t, err)
	assert.Equal(t, "one", first)
	assert.Equal(t, "two", second)
	assert.Equal(t, 1, host.starts, "one run, many turns")
	assert.NotEmpty(t, o.Launch.MCP.URL, "the session endpoint was minted once for every turn")
}

// TestOneShot_FailedTurnNamesTheEngineFailure: an engine whose turn fails
// fails the turn with what the runner reported — the code and whatever it
// said on stderr — which a distill's caller reports (the content distiller
// leaves the item raw over it, naming this) and never a successful empty
// answer.
func TestOneShot_FailedTurnNamesTheEngineFailure(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := oneshotTestConfig(t)
	stub := &stubEngine{exitCode: 1, stderr: "quota exhausted"}
	o, err := testOneShot(t, cfg, opPipe(cfg, loader), stub, launch.Source{Profiles: []string{"rev"}})
	require.NoError(t, err)
	_, err = o.Turn(context.Background(), "distill this")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exited with code 1")
	assert.Contains(t, err.Error(), "quota exhausted")
}

// TestOneShot_EndedSessionRefusesATurn: End releases the session; a turn
// after it is refused, never silently driven on a dead harp.
func TestOneShot_EndedSessionRefusesATurn(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := oneshotTestConfig(t)
	stub := &stubEngine{out: "x"}
	o, err := testOneShot(t, cfg, opPipe(cfg, loader), stub, launch.Source{Profiles: []string{"rev"}})
	require.NoError(t, err)
	o.End()
	_, err = o.Turn(context.Background(), "again")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ended")
}

func TestResolveBackend(t *testing.T) {
	cfg := gatedFixture(config.Fixture{LM: config.LMConfig{Configs: map[string]config.LLMConfig{
		"agy-code": {Type: "mock", Body: map[string]any{"model": "gemini-3-pro"}},
	}}})
	// The degrade target is the engine shipped by default, bound by
	// validating against the composed registry — never a literal in config.
	require.NoError(t, cfg.Validate(engines.Registry()))

	t.Run("configured label resolves to its type and model", func(t *testing.T) {
		backend, model := ResolveBackend(engines.Registry(), cfg, "agy-code")
		assert.Equal(t, "mock", backend)
		assert.Equal(t, "gemini-3-pro", model)
	})

	t.Run("unknown non-backend label degrades to the default", func(t *testing.T) {
		backend, model := ResolveBackend(engines.Registry(), cfg, "no-such-label")
		assert.Equal(t, "claude-code", backend)
		assert.Empty(t, model)
	})

	t.Run("the ad-hoc arm admits only a registered backend name", func(t *testing.T) {
		backend, model := ResolveBackend(engines.Registry(), cfg, "claude-code")
		assert.Equal(t, "claude-code", backend)
		assert.Empty(t, model)
	})

	// There is no alias table, so a retired short spelling is not a backend
	// name. As an ad-hoc label it is simply an unknown label, and takes the
	// same degrade path "no-such-label" does above — it is NOT resolved to
	// the engine it used to abbreviate.
	t.Run("a retired short spelling is an unknown label, not a backend", func(t *testing.T) {
		for _, spelling := range []string{"claude", "CLAUDE", "claudecode", "Claude-Code"} {
			backend, model := ResolveBackend(engines.Registry(), cfg, spelling)
			assert.Equal(t, "claude-code", backend, "ResolveBackend(%q) degrades like any unknown label", spelling)
			assert.Empty(t, model)
		}
	})

	// A configured entry's type is validated on write (SetLLM); a hand-written
	// one that names no registered backend leaves here AS WRITTEN, so the
	// launch path refuses it as an unknown backend rather than this boundary
	// rounding it to a real one.
	t.Run("a hand-written entry's type is not rewritten", func(t *testing.T) {
		handWritten := gatedFixture(config.Fixture{LM: config.LMConfig{Configs: map[string]config.LLMConfig{
			"hand-edited": {Type: "claude", Body: map[string]any{"model": "opus"}},
		}}})
		backend, model := ResolveBackend(engines.Registry(), handWritten, "hand-edited")
		assert.Equal(t, "claude", backend)
		assert.Equal(t, "opus", model)
	})
}

// TestOneShot_InternalSourceDeclaresItsOwnPosture: an internal one-shot (the
// distiller, trigger triage) runs headless on whatever label the project
// names, and a label need not declare a posture. The floor refuses a
// headless run it would have to widen, so the internal source declares its
// posture itself rather than depend on a label or on a silent raise.
func TestOneShot_InternalSourceDeclaresItsOwnPosture(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := cfgWithDirProfiles(t, afero.NewMemMapFs(), testBaseDir, nil, config.Fixture{
		LM: config.LMConfig{
			Configs:  map[string]config.LLMConfig{"plain": {Type: "mock"}},
			Defaults: config.RoleDefaults{Primary: "plain", Fast: "plain"},
		},
	})
	o, err := testOneShot(t, cfg, opPipe(cfg, loader), &stubEngine{out: "ok"}, InternalSource("plain", "", ""))
	require.NoError(t, err, "an internal one-shot on a label declaring no posture must still launch")
	assert.Equal(t, engine.PermissionPlan, o.Launch.Permission,
		"an internal one-shot only reads and answers; its payload may carry a transcript, so it never runs at bypass")
}
