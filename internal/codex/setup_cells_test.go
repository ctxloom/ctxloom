package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sessionInstance gives a test the per-session codex home a `config_home:
// project` run gets: the env CODEX_HOME contribution operations.
// InTreeAgentHomeEnv makes, and the home it names. Every Setup test that
// exercises codex's HOME-KEYED surfaces needs one, because since D2 a run with
// no contributed CODEX_HOME keeps the user's own ~/.codex — which ctxloom
// refuses to write (surfaces.go's deliveryHome), so those surfaces would
// deliver nothing at all.
func sessionInstance(t *testing.T, work string) (env map[string]string, home string) {
	t.Helper()
	root, err := SessionHome(work, "ugly-icy-squid")
	require.NoError(t, err)
	home = cellScopedCodexHome(root)
	return map[string]string{CodexHomeEnv: home}, home
}

// contextCacheHash returns the single hash under <work>/.ctxloom/cache/context.
func contextCacheHash(t *testing.T, work string) string {
	t.Helper()
	dir := filepath.Join(work, agent.SCMContextSubdir)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err, "the raw context cache dir must exist")
	require.Len(t, entries, 1, "exactly one context cache file")
	return strings.TrimSuffix(entries[0].Name(), ".md")
}

// TestCodex_Setup_DirectoryIsolated_ArtifactsAndHook drives codex Setup in an
// isolated cell and asserts the full cell package: the raw context cache file, the
// config.toml carrying the SessionStart inject-context hook KEYED TO the delivered
// context hash, the cell-scoped prompts under <ProjectHome(work)>/prompts, and
// the CODEX_HOME env pointing there.
func TestCodex_Setup_DirectoryIsolated_ArtifactsAndHook(t *testing.T) {
	work := t.TempDir()
	b := NewCodex()

	managed := &agent.ManagedConfig{
		Commands: []agent.CommandExport{{Name: "demo", Content: "do a thing", Enabled: true}},
		Hooks: &wire.HooksConfig{Unified: wire.UnifiedHooks{
			PreTool: []wire.Hook{{Command: "ctxloom hook guard", Type: "command"}},
		}},
		BundleMCP: map[string]wire.MCPServer{"srv": {Command: "run-srv"}},
	}
	env, home := sessionInstance(t, work)
	require.NoError(t, b.Setup(context.Background(), &agent.SetupRequest{
		WorkDir:   work,
		Env:       env,
		Fragments: []*agent.Fragment{{Content: "project rules"}},
		CellKind:  agent.CellKindDirectoryIsolated,
		Managed:   managed,
	}))

	// The raw cache file is on disk (the SessionStart hook reads it at run time).
	hash := contextCacheHash(t, work)

	// config.toml carries the inject-context SessionStart hook keyed to that hash.
	cfg, err := os.ReadFile(filepath.Join(home, ConfigFileName))
	require.NoError(t, err, "the config surface must write config.toml under this session's codex home")
	config := string(cfg)
	assert.Contains(t, config, "inject-context", "codex fires the SessionStart context-injection hook")
	assert.Contains(t, config, hash, "the injection hook is keyed to the delivered context hash")
	assert.Contains(t, config, "run-srv", "the managed MCP server is written to config.toml")

	// Cell-scoped prompts live under this session's instance (NOT the global ~/.codex).
	assert.FileExists(t, filepath.Join(home, PromptsDirName, "demo.md"),
		"commands are delivered to the cell-scoped prompts dir")

	// CODEX_HOME points codex at the same home so it discovers those prompts.
	execEnv := b.ExecuteEnv(&agent.ExecuteRequest{WorkDir: work, Env: env, CellKind: agent.CellKindDirectoryIsolated})
	assert.Equal(t, home, execEnv[CodexHomeEnv],
		"Execute's CODEX_HOME is the instance Setup delivered into")
}

