package operations

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/agent/present"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// surfaceCurrencyFixture builds a project whose DEFAULT agent composes a
// non-empty context (one tag-selected fragment carrying mark), plus an empty
// work dir with nothing materialized in it. The default agent matters: it is
// what cfg.DefaultAgentProfiles feeds AssembleContext, and therefore what
// decides whether this loadout carries anything destined for a file surface.
func surfaceCurrencyFixture(t *testing.T, mark string) (cfg *config.Config, workDir string) {
	t.Helper()
	testsupport.Isolate(t)
	appDir, workDir := regenTestApp(t)
	writeRegenBundle(t, appDir, "dev", `version: "1.0"
fragments:
  rules:
    tags: ["security"]
    content: "`+mark+`"
`)
	cfg = cfgWithDirProfiles(t, afero.NewOsFs(), appDir, map[string]config.Profile{
		"reviewer": {SelectTags: []string{"security"}},
	}, config.Fixture{
		DefaultAgent: "primary",
		Agents:       map[string]agents.Agent{"primary": {Profiles: []string{"reviewer"}}},
	})
	return cfg, workDir
}

// currencyFor returns the reported currency for one backend, and whether the
// report mentions that backend at all. Absence is a VERDICT here (silence), so
// it is returned rather than fataled on.
func currencyFor(surfaces []SurfaceCurrency, backend string) (SurfaceCurrency, bool) {
	for _, s := range surfaces {
		if s.Backend == backend {
			return s, true
		}
	}
	return SurfaceCurrency{}, false
}

// deliverNativeContext materializes one backend's NATIVE-FILE context route
// into dir, through the very surface the read half answers for — so a test
// that then reads it back is comparing the read side against the real write
// side, not against a hand-rolled imitation of it.
func deliverNativeContext(t *testing.T, backend, dir, contextText string) {
	t.Helper()
	delivery, ok := backends.Declared(backend).Construct(agent.SurfaceContext, agent.ApproachUnsafeFile, agent.SurfaceInputs{Context: contextText}, afero.NewOsFs())
	require.True(t, ok, "%s must offer a native-file context route", backend)
	_, err := delivery.Deliver(present.ProjectOnHost(dir))
	require.NoError(t, err)
}

// composedContext is what the fixture's default agent currently composes for
// backend's native file — obtained the way surfaceCurrencies obtains it. It
// requires every writer of that file to compose the same bytes, so a test
// materializing "the current composition" through it is not silently picking
// one writer's side of a divergence.
func composedContext(t *testing.T, cfg *config.Config, backend string) string {
	t.Helper()
	intended, err := intendedContextFiles(context.Background(), cfg, backend)
	require.NoError(t, err)
	require.NotEmpty(t, intended)
	for _, composed := range intended {
		require.NotEmpty(t, composed)
		require.Equal(t, intended[0], composed, "%s: its writers compose different files; name the writer instead of using this helper", backend)
	}
	return intended[0]
}

// --- ARM ONE: the alarm stays SILENT where the file is not the route ----------

// TestSurfaceCurrencies_StaysSilentWhereTheFileIsNotTheRoute is the false
// alarm this predicate exists to prevent: claude declares hook-carried
// context, so `manage hooks install` never writes CLAUDE.md — and a check
// that reports it missing tells every hooks-installed claude project its
// context is gone. The expectation is read from the SAME predicate the
// install writes by (installedThroughProjectFile), so the two cannot
// disagree about which file is the route.
func TestSurfaceCurrencies_StaysSilentWhereTheFileIsNotTheRoute(t *testing.T) {
	cfg, workDir := surfaceCurrencyFixture(t, "SECURITY-RULES")

	surfaces, errs := surfaceCurrencies(context.Background(), cfg, afero.NewOsFs(), workDir)
	assert.Empty(t, errs)

	got, ok := currencyFor(surfaces, "claude-code")
	assert.False(t, ok, "claude's context reaches it through the hook, so an absent CLAUDE.md is not a finding; got %+v", got)
}

