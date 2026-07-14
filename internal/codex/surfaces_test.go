package codex

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// captureStderr redirects os.Stderr around fn and returns what was written. The
// Unsafe adapter's WARN streams through clidiag → os.Stderr with no recorded
// finding, so this is how the Unsafe test observes the loud line.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	fn()

	require.NoError(t, w.Close())
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(out)
}

// sampleInputs is a representative, fully-populated SurfaceInputs.
func sampleInputs() agent.SurfaceInputs {
	return agent.SurfaceInputs{
		Fragments: []*agent.Fragment{{Name: "rules", Content: "the secret color is vermilion"}},
		MCP: &wire.MCPConfig{
			Servers: map[string]wire.MCPServer{
				"config-server": {Command: "config-cmd", Args: []string{"--flag"}},
			},
		},
		BundleMCP: map[string]wire.MCPServer{
			"bundle-server": {Command: "bundle-cmd", SCM: "ctxloom-bundle:test"},
		},
		Hooks: &wire.HooksConfig{
			Unified: wire.UnifiedHooks{
				// A ctxloom-managed command (recognized by codex's removeManagedHooks
				// on cleanup, which keys on the ctxloom executable token — a companion
				// hook like "ltk evaluate" carries no marker and would persist).
				SessionStart: []wire.Hook{{Command: "ctxloom hook session-start"}},
			},
		},
		Skills: []agent.CommandExport{
			{Name: "review", Content: "Review {{file}}", Enabled: true, Description: "Code review"},
		},
	}
}

// ---- compile-time capability guarantees -----------------------------------
//
// Every codex surface is Delivery-ONLY (codex exposes no out-of-cwd redirect and
// no SharedRealization), proven by these assignments compiling.
var (
	_ agent.Delivery = (*contextSurface)(nil)
	_ agent.Delivery = (*configSurface)(nil)
	_ agent.Delivery = (*agent.ManagedSkillsDelivery)(nil) // codex's prompts surface
)

// No codex surface has a SharedRealization (codex has no --mcp-config /
// --settings / --append-system-prompt equivalent), so a SHARED-cwd delivery of
// any codex surface always falls back to the loud well-known write — proven live
// in TestUnsafe_WarnsAndProceeds below.

// contextFile returns the single .md context file WriteContextFile wrote under
// dir's context cache (there is exactly one for one delivery).
func contextFile(t *testing.T, fs afero.Fs, dir string) string {
	t.Helper()
	cacheDir := filepath.Join(dir, agent.SCMContextSubdir)
	entries, err := afero.ReadDir(fs, cacheDir)
	require.NoError(t, err)
	var md []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".md" {
			md = append(md, filepath.Join(cacheDir, e.Name()))
		}
	}
	require.Len(t, md, 1, "exactly one context file")
	return md[0]
}

// ---- context surface -------------------------------------------------------

// context Delivery writes the raw context file (via agent.WriteContextFile) into
// dir's well-known cache and its Cleanup removes it.
func TestContextSurface_DeliverWritesContextFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	s := NewSurfaces(sampleInputs(), fs)

	handle, err := s.Context.Deliver(dir)
	require.NoError(t, err)

	path := contextFile(t, fs, dir)
	got, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	assert.Equal(t, "the secret color is vermilion", string(got), "the context file holds the assembled context")

	require.NoError(t, handle.Cleanup())
	exists, _ := afero.Exists(fs, path)
	assert.False(t, exists, "cleanup reverses the context-file write")
}

// Empty context writes nothing and cleans up to a no-op.
func TestContextSurface_EmptyContextIsNoOp(t *testing.T) {
	fs := afero.NewMemMapFs()
	s := NewSurfaces(agent.SurfaceInputs{}, fs)

	handle, err := s.Context.Deliver("/proj")
	require.NoError(t, err)
	assert.Nil(t, handle, "empty context delivers nothing, so no cleanup handle")

	exists, _ := afero.Exists(fs, filepath.Join("/proj", agent.SCMContextSubdir))
	assert.False(t, exists, "no context, nothing written")
}

