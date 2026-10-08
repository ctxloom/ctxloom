package operations

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
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
	res, err := MaterializeProfile(context.Background(), engines.Registry(), cfg, MaterializeProfileRequest{
		Profiles: cfg.DefaultAgentProfiles(), Target: dir, Backend: backend, Root: safefs.New(),
	})
	require.NoError(t, err)
	require.Contains(t, res.Wrote, "context")
}

// TestSurfaceCurrencies_StaysSilentWhereNothingIsMaterialized: the
// hook-delivered default writes no context file, and an absent file the
// record does not own is no finding.
func TestSurfaceCurrencies_StaysSilentWhereNothingIsMaterialized(t *testing.T) {
	cfg, workDir := surfaceCurrencyFixture(t, "SECURITY-RULES")

	surfaces, errs := surfaceCurrencies(context.Background(), engines.Registry(), cfg, afero.NewOsFs(), workDir)
	assert.Empty(t, errs)
	got, ok := currencyFor(surfaces, "claude-code")
	assert.False(t, ok, "claude's context reaches it through the hook, so an absent CLAUDE.md is not a finding; got %+v", got)
}

// TestSurfaceCurrencies_LeavesTheHermeticMockEngineOutOfTheReport: a
// test-only engine is never a wiring finding.
func TestSurfaceCurrencies_LeavesTheHermeticMockEngineOutOfTheReport(t *testing.T) {
	cfg, workDir := surfaceCurrencyFixture(t, "SECURITY-RULES")
	materializeInto(t, cfg, "mock", workDir)

	surfaces, _ := surfaceCurrencies(context.Background(), engines.Registry(), cfg, afero.NewOsFs(), workDir)
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

	surfaces, errs := surfaceCurrencies(context.Background(), engines.Registry(), cfg, afero.NewOsFs(), workDir)
	assert.Empty(t, errs)
	got, ok := currencyFor(surfaces, "claude-code")
	require.True(t, ok, "claude's freshly materialized context file must be reported")
	assert.Equal(t, "CLAUDE.md", got.Route)
	assert.Equal(t, string(agent.StatusDelivered), got.Status, "detail was %q", got.Detail)

	// The composition moves on: the bundle now carries other rules.
	moved := currencyConfig(t, appDir, "REVISED-RULES")
	surfaces, errs = surfaceCurrencies(context.Background(), engines.Registry(), moved, afero.NewOsFs(), workDir)
	assert.Empty(t, errs)
	got, ok = currencyFor(surfaces, "claude-code")
	require.True(t, ok)
	assert.Equal(t, string(agent.StatusStale), got.Status, "the file holds last week's copy")
}

// TestMaterialize_RedeliversASectionTheUserRemoved: materialize is the
// explicit delivery command, so a user who deleted ctxloom's section from
// CLAUDE.md gets it back on the next materialize, with their own text kept,
// and the check reports the file delivered again.
func TestMaterialize_RedeliversASectionTheUserRemoved(t *testing.T) {
	const mine = "# my own notes\n"
	cfg, workDir := surfaceCurrencyFixture(t, "SECURITY-RULES")
	contextPath := filepath.Join(workDir, "CLAUDE.md")
	require.NoError(t, os.WriteFile(contextPath, []byte(mine), 0o644))
	materializeInto(t, cfg, "claude-code", workDir)

	// The user deletes the section and goes on editing their own text, so
	// the file is neither what ctxloom wrote nor what it found.
	const edited = mine + "\nmore of mine\n"
	require.NoError(t, os.WriteFile(contextPath, []byte(edited), 0o644))
	materializeInto(t, cfg, "claude-code", workDir)

	got, err := os.ReadFile(contextPath)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(got), edited), "the user's own text is kept; got %q", got)
	assert.Contains(t, string(got), "SECURITY-RULES", "the managed section is back")
	surfaces, errs := surfaceCurrencies(context.Background(), engines.Registry(), cfg, afero.NewOsFs(), workDir)
	assert.Empty(t, errs)
	cur, ok := currencyFor(surfaces, "claude-code")
	require.True(t, ok)
	assert.Equal(t, string(agent.StatusDelivered), cur.Status, "detail was %q", cur.Detail)
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

	kind, ok := engines.Registry().Lookup(engine.Name("mock"))
	require.True(t, ok)
	pkg := composite.Package{Context: composite.Context{Text: "COMPOSED"}, Fragments: []composite.Item[composite.Fragment]{{Ref: "t#fragment/f", Value: composite.Fragment{Name: "f", Body: "COMPOSED"}}}}
	_, _, err = Deliver(context.Background(), safefs.NewMem(fs), kind, pkg, delivery.Loadout{}, atRestPlacement(dir, kind.Root().Name, delivery.AllKinds()))
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