// --- ARM TWO: the alarm STAYS SILENT where nothing was expected --------------

// TestSurfaceCurrencies_LeavesTheHermeticMockEngineOutOfTheReport keeps a test
// engine out of a user's wiring report. mock has no settings surface and is
// absent from every other line of `manage check`; a missing verdict that fires
// on an ABSENT file would have put MOCK_CONTEXT.md in front of every real user.
func TestSurfaceCurrencies_LeavesTheHermeticMockEngineOutOfTheReport(t *testing.T) {
	cfg, workDir := surfaceCurrencyFixture(t, "SECURITY-RULES")

	surfaces, _ := surfaceCurrencies(context.Background(), cfg, afero.NewOsFs(), workDir)

	got, ok := currencyFor(surfaces, "mock")
	assert.False(t, ok, "mock must not appear in the report; got %+v", got)
}

// TestReportableContextCurrency_StaysSilentWhenTheLoadoutCarriesNothing is the
// other half of the rule backends.UncarriedSurfaces states: a loadout that
// carries no context carries nothing destined for a file surface, so no
// engine's absent file costs anything — even an engine that declares the file
// its default route.
//
// It exercises the rule directly rather than through surfaceCurrencies because
// an EMPTY composition is not reachable from configuration: the builtin
// isolation fragment is always injected, so AssembleContext never returns "" for
// any config a user can write. The predicate still has to hold the line for the
// case that IS reachable — an assembly that resolved to nothing.
func TestReportableContextCurrency_StaysSilentWhenTheLoadoutCarriesNothing(t *testing.T) {
	absent := agent.FileDeliveryState{Rel: "CLAUDE.md"}

	_, report := reportableContextCurrency(absent, []string{""}, true)
	assert.False(t, report, "an empty composition expects no file, so an absent one is not a finding")

	_, report = reportableContextCurrency(absent, []string{"   \n\t "}, true)
	assert.False(t, report, "whitespace is no context at all — same verdict as empty")

	cur, report := reportableContextCurrency(absent, []string{"REAL CONTEXT"}, true)
	require.True(t, report, "context to deliver plus an expected file plus nothing there IS the finding")
	assert.Equal(t, agent.StatusMissing, cur.Status)
}

// TestReportableContextCurrency_AlwaysReportsAFileThatExists pins the asymmetry:
// the expectation gates only the MISSING verdict. A file sitting on disk is
// reported for any engine that can read it, because it is evidence the engine
// materialized there and its content is now drifting.
func TestReportableContextCurrency_AlwaysReportsAFileThatExists(t *testing.T) {
	present := agent.FileDeliveryState{
		Rel: "AGENTS.md", Found: true, HasSection: true, Managed: "OLD CONTEXT",
	}

	cur, report := reportableContextCurrency(present, []string{"NEW CONTEXT"}, false)
	require.True(t, report, "an unexpected-but-present file is still real drift")
	assert.Equal(t, agent.StatusStale, cur.Status)

	cur, report = reportableContextCurrency(present, []string{"OLD CONTEXT"}, false)
	require.True(t, report)
	assert.Equal(t, agent.StatusDelivered, cur.Status)
}

// --- PART ONE: the ported read halves actually read ---------------------------