// taskloom tiny-ooze: the materialize path only ever populates
// SurfaceInputs.Context (the assembled STRING) — never Fragments, because
// AssembleContext only returns a flattened string, not resolved Fragment
// objects (see internal/operations/profile_materialize.go). Before codex had
// a ContextWriter, this meant materialize silently delivered ZERO context to
// codex: the fragments-keyed cache-file route saw an empty fragment slice and
// no-op'd. codex's context surface must ALSO deliver via AGENTS.md (managed
// markers) whenever in.Context is non-empty, regardless of whether Fragments
// was populated.
func TestContextSurface_DeliverWritesAGENTSmdFromContextString(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	// Materialize shape: Context is populated, Fragments is NOT (this is the
	// exact input shape profile_materialize.go builds for codex).
	s := NewSurfaces(agent.SurfaceInputs{Context: "the secret color is vermilion"}, fs)

	handle, err := s.Context.Deliver(dir)
	require.NoError(t, err)
	require.NotNil(t, handle, "materialize-shaped input (Context, no Fragments) must still deliver codex context")

	data, err := afero.ReadFile(fs, filepath.Join(dir, "AGENTS.md"))
	require.NoError(t, err, "codex now owns a native AGENTS.md context surface")
	assert.Contains(t, string(data), "the secret color is vermilion")

	require.NoError(t, handle.Cleanup())
	exists, _ := afero.Exists(fs, filepath.Join(dir, "AGENTS.md"))
	assert.False(t, exists, "cleanup strips the managed section; wholly-managed file is removed")
}

