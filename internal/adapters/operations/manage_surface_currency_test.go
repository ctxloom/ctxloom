package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/agents"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// surfaceCurrencyFixture is a project whose default agent composes one
// fragment carrying mark, so a materialized context file either holds
// mark (delivered) or does not (stale).
func surfaceCurrencyFixture(t *testing.T, mark string) (cfg *config.Config, workDir string) {
	t.Helper()
	testsupport.Isolate(t)
	appDir, workDir := regenTestApp(t)
	return currencyConfig(t, appDir, mark), workDir
}

// currencyConfig (re)composes the fixture's one fragment as mark.
func currencyConfig(t *testing.T, appDir, mark string) *config.Config {
	t.Helper()
	writeRegenBundle(t, appDir, "dev", `version: "1.0"
fragments:
  rules:
    tags: ["security"]
    content: "`+mark+`"
`)
	return cfgWithDirProfiles(t, afero.NewOsFs(), appDir, map[string]config.Profile{
		"reviewer": {SelectTags: []string{"security"}},
	}, config.Fixture{
		DefaultAgent: "primary",
		Agents:       map[string]agents.Agent{"primary": {Profiles: []string{"reviewer"}}},
	})
}

func currencyFor(surfaces []SurfaceCurrency, backend string) (SurfaceCurrency, bool) {
	for _, s := range surfaces {
		if s.Backend == backend {
			return s, true
		}
	}
	return SurfaceCurrency{}, false
}

// materializeInto delivers the default agent's profile set for backend at
// rest into dir — what `profile materialize --target dir` does — so the
// project writer's record owns the context file the check reads.
func materializeInto(t *testing.T, cfg *config.Config, backend, dir string) {
	t.Helper()
	res, err := MaterializeProfile(context.Background(), cfg, MaterializeProfileRequest{
		Profiles: cfg.DefaultAgentProfiles(), Target: dir, Backend: backend, FS: afero.NewOsFs(),
	})
	require.NoError(t, err)
	require.Contains(t, res.Wrote, "context")
}

// TestSurfaceCurrencies_StaysSilentWhereNothingIsMaterialized: the
// hook-delivered default writes no context file, and an absent file the
// record does not own is no finding.
func TestSurfaceCurrencies_StaysSilentWhereNothingIsMaterialized(t *testing.T) {
	cfg, workDir := surfaceCurrencyFixture(t, "SECURITY-RULES")

	surfaces, errs := surfaceCurrencies(context.Background(), cfg, afero.NewOsFs(), workDir)
	assert.Empty(t, errs)
	got, ok := currencyFor(surfaces, "claude-code")
	assert.False(t, ok, "claude's context reaches it through the hook, so an absent CLAUDE.md is not a finding; got %+v", got)
}

// TestSurfaceCurrencies_LeavesTheHermeticMockEngineOutOfTheReport: a
// test-only engine is never a wiring finding.
func TestSurfaceCurrencies_LeavesTheHermeticMockEngineOutOfTheReport(t *testing.T) {
	cfg, workDir := surfaceCurrencyFixture(t, "SECURITY-RULES")
	materializeInto(t, cfg, "mock", workDir)

	surfaces, _ := surfaceCurrencies(context.Background(), cfg, afero.NewOsFs(), workDir)
	got, ok := currencyFor(surfaces, "mock")
	assert.False(t, ok, "mock must not appear in the report; got %+v", got)
}

// TestSurfaceCurrencies_ReportsAMaterializedFileTheCompositionMovedAwayFrom:
// a materialize into the project root leaves a file the record owns; when
// the composition moves on the check names it stale, and a fresh
// materialize makes it delivered again.
func TestSurfaceCurrencies_ReportsAMaterializedFileTheCompositionMovedAwayFrom(t *testing.T) {
	testsupport.Isolate(t)
	appDir, workDir := regenTestApp(t)
	cfg := currencyConfig(t, appDir, "SECURITY-RULES")
	materializeInto(t, cfg, "claude-code", workDir)

	surfaces, errs := surfaceCurrencies(context.Background(), cfg, afero.NewOsFs(), workDir)
	assert.Empty(t, errs)
	got, ok := currencyFor(surfaces, "claude-code")
	require.True(t, ok, "claude's freshly materialized context file must be reported")
	assert.Equal(t, "CLAUDE.md", got.Route)
	assert.Equal(t, string(agent.StatusDelivered), got.Status, "detail was %q", got.Detail)

	// The composition moves on: the bundle now carries other rules.
	moved := currencyConfig(t, appDir, "REVISED-RULES")
	surfaces, errs = surfaceCurrencies(context.Background(), moved, afero.NewOsFs(), workDir)
	assert.Empty(t, errs)
	got, ok = currencyFor(surfaces, "claude-code")
	require.True(t, ok)
	assert.Equal(t, string(agent.StatusStale), got.Status, "the file holds last week's copy")
}

// TestContextFileCurrency_ReadsOnlyWhatTheRecordOwns: a context file the
// project writer never wrote is nobody's finding, however stale; one it
// wrote and that is gone is missing.
func TestContextFileCurrency_ReadsOnlyWhatTheRecordOwns(t *testing.T) {
	testsupport.Isolate(t)
	fs := afero.NewOsFs()
	dir := t.TempDir()
	records, err := OwnershipRecordsOn(fs)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("hand-written"), 0o644))

	_, owned, err := contextFileCurrency(fs, records, dir, "CLAUDE.md", "COMPOSED")
	require.NoError(t, err)
	assert.False(t, owned, "the user's own file is not ctxloom's to judge")

	kind, ok := backends.Kind("mock")
	require.True(t, ok)
	pkg := composite.Package{Context: composite.Context{Text: "COMPOSED"}, Fragments: []composite.Item[composite.Fragment]{{Ref: "t#fragment/f", Value: composite.Fragment{Name: "f", Body: "COMPOSED"}}}}
	_, _, err = DeliverProject(context.Background(), fs, kind, pkg, dir)
	require.NoError(t, err)
	rel, ok := contextFileOf(kind)
	require.True(t, ok)
	cur, owned, err := contextFileCurrency(fs, records, dir, rel, "COMPOSED")
	require.NoError(t, err)
	require.True(t, owned)
	assert.Equal(t, agent.StatusDelivered, cur.Status)

	require.NoError(t, os.Remove(filepath.Join(dir, rel)))
	cur, owned, err = contextFileCurrency(fs, records, dir, rel, "COMPOSED")
	require.NoError(t, err)
	require.True(t, owned)
	assert.Equal(t, agent.StatusMissing, cur.Status)
}