// TestSurfaceCurrencies_ReportsStaleForPortedBackends is the port's payload: a
// backend gets its materialized native file reported when it no longer
// matches. A file that is actually sitting there is reported for every engine
// that can read it, expectation or not, because content nobody composes any
// more is real drift.
func TestSurfaceCurrencies_ReportsStaleForPortedBackends(t *testing.T) {
	cfg, workDir := surfaceCurrencyFixture(t, "SECURITY-RULES")

	for _, backend := range []string{"claude-code"} {
		deliverNativeContext(t, backend, workDir, "CONTEXT FROM A PREVIOUS COMPOSITION")
	}

	surfaces, errs := surfaceCurrencies(context.Background(), cfg, afero.NewOsFs(), workDir)
	assert.Empty(t, errs)

	for backend, route := range map[string]string{
		"claude-code": "CLAUDE.md",
	} {
		got, ok := currencyFor(surfaces, backend)
		require.True(t, ok, "%s's materialized context file must be reported", backend)
		assert.Equal(t, route, got.Route)
		assert.Equal(t, string(agent.StatusStale), got.Status,
			"%s carries content that no longer matches the composition", backend)
	}
}

// TestSurfaceCurrencies_ReportsDeliveredForPortedBackends closes the loop: the
// read half must agree with the write half it wraps. Any frame the writer adds
// and the reader forgets to strip would show up here as a permanent,
// unfixable "stale".
func TestSurfaceCurrencies_ReportsDeliveredForPortedBackends(t *testing.T) {
	cfg, workDir := surfaceCurrencyFixture(t, "SECURITY-RULES")
	current := composedContext(t, cfg, "claude-code")

	for _, backend := range []string{"claude-code"} {
		deliverNativeContext(t, backend, workDir, current)
	}

	surfaces, errs := surfaceCurrencies(context.Background(), cfg, afero.NewOsFs(), workDir)
	assert.Empty(t, errs)

	for _, backend := range []string{"claude-code"} {
		got, ok := currencyFor(surfaces, backend)
		require.True(t, ok, "%s's freshly written context file must be reported", backend)
		assert.Equal(t, string(agent.StatusDelivered), got.Status,
			"%s just wrote the composed context; detail was %q", backend, got.Detail)
		assert.Empty(t, got.Detail)
	}
}

// --- THE CHECK COMPOSES WHAT EACH WRITER WROTE --------------------------------

// premisedDefaultAgentFixture builds a project whose DEFAULT agent composes a
// bundle carrying one premised fragment (body mark) — the content whose
// delivery differs by engine — plus an empty dir to write into. The default
// agent matters for the same reason as in surfaceCurrencyFixture: it is what
// the check composes, so materialize must be handed the same profiles.
func premisedDefaultAgentFixture(t *testing.T, mark string) (cfg *config.Config, target string) {
	t.Helper()
	testsupport.Isolate(t)
	appDir, target := regenTestApp(t)
	bundleDir := filepath.Join(authoredV1(appDir), "premise-bundle")
	require.NoError(t, os.MkdirAll(bundleDir, 0o755))
	// `premise:` is the flat v1 key; the v2 tree format spells it `description:`.
	require.NoError(t, os.WriteFile(filepath.Join(bundleDir, "bundle.yaml"),
		[]byte("version: \"1.0\"\nfragments:\n"+
			"  always-applies:\n    content: \"UNCONDITIONAL-MARKER\"\n"+
			"  only-sometimes:\n    premise: \"You are about to cut a release.\"\n    content: \""+mark+"\"\n"), 0o644))
	cfg = cfgWithDirProfiles(t, afero.NewOsFs(), appDir, map[string]config.Profile{
		"premised": {Bundles: []string{"premise-bundle"}},
	}, config.Fixture{
		DefaultAgent: "primary",
		Agents:       map[string]agents.Agent{"primary": {Profiles: []string{"premised"}}},
	})
	return cfg, target
}

// nativeContextState reads backend's native context file under dir through
// the same read half surfaceCurrencies walks, and returns the managed bytes
// alongside it so a test can assert on CONTENT, not merely on agreement.
func nativeContextState(t *testing.T, backend, dir string) (agent.DeliveryState, string) {
	t.Helper()
	reader, ok := contextFileReader(backends.Declared(backend), afero.NewOsFs())
	require.True(t, ok, "%s must offer a native-file context route", backend)
	state, err := reader.State(dir)
	require.NoError(t, err)
	file, ok := state.(agent.FileDeliveryState)
	require.True(t, ok, "%s's file route must read back as a FileDeliveryState, got %T", backend, state)
	require.True(t, file.HasSection, "%s: nothing was written into %s", backend, dir)
	return state, file.Managed
}