// TestApplyHooks_Claude_WithdrawsAClaimedClaudeMdContextSection: claude's
// assembled context reaches a session through its system prompt, never a
// CLAUDE.md beside it. A CLAUDE.md section the project writer's record
// claims — here one a materialize left — is withdrawn by the next claude
// delivery, and the user's own bytes come back exactly as they were.
func TestApplyHooks_Claude_WithdrawsAClaimedClaudeMdContextSection(t *testing.T) {
	const mine = "# my own notes\n\nLive claude usage is hand-written here.\n"
	cfg, workDir := surfaceCurrencyFixture(t, "SECURITY-RULES")
	contextPath := filepath.Join(workDir, "CLAUDE.md")
	require.NoError(t, os.WriteFile(contextPath, []byte(mine), 0o644))
	materializeInto(t, cfg, "claude-code", workDir)
	claimed, err := os.ReadFile(contextPath)
	require.NoError(t, err)
	require.Contains(t, string(claimed), "SECURITY-RULES", "precondition: the record claims a context section in CLAUDE.md")

	res, err := ApplyHooks(context.Background(), engines.Registry(), ApplyHooksRequest{
		Backend: "claude-code", RegenerateContext: true, Root: safefs.New(), Cfg: cfg, WorkDir: workDir,
	})
	require.NoError(t, err)
	require.Equal(t, "applied", res.Status)

	got, err := os.ReadFile(contextPath)
	require.NoError(t, err)
	assert.Equal(t, mine, string(got), "the claimed context section is withdrawn; the hand-written CLAUDE.md is left exactly as it was")
}

// TestApplyHooks_Claude_DeliversOneContextFreeSessionStartHook: claude's
// assembled context reaches a session once, through its system prompt. The
// project settings ctxloom writes at rest carry exactly ONE SessionStart hook
// of ctxloom's context family — `hook session-start`, which delivers the
// resumed essence and the session-start notices and never the project's
// context — and nothing that could hand it the project context: no retired
// inject-context hook, no context hash on its argv, no context text in the
// file.
func TestApplyHooks_Claude_DeliversOneContextFreeSessionStartHook(t *testing.T) {
	const marker = "PROJECT-CONTEXT-MARKER-4c1e"
	cfg, workDir := surfaceCurrencyFixture(t, marker)

	res, err := ApplyHooks(context.Background(), engines.Registry(), ApplyHooksRequest{
		Backend: "claude-code", RegenerateContext: true, Root: safefs.New(), Cfg: cfg, WorkDir: workDir,
	})
	require.NoError(t, err)
	require.Equal(t, "applied", res.Status)

	raw, err := os.ReadFile(filepath.Join(workDir, ".claude", "settings.json"))
	require.NoError(t, err)
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string   `json:"command"`
				Args    []string `json:"args"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal(raw, &settings))

	var family [][]string
	for _, group := range settings.Hooks["SessionStart"] {
		for _, h := range group.Hooks {
			argv := append(strings.Fields(h.Command), h.Args...)
			if slices.Contains(argv, "inject-context") || slices.Contains(argv, "session-start") {
				family = append(family, argv)
			}
		}
	}
	require.Len(t, family, 1, "exactly one SessionStart hook of ctxloom's context family; got %q", family)
	assert.Equal(t, []string{"ctxloom", "hook", "session-start", agent.HookEngineFlag, "claude-code"}, family[0], "the one hook is session-start, naming only the engine that fires it — no argument that names a context")
	if res.ContextHash != "" {
		assert.NotContains(t, string(raw), res.ContextHash, "no hook names the regenerated context cache")
	}
	assert.NotContains(t, string(raw), marker, "the project context never rides the settings file")
}
