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
// backend's materialized file — the same string surfaceCurrencies compares
// against, obtained the same way.
func composedContext(t *testing.T, cfg *config.Config, backend string) string {
	t.Helper()
	composed, err := intendedContextFile(context.Background(), cfg, backend)
	require.NoError(t, err)
	require.NotEmpty(t, composed)
	return composed
}

// --- ARM ONE: the alarm FIRES where materialization was expected -------------

// TestSurfaceCurrencies_ReportsMissingWhereExpected is the finding this task
// exists for: a project whose composed context has content, an engine that
// declares the native file its DEFAULT context route, and no file on disk. That
// was silent before — the one case a user most needs told about.
func TestSurfaceCurrencies_ReportsMissingWhereExpected(t *testing.T) {
	cfg, workDir := surfaceCurrencyFixture(t, "SECURITY-RULES")

	surfaces, errs := surfaceCurrencies(context.Background(), cfg, afero.NewOsFs(), workDir)
	assert.Empty(t, errs)

	claude, ok := currencyFor(surfaces, "claude-code")
	require.True(t, ok, "claude-code declares CLAUDE.md its default context route, so its absence is a finding")
	assert.Equal(t, "CLAUDE.md", claude.Route)
	assert.Equal(t, string(agent.StatusMissing), claude.Status)
	assert.Equal(t, "CLAUDE.md does not exist", claude.Detail)
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
	assert.NotEmpty(t, surfaces, "the real engines are still reported")
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

	_, report := reportableContextCurrency(absent, "", true)
	assert.False(t, report, "an empty composition expects no file, so an absent one is not a finding")

	_, report = reportableContextCurrency(absent, "   \n\t ", true)
	assert.False(t, report, "whitespace is no context at all — same verdict as empty")

	cur, report := reportableContextCurrency(absent, "REAL CONTEXT", true)
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

	cur, report := reportableContextCurrency(present, "NEW CONTEXT", false)
	require.True(t, report, "an unexpected-but-present file is still real drift")
	assert.Equal(t, agent.StatusStale, cur.Status)

	cur, report = reportableContextCurrency(present, "OLD CONTEXT", false)
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

// --- THE CHECK COMPOSES WHAT MATERIALIZE WROTE ------------------------------

// premisedDefaultAgentFixture builds a project whose DEFAULT agent composes a
// bundle carrying one premised fragment (body mark) — the content whose
// delivery differs by engine — plus an empty dir to materialize into. The
// default agent matters for the same reason as in surfaceCurrencyFixture: it
// is what the check composes, so materialize must be handed the same profiles.
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

// TestIntendedContextFile_IsWhatMaterializeWrote pins static-vs-dynamic
// delivery to WHAT IS WRITTEN rather than to whichever caller is composing:
// `manage check` composes the file it compares against through
// intendedContextFile, MaterializeProfile composes the file it writes, and for
// the same engine the two must be the same bytes — otherwise a correct
// materialization is reported stale for as long as it exists.
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

			intended, err := intendedContextFile(context.Background(), cfg, tc.backend)
			require.NoError(t, err)
			// Not vacuous: the two arms must compose DIFFERENT bytes for the
			// premised fragment, or "they agree" is satisfied by a resolution
			// that ignores the engine and always picks one mode.
			assert.Equal(t, tc.premisedInContext, strings.Contains(intended, "PREMISED-MARKER"),
				"%s: the check's composition must carry the premised fragment exactly when the engine has no skills surface to re-deliver it through", tc.backend)
			assert.Contains(t, intended, "UNCONDITIONAL-MARKER")

			decl := backends.Declared(tc.backend)
			reader, ok := contextFileReader(decl, afero.NewOsFs())
			require.True(t, ok, "%s must offer a native-file context route", tc.backend)
			state, err := reader.State(target)
			require.NoError(t, err)
			cur, report := reportableContextCurrency(state, intended, contextFileExpected(decl))
			require.True(t, report, "%s: a materialized file that exists is always reported", tc.backend)
			assert.Equal(t, agent.StatusDelivered, cur.Status,
				"%s: the check composed something other than what materialize wrote for the same engine — %s", tc.backend, cur.Detail)
		})
	}
}