// checkVerdict is what `manage check` would say about backend's native file
// under dir, composed the way surfaceCurrencies composes it.
func checkVerdict(t *testing.T, cfg *config.Config, backend, dir string) agent.Currency {
	t.Helper()
	intended, err := intendedContextFiles(context.Background(), cfg, backend)
	require.NoError(t, err)
	state, _ := nativeContextState(t, backend, dir)
	cur, report := reportableContextCurrency(state, intended, installedThroughProjectFile(backends.Declared(backend), agent.SurfaceContext))
	require.True(t, report, "%s: a native file that exists is always reported", backend)
	return cur
}

// hooksInstall runs `manage hooks install` for backend into dir, regenerating
// context so the native file is written — the second writer of the file the
// check reads.
func hooksInstall(t *testing.T, cfg *config.Config, backend, dir string) {
	t.Helper()
	res, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend:           backend,
		RegenerateContext: true,
		FS:                afero.NewOsFs(),
		ConfigLoader:      func() (*config.Config, error) { return cfg, nil },
		WorkDir:           dir,
	})
	require.NoError(t, err)
	require.Empty(t, res.Errors, "%s: hooks install must write cleanly", backend)
	require.NotEmpty(t, res.ContextHash, "%s: context must have been regenerated, or the native file is never written", backend)
}

// TestIntendedContextFile_IsWhatMaterializeWrote pins static-vs-dynamic
// delivery to WHAT IS WRITTEN rather than to whichever caller is composing:
// `manage check` composes the files it compares against through
// intendedContextFiles, MaterializeProfile composes the file it writes, and for
// the same engine the check must hold what materialize wrote as delivered —
// otherwise a correct materialization is reported stale for as long as it
// exists.
//
// Both arms are asserted because they resolve to DIFFERENT deliveries. An
// engine with a skills surface gets the dynamic assembly (its premised
// fragments become skill packages); one without gets the static assembly (they
// are dumped into the context, since nothing behind a materialized surface can
// pull them later). A caller picking its own mode agrees with the writer on
// one arm by coincidence and diverges on the other, so a single-arm assertion
// cannot tell a shared resolution from a lucky one. mock-noskills is the
// engine without a skills mapper.
func TestIntendedContextFile_IsWhatMaterializeWrote(t *testing.T) {
	for _, tc := range []struct {
		backend           string
		premisedInContext bool
	}{
		{backend: "mock", premisedInContext: false},
		{backend: "mock-noskills", premisedInContext: true},
	} {
		t.Run(tc.backend, func(t *testing.T) {
			cfg, target := premisedDefaultAgentFixture(t, "PREMISED-MARKER")
			_, err := MaterializeProfile(context.Background(), cfg, MaterializeProfileRequest{
				Profiles: cfg.DefaultAgentProfiles(), Target: target, Backend: tc.backend,
			})
			require.NoError(t, err)

			// Not vacuous: the file must carry the premised fragment exactly
			// when the engine has no skills surface to re-deliver it through,
			// or "delivered" is satisfied by a check that accepts anything.
			_, written := nativeContextState(t, tc.backend, target)
			assert.Equal(t, tc.premisedInContext, strings.Contains(written, "PREMISED-MARKER"),
				"%s: materialize must write the premised fragment into the file exactly when the engine has no skills surface", tc.backend)
			assert.Contains(t, written, "UNCONDITIONAL-MARKER")

			cur := checkVerdict(t, cfg, tc.backend, target)
			assert.Equal(t, agent.StatusDelivered, cur.Status,
				"%s: the check composed nothing matching what materialize wrote for the same engine — %s", tc.backend, cur.Detail)
		})
	}
}