// TestCodex_Setup_WritesAGENTSmd proves the LIVE RUN/LAUNCH path — not just
// `profile materialize` — now delivers codex's context via the native
// AGENTS.md route (taskloom steep-lapel: a live, authenticated `ctxloom run`
// against codex was measured to never deliver a planted sentinel; codex's
// SessionStart-hook+cache-file route was the ONLY route, and it silently
// diverged from what was actually assembled). `ctxloom run` calls this exact
// Setup → setupViaCells → NewSurfaces → SurfaceFor(SurfaceContext, Hook) chain
// (internal/shared/agent/launch_backend.go), building SurfaceInputs.Context
// from assembleDedupedContext(req.Fragments) — the SAME deduped/assembled
// string every other backend's ContextWriter already reads. So codex's
// AGENTS.md route (agent.ComposedDelivery, this fix) reaches the live run path with NO
// launch_backend.go changes: it rides the same NewSurfaces codex already
// wires into its CellDelivery (backend.go).
func TestCodex_Setup_WritesAGENTSmd(t *testing.T) {
	work := t.TempDir()
	b := NewCodex()

	env, _ := sessionInstance(t, work)
	require.NoError(t, b.Setup(context.Background(), &agent.SetupRequest{
		WorkDir:   work,
		Env:       env,
		Fragments: []*agent.Fragment{{Content: "the secret color is vermilion"}},
		CellKind:  agent.CellKindDirectoryIsolated,
		Managed:   &agent.ManagedConfig{},
	}))

	data, err := os.ReadFile(filepath.Join(work, "AGENTS.md"))
	require.NoError(t, err, "codex's native AGENTS.md context surface must be written on the run/launch path, not just materialize")
	assert.Contains(t, string(data), "the secret color is vermilion")

	// The hash-file route the SessionStart hook reads is STILL written too —
	// this fix is additive, not a replacement of the existing route.
	hash := contextCacheHash(t, work)
	assert.NotEmpty(t, hash)
}

// TestCodex_CodexHomeEnv_AllCellsExceptSkipSetup proves CODEX_HOME is scoped to
// this session's instance in every NON-container cell (including SharedCell, so
// codex finds the cell-scoped prompts for the live cwd), and is left unset for
// a minimal/distill (SkipSetup) run so codex keeps its global ~/.codex home. A
// ProcessIsolated (container) cell is asserted separately below
// (TestCodex_CodexHomeEnv_ProcessIsolated_UsesContainerHome, dense-amaze): it
// scopes to the container's own fresh $HOME instead, since the project tree
// there is the bind-mounted PROJECT dir, where the isolation layer never mounts
// creds.
func TestCodex_CodexHomeEnv_AllCellsExceptSkipSetup(t *testing.T) {
	b := NewCodex()
	work := t.TempDir()
	env, want := sessionInstance(t, work)

	for _, cell := range []agent.CellKind{agent.CellKindShared, agent.CellKindDirectoryIsolated} {
		got := b.ExecuteEnv(&agent.ExecuteRequest{WorkDir: work, Env: env, CellKind: cell})
		assert.Equal(t, want, got[CodexHomeEnv], "CODEX_HOME is set for cell %v", cell)
	}

	// No Env here on purpose: ExecuteEnv layers req.Env in first, so passing
	// the instance would prove nothing about the backend's own contributor.
	skipped := b.ExecuteEnv(&agent.ExecuteRequest{WorkDir: work, CellKind: agent.CellKindShared, SkipSetup: true})
	_, ok := skipped[CodexHomeEnv]
	assert.False(t, ok, "a SkipSetup run keeps codex's global home (no CODEX_HOME override)")
}

// TestCodex_HomeKeyedSurfaces_RefuseTheRealHostHome is the ruling's hardest
// line at the delivery seam: a run with no controlled home (no binding, an
// undeclared one, or `config_home: host`) keeps the user's own ~/.codex, and
// ctxloom writes NOTHING into it — not config.toml, not prompts, not skills.
// The refusal is LOUD, because a delivery that silently wrote nothing is this
// project's signature failure wearing a success's clothes. The CWD-KEYED
// context surface still delivers, so the run is degraded, not broken.
func TestCodex_HomeKeyedSurfaces_RefuseTheRealHostHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-test")
	work := t.TempDir()

	b := NewCodex()
	out := captureStderr(t, func() {
		require.NoError(t, b.Setup(context.Background(), &agent.SetupRequest{
			WorkDir:   work,
			Fragments: []*agent.Fragment{{Content: "project rules"}},
			CellKind:  agent.CellKindShared,
			Managed: &agent.ManagedConfig{
				Commands: []agent.CommandExport{{Name: "demo", Content: "do a thing", Enabled: true}},
				Hooks: &wire.HooksConfig{Unified: wire.UnifiedHooks{
					PreTool: []wire.Hook{{Command: "ctxloom hook guard", Type: "command"}},
				}},
				BundleMCP: map[string]wire.MCPServer{"srv": {Command: "run-srv"}},
			},
		}))
	})

	entries, err := os.ReadDir(home)
	require.NoError(t, err)
	// paths.AppDirName (~/.ctxloom) is the ONE tolerated entry: this Setup's
	// managed AGENTS.md write goes through agent.WriteManagedContext, which
	// (as of the home-lock-dir fix) takes filelock.HomePathFor's lock around
	// every managed-file RMW regardless of the TARGET's own location — the
	// lock's home is ctxloom's own ~/.ctxloom, never the engine's ~/.codex,
	// so its existence here proves nothing about the invariant this test
	// actually guards (ctxloom writes NOTHING into the user's real ~/.codex).
	for _, e := range entries {
		assert.Equal(t, paths.AppDirName, e.Name(),
			"ctxloom created something inside the user's real home besides its own %s (the home lock dir any WithFileLock acquisition touches): %v", paths.AppDirName, entries)
	}

	assert.Contains(t, out, "config_home: project", "the refusal names the fix")
	assert.Contains(t, out, filepath.Join(home, ConfigDirName), "and the home it refused to write")

	// Degraded, not broken: the cwd-keyed context surface still landed.
	assert.FileExists(t, filepath.Join(work, AgentsMDFile), "AGENTS.md is cwd-keyed and unaffected")
}