// Hand-authored AGENTS.md content outside the managed markers survives
// materialize's write byte-for-byte (the other half of lanky-plop's fix:
// AGENTS.md is a file codex users may already hand-author).
func TestContextSurface_DeliverPreservesHandWrittenAGENTSmd(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	require.NoError(t, afero.WriteFile(fs, filepath.Join(dir, "AGENTS.md"), []byte("# Team conventions\nalways use tabs\n"), 0644))
	s := NewSurfaces(agent.SurfaceInputs{Context: "the secret color is vermilion"}, fs)

	handle, err := s.Context.Deliver(dir)
	require.NoError(t, err)
	require.NotNil(t, handle)

	data, err := afero.ReadFile(fs, filepath.Join(dir, "AGENTS.md"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "always use tabs", "hand-written content survives")
	assert.Contains(t, string(data), "the secret color is vermilion")

	require.NoError(t, handle.Cleanup())
	data, err = afero.ReadFile(fs, filepath.Join(dir, "AGENTS.md"))
	require.NoError(t, err, "the hand-authored file survives cleanup (not wholly ctxloom's)")
	assert.Contains(t, string(data), "always use tabs")
	assert.NotContains(t, string(data), "the secret color is vermilion", "the managed section is gone after cleanup")
}

// ---- config surface (folded settings + hooks + MCP) ------------------------

// config Delivery writes .codex/config.toml (hooks + mcp_servers) into dir via
// the reused WriteSettings; Cleanup reverts the ctxloom-managed entries.
func TestConfigSurface_DeliverWritesConfigTOML(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	s := NewSurfaces(sampleInputs(), fs)

	handle, err := s.Config.Deliver(dir)
	require.NoError(t, err)

	cfgPath := filepath.Join(dir, ".codex", "config.toml")
	cfg := readConfig(t, fs, cfgPath)
	servers := asMap(cfg["mcp_servers"])
	require.NotNil(t, servers, "config surface carries the MCP servers")
	assert.Contains(t, servers, agent.MCPServerName)
	assert.Contains(t, servers, "config-server")
	assert.Contains(t, servers, "bundle-server")
	assert.Contains(t, cfg, "hooks", "config surface carries the hooks (folded into config.toml)")

	require.NoError(t, handle.Cleanup())
	cfg = readConfig(t, fs, cfgPath)
	assert.NotContains(t, asMap(cfg["mcp_servers"]), agent.MCPServerName, "cleanup reverts the managed ctxloom server")
	assert.NotContains(t, cfg, "hooks", "cleanup reverts the managed hooks")
}

// ---- skills surface (cell-scoped $CODEX_HOME) ------------------------------

// skills Delivery writes prompts under the CELL-SCOPED $CODEX_HOME derived from
// dir (<dir>/.codex/prompts) — NOT the global ~/.codex/prompts — so a
// DirectoryIsolatedCell isolates them. Cleanup reverts the manifest-tracked set.
func TestSkillsSurface_DeliverWritesCellScopedPrompts(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	s := NewSurfaces(sampleInputs(), fs)

	handle, err := s.Skills.Deliver(dir)
	require.NoError(t, err)

	promptPath := filepath.Join(dir, ".codex", "prompts", "review.md")
	exists, _ := afero.Exists(fs, promptPath)
	assert.True(t, exists, "the prompt lands under the cell-scoped $CODEX_HOME (<dir>/.codex/prompts)")
	assert.Equal(t, filepath.Join(dir, ".codex", "prompts"), cellScopedPromptsDir(dir))
	assert.Equal(t, filepath.Join(dir, ".codex"), cellScopedCodexHome(dir),
		"CODEX_HOME is <dir>/.codex, so the launched codex reads these prompts")

	require.NoError(t, handle.Cleanup())
	exists, _ = afero.Exists(fs, promptPath)
	assert.False(t, exists, "cleanup reverts the manifest-tracked prompt")
}

// ---- no SharedRealization (the only path to a shared cwd) ------------------

// A codex surface has no out-of-cwd flag and no SharedRealization, so
// DeliverShared falls back to the well-known write — WARNING to stderr AND
// PROCEEDING (a sanctioned, permitted action, never a fatal abort).
func TestUnsafe_WarnsAndProceeds(t *testing.T) {
	fs := afero.NewMemMapFs()
	cwd := "/live-cwd"
	s := NewSurfaces(sampleInputs(), fs)

	r, err := agent.Select(s).WithSkills(agent.SkillsWriteUnsafeFile).Build()
	require.NoError(t, err)

	var delivered []agent.Delivered
	stderr := captureStderr(t, func() {
		var errs []error
		delivered, _, errs = r.DeliverShared(cwd)
		require.Empty(t, errs)
	})
	require.Len(t, delivered, 1)

	assert.Contains(t, stderr, "warning:", "the fallback streams a loud WARN")
	assert.Contains(t, stderr, "skills")
	assert.Contains(t, stderr, "shared cwd")

	// It PROCEEDED: the well-known write lands under the shared cwd.
	exists, _ := afero.Exists(fs, filepath.Join(cwd, ".codex", "prompts", "review.md"))
	assert.True(t, exists, "the well-known write proceeded into the shared cwd")
	require.NoError(t, delivered[0].Cleanup())
}

// ---- cells wiring ----------------------------------------------------------

// An isolated cell accepts EVERY codex surface as a plain Delivery — Deliveries()
// is the iteration set for a worktree / container / materialize target, and the
// only cell type codex's Delivery-only surfaces can enter directly.
func TestDirectoryIsolatedCell_AcceptsAllCodexSurfaces(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/worktree"
	s := NewSurfaces(sampleInputs(), fs)

	ds := s.Deliveries()
	require.Len(t, ds, 3, "context, config, skills")

	cell := agent.NewDirectoryIsolatedCell(dir)
	for _, surface := range ds {
		d, err := cell.Deliver(surface)
		require.NoError(t, err)
		require.NotNil(t, d)
	}

	// The private-dir writes all landed at codex's well-known cell-local locations.
	contextFile(t, fs, dir) // asserts exactly one context file exists in the cell
	exists, _ := afero.Exists(fs, filepath.Join(dir, ".codex", "config.toml"))
	assert.True(t, exists)
	exists, _ = afero.Exists(fs, filepath.Join(dir, ".codex", "prompts", "review.md"))
	assert.True(t, exists)
}

// DeliverShared falls back to the well-known write for every codex surface (codex
// has no SharedRealization), and each warns when it delivers.
func TestSharedCell_AcceptsOnlyUnsafeCodexSurfaces(t *testing.T) {
	fs := afero.NewMemMapFs()
	cwd := "/live"
	s := NewSurfaces(sampleInputs(), fs)

	r, err := agent.Select(s).WithEverything().Build()
	require.NoError(t, err)

	var delivered []agent.Delivered
	stderr := captureStderr(t, func() {
		var errs []error
		delivered, _, errs = r.DeliverShared(cwd)
		require.Empty(t, errs)
	})
	require.Len(t, delivered, 3, "context (cache file), config, and skills each deliver")
	assert.Contains(t, stderr, "warning:", "every codex surface warns when DeliverShared delivers it")
}

// ---- approach dispatch (vital-tiger v2) -------------------------------------

// SupportedApproaches pins codex's per-surface table: context is Hook-only (codex
// has no CLAUDE.md-style native file), settings/skills are native-file-only, and
// MCP is absent (folded into the config/settings surface).
func TestSurfaces_SupportedApproaches(t *testing.T) {
	s := NewSurfaces(sampleInputs(), afero.NewMemMapFs())

	assert.Equal(t, []agent.Approach{agent.ApproachHook}, s.SupportedApproaches(agent.SurfaceContext))
	assert.Equal(t, []agent.Approach{agent.ApproachUnsafeFile}, s.SupportedApproaches(agent.SurfaceSettings))
	assert.Equal(t, []agent.Approach{agent.ApproachUnsafeFile}, s.SupportedApproaches(agent.SurfaceSkills))
	assert.Empty(t, s.SupportedApproaches(agent.SurfaceMCP), "MCP folds into config.toml — no distinct surface")
}

// DefaultApproach is Hook for context (codex's only approach), the native file for
// settings/skills, and absent for the folded MCP kind.
func TestSurfaces_DefaultApproach(t *testing.T) {
	s := NewSurfaces(sampleInputs(), afero.NewMemMapFs())

	a, ok := s.DefaultApproach(agent.SurfaceContext)
	require.True(t, ok)
	assert.Equal(t, agent.ApproachHook, a)

	a, ok = s.DefaultApproach(agent.SurfaceSettings)
	require.True(t, ok)
	assert.Equal(t, agent.ApproachUnsafeFile, a)

	_, ok = s.DefaultApproach(agent.SurfaceMCP)
	assert.False(t, ok, "MCP is folded/absent for codex")
}

// SurfaceFor(context, Hook) resolves to the real cache-file write (codex's ONLY
// DECLARED context approach performs real work, unlike claude's Hook no-op);
// naming UnsafeFile for codex's context is unsupported — codex's native
// AGENTS.md write rides the SAME Hook-declared contextSurface (see
// codexApproaches' doc comment), it is not a separately-selectable approach.
func TestSurfaceFor_ContextHookWritesCacheFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	s := NewSurfaces(sampleInputs(), fs)

	d, err := s.SurfaceFor(agent.SurfaceContext, agent.ApproachHook)
	require.NoError(t, err)
	dir := t.TempDir()
	handle, err := d.Deliver(dir)
	require.NoError(t, err)
	require.NotNil(t, handle, "codex's Hook approach performs the real cache-file write")

	_, err = s.SurfaceFor(agent.SurfaceContext, agent.ApproachUnsafeFile)
	assert.Error(t, err, "UnsafeFile is not a separately-selectable approach for codex's context")
}

// SharedRealization is absent for every kind: codex has no out-of-cwd redirect,
// so a SHARED-cwd delivery always falls back to the loud well-known write.
func TestSurfaces_SharedRealization_Absent(t *testing.T) {
	s := NewSurfaces(sampleInputs(), afero.NewMemMapFs())
	for _, kind := range []agent.SurfaceKind{agent.SurfaceContext, agent.SurfaceSettings, agent.SurfaceSkills} {
		_, ok := s.SharedRealization(kind)
		assert.False(t, ok, "%s: codex has no out-of-cwd realization", kind)
	}
}