// TestIntendedContextFile_IsWhatHooksInstallWrote is the other writer of the
// same file. `manage hooks install` composes for a LIVE session — the hooks
// and ctxloom's own MCP server it installs beside the file are ctxloom staying
// in the loop, so a launch pulls a withheld fragment on demand — and it
// therefore withholds the premised fragment for EVERY engine, skills surface
// or not. The check must hold that file as delivered too.
//
// The no-skills engine is the arm that matters: it is the one where the two
// writers legitimately compose different bytes for one file, and where a check
// that states only materialize's subject reports a correct hooks-installed
// file stale forever.
func TestIntendedContextFile_IsWhatHooksInstallWrote(t *testing.T) {
	for _, backend := range []string{"mock", "mock-noskills"} {
		t.Run(backend, func(t *testing.T) {
			cfg, workDir := premisedDefaultAgentFixture(t, "PREMISED-MARKER")
			hooksInstall(t, cfg, backend, workDir)

			_, written := nativeContextState(t, backend, workDir)
			assert.NotContains(t, written, "PREMISED-MARKER",
				"%s: hooks install writes for a live session, which pulls a premised fragment on demand — its body must be withheld from the file", backend)
			assert.Contains(t, written, "UNCONDITIONAL-MARKER")

			cur := checkVerdict(t, cfg, backend, workDir)
			assert.Equal(t, agent.StatusDelivered, cur.Status,
				"%s: the check composed nothing matching what hooks install wrote for the same engine — %s", backend, cur.Detail)
		})
	}
}

// TestIntendedContextFiles_HoldTheFileToItsWriterNotToOneMode pins the shape
// of the resolution rather than either arm alone: for the engine without a
// skills surface the two writers produce DIFFERENT files — materialize carries
// the premised body, hooks install withholds it — and the check must call
// each one delivered while still calling a file that matches neither stale. A
// check composing one subject passes exactly one writer; a check accepting
// anything passes the stale file. Only composing every subject a writer
// states survives all three.
func TestIntendedContextFiles_HoldTheFileToItsWriterNotToOneMode(t *testing.T) {
	const backend = "mock-noskills"
	cfg, materialized := premisedDefaultAgentFixture(t, "PREMISED-MARKER")
	installed := filepath.Join(filepath.Dir(materialized), "installed")
	require.NoError(t, os.MkdirAll(installed, 0o755))

	_, err := MaterializeProfile(context.Background(), cfg, MaterializeProfileRequest{
		Profiles: cfg.DefaultAgentProfiles(), Target: materialized, Backend: backend,
	})
	require.NoError(t, err)
	hooksInstall(t, cfg, backend, installed)

	_, byMaterialize := nativeContextState(t, backend, materialized)
	_, byInstall := nativeContextState(t, backend, installed)
	require.NotEqual(t, byMaterialize, byInstall,
		"the two writers must compose different files for an engine without a skills surface, or this test pins nothing")
	assert.Contains(t, byMaterialize, "PREMISED-MARKER")
	assert.NotContains(t, byInstall, "PREMISED-MARKER")

	assert.Equal(t, agent.StatusDelivered, checkVerdict(t, cfg, backend, materialized).Status, "materialize's file is current under its writer")
	assert.Equal(t, agent.StatusDelivered, checkVerdict(t, cfg, backend, installed).Status, "hooks install's file is current under its writer")

	intended, err := intendedContextFiles(context.Background(), cfg, backend)
	require.NoError(t, err)
	neither := agent.FileDeliveryState{Rel: "MOCK_CONTEXT.md", Found: true, HasSection: true, Managed: "CONTEXT FROM A PREVIOUS COMPOSITION"}
	cur, report := reportableContextCurrency(neither, intended, true)
	require.True(t, report)
	assert.Equal(t, agent.StatusStale, cur.Status, "a file matching no writer's composition is real drift")
}