// TestCodex_CodexHomeEnv_ProcessIsolated_UsesContainerHome is dense-amaze's
// ExecuteEnv-level assertion: a ProcessIsolated (container) cell scopes
// CODEX_HOME to the container's OWN fresh $HOME (where codexCredentialMounts
// bind-mounts auth.json — isolation/auth.go), never <WorkDir>/.codex (the
// bind-mounted project dir this used to silently fall back to, landing on an
// empty, unauthenticated home and a silent 401).
func TestCodex_CodexHomeEnv_ProcessIsolated_UsesContainerHome(t *testing.T) {
	t.Setenv("HOME", "/home/ctxloom")
	b := NewCodex()
	env := b.ExecuteEnv(&agent.ExecuteRequest{WorkDir: "/workspace/proj", CellKind: agent.CellKindProcessIsolated})
	assert.Equal(t, "/home/ctxloom/.codex", env["CODEX_HOME"])
}

// TestCodex_Setup_ConfigByteIdenticalToDirectWrite pins byte-identity: the
// config.toml the cell path's config surface writes is exactly what a direct
// MergeManaged + CodexHookWriter write on the merged state produces for the
// same workDir — same [hooks] (incl. the inject-context hook keyed to the same
// hash), same [mcp_servers], same [projects] trust pre-seed. Both run in the
// SAME workDir so the inject-context hook's --project path matches.
//
// The direct half calls WriteSettingsWithTrust, not the bare WriteSettings,
// because the RUN path pre-seeds workspace trust on every axis (the home is one
// ctxloom provisioned and codex has never seen — see docs/trust-model.md).
// Comparing against a trust-less write would not be a weaker assertion, it
// would be a false one: the two writes genuinely differ, and the run path's
// version is the correct one.
func TestCodex_Setup_ConfigByteIdenticalToDirectWrite(t *testing.T) {
	managed := &agent.ManagedConfig{
		Hooks: &wire.HooksConfig{Unified: wire.UnifiedHooks{
			PreTool: []wire.Hook{{Command: "ctxloom hook guard", Type: "command"}},
		}},
		BundleMCP: map[string]wire.MCPServer{"srv": {Command: "run-srv"}},
	}
	fragments := []*agent.Fragment{{Content: "project rules"}}
	work := t.TempDir()
	env, home := sessionInstance(t, work)
	configPath := filepath.Join(home, ConfigFileName)

	// New path: the cell delivery writes config.toml via the config surface.
	b := NewCodex()
	require.NoError(t, b.Setup(context.Background(), &agent.SetupRequest{
		WorkDir:   work,
		Env:       env,
		Fragments: fragments,
		CellKind:  agent.CellKindDirectoryIsolated,
		Managed:   managed,
	}))
	newCfg, err := os.ReadFile(configPath)
	require.NoError(t, err)
	hash := contextCacheHash(t, work)

	// Direct path in the SAME dir: delete the config surface's file, then reproduce
	// it via a fresh BaseLifecycle merge + CodexHookWriter.WriteSettings on the
	// merged hooks/MCP, keyed to the same hash — the write the config surface wraps.
	require.NoError(t, os.Remove(configPath))
	life := agent.NewBaseLifecycle("codex")
	life.MergeManaged(managed, work, hash)
	absWork, err := filepath.Abs(work)
	require.NoError(t, err)
	instanceRoot, err := SessionHome(work, "ugly-icy-squid")
	require.NoError(t, err)
	require.NoError(t, (&CodexHookWriter{}).writeSettingsIn(life.GetHooks(), life.GetBundleMCP(), instanceRoot, absWork))
	directCfg, err := os.ReadFile(configPath)
	require.NoError(t, err)

	assert.Equal(t, string(directCfg), string(newCfg),
		"the cell path's config.toml is byte-identical to a direct merged-state write")
}
