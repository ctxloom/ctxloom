package operations

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
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
	set := backends.BuildSurfaces(backend, agent.SurfaceInputs{Context: contextText}, afero.NewOsFs())
	delivery, err := set.SurfaceFor(agent.SurfaceContext, agent.ApproachUnsafeFile)
	require.NoError(t, err, "%s must offer a native-file context route", backend)
	_, err = delivery.Deliver(dir)
	require.NoError(t, err)
}

// composedContext is what the fixture's default agent currently assembles —
// the same string surfaceCurrencies compares against.
func composedContext(t *testing.T, cfg *config.Config) string {
	t.Helper()
	asm, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{Profiles: cfg.DefaultAgentProfiles()})
	require.NoError(t, err)
	require.NotEmpty(t, asm.Context)
	return asm.Context
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

// TestSurfaceCurrencies_ReportsMissingForEveryFileDefaultedEngine widens arm one
// across the three backends this task ported the read half onto. kiro and
// opencode both declare their owned context file the default route, so both owe
// a missing verdict; the assertion names each route so a port that reads the
// WRONG path still fails.
func TestSurfaceCurrencies_ReportsMissingForEveryFileDefaultedEngine(t *testing.T) {
	cfg, workDir := surfaceCurrencyFixture(t, "SECURITY-RULES")

	surfaces, _ := surfaceCurrencies(context.Background(), cfg, afero.NewOsFs(), workDir)

	for backend, route := range map[string]string{
		"kiro":     ".kiro/steering/ctxloom-context.md",
		"opencode": ".opencode/ctxloom-context.md",
	} {
		got, ok := currencyFor(surfaces, backend)
		require.True(t, ok, "%s must report its absent context file", backend)
		assert.Equal(t, route, got.Route)
		assert.Equal(t, string(agent.StatusMissing), got.Status)
	}
}

// --- ARM TWO: the alarm STAYS SILENT where nothing was expected --------------

// TestSurfaceCurrencies_StaysSilentForCodex is the false-alarm guard the
// ruling names by engine. codex HAS a native context file (AGENTS.md) and now
// HAS a read half for it — but its DECLARED default context route is the hook
// (a per-run content-addressed cache file), so a harpless caller has no grounds
// to expect a materialized file and must say nothing. This is the same fact
// backends.LaunchOnlySurfaces encodes for codex's other surfaces.
func TestSurfaceCurrencies_StaysSilentForCodex(t *testing.T) {
	cfg, workDir := surfaceCurrencyFixture(t, "SECURITY-RULES")

	surfaces, _ := surfaceCurrencies(context.Background(), cfg, afero.NewOsFs(), workDir)

	got, ok := currencyFor(surfaces, "codex")
	assert.False(t, ok, "codex must not be reported missing; got %+v", got)
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

// TestSurfaceCurrencies_ReportsStaleForPortedBackends is the port's payload:
// codex, kiro and opencode each get their materialized native file reported
// when it no longer matches. codex appears HERE and not in the missing test —
// a file that is actually sitting there is reported for every engine that can
// read it, expectation or not, because content nobody composes any more is
// real drift.
func TestSurfaceCurrencies_ReportsStaleForPortedBackends(t *testing.T) {
	cfg, workDir := surfaceCurrencyFixture(t, "SECURITY-RULES")

	for _, backend := range []string{"codex", "kiro", "opencode"} {
		deliverNativeContext(t, backend, workDir, "CONTEXT FROM A PREVIOUS COMPOSITION")
	}

	surfaces, errs := surfaceCurrencies(context.Background(), cfg, afero.NewOsFs(), workDir)
	assert.Empty(t, errs)

	for backend, route := range map[string]string{
		"codex":    "AGENTS.md",
		"kiro":     ".kiro/steering/ctxloom-context.md",
		"opencode": ".opencode/ctxloom-context.md",
	} {
		got, ok := currencyFor(surfaces, backend)
		require.True(t, ok, "%s's materialized context file must be reported", backend)
		assert.Equal(t, route, got.Route)
		assert.Equal(t, string(agent.StatusStale), got.Status,
			"%s carries content that no longer matches the composition", backend)
	}
}

// TestSurfaceCurrencies_ReportsDeliveredForPortedBackends closes the loop: the
// read half must agree with the write half it wraps. A frame the writer adds
// and the reader forgets to strip (kiro's `inclusion: always` front matter)
// would show up here as a permanent, unfixable "stale".
func TestSurfaceCurrencies_ReportsDeliveredForPortedBackends(t *testing.T) {
	cfg, workDir := surfaceCurrencyFixture(t, "SECURITY-RULES")
	current := composedContext(t, cfg)

	for _, backend := range []string{"claude-code", "codex", "kiro", "opencode"} {
		deliverNativeContext(t, backend, workDir, current)
	}

	surfaces, errs := surfaceCurrencies(context.Background(), cfg, afero.NewOsFs(), workDir)
	assert.Empty(t, errs)

	for _, backend := range []string{"claude-code", "codex", "kiro", "opencode"} {
		got, ok := currencyFor(surfaces, backend)
		require.True(t, ok, "%s's freshly written context file must be reported", backend)
		assert.Equal(t, string(agent.StatusDelivered), got.Status,
			"%s just wrote the composed context; detail was %q", backend, got.Detail)
		assert.Empty(t, got.Detail)
	}
}
