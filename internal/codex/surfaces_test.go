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
		Context:   "the secret color is vermilion",
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
		Commands: []agent.CommandExport{
			{Name: "review", Content: "Review {{file}}", Enabled: true, Description: "Code review"},
		},
		Skills: []agent.SkillExport{
			{
				Name:        "humanize",
				Description: "Removes AI writing tells",
				Enabled:     true,
				Files: []agent.PackageFile{
					{RelPath: "SKILL.md", Content: []byte("---\nname: humanize\ndescription: Removes AI writing tells\n---\n\nBody.\n"), Mode: 0644},
					{RelPath: "scripts/run.sh", Content: []byte("#!/bin/sh\necho hi\n"), Mode: 0755},
				},
			},
			{Name: "disabled-skill", Enabled: false, Files: []agent.PackageFile{{RelPath: "SKILL.md", Content: []byte("nope")}}},
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
	_ agent.Delivery = (*agent.ManagedCommandsDelivery)(nil) // codex's prompts surface
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
	s := NewSurfaces(sampleInputs(), "", "", fs)

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
	s := NewSurfaces(agent.SurfaceInputs{}, "", "", fs)

	handle, err := s.Context.Deliver("/proj")
	require.NoError(t, err)
	assert.Nil(t, handle, "empty context delivers nothing, so no cleanup handle")

	exists, _ := afero.Exists(fs, filepath.Join("/proj", agent.SCMContextSubdir))
	assert.False(t, exists, "no context, nothing written")
}

// A past review claimed contextSurface.Deliver's (nil, nil) on empty fragments
// is a silent no-op — "success with zero bytes delivered". It is the opposite: the
// nil handle is how "nothing was asked for" is REPORTED (agent.DeliverAll skips
// a nil handle by contract, and the composed route still delivers AGENTS.md
// from the context STRING). The delivery that WOULD be a silent no-op — asking
// for fragments and assembling zero bytes out of them — is an error, and this
// pins that asymmetry. Do not "fix" the empty case into an error: materialize
// legitimately has no fragments.
func TestContextSurface_ZeroBytesFromRealFragmentsIsAnError(t *testing.T) {
	fs := afero.NewMemMapFs()
	s := NewSurfaces(agent.SurfaceInputs{Fragments: []*agent.Fragment{{Name: "rules", Content: ""}}}, "", "", fs)

	handle, err := s.Context.Deliver("/proj")
	require.Error(t, err, "fragments were asked for and produced nothing — that is the silent no-op worth refusing")
	assert.ErrorIs(t, err, agent.ErrNoContext)
	assert.Nil(t, handle)
}

// ---- agentsMD surface (the OTHER, native context route) --------------------

// The materialize path only ever populates SurfaceInputs.Context (the
// assembled STRING) — never Fragments, because
// AssembleContext only returns a flattened string, not resolved Fragment
// objects (see internal/operations/profile_materialize.go). Before codex had
// a ContextWriter, this meant materialize silently delivered ZERO context to
// codex: the fragments-keyed cache-file route (contextSurface) saw an empty
// fragment slice and no-op'd. codex's NEW agentsMDSurface must deliver via
// AGENTS.md (managed markers) whenever in.Context is non-empty, regardless of
// whether Fragments was populated.
func TestAgentsMDSurface_DeliverWritesAGENTSmdFromContextString(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	// Materialize shape: Context is populated, Fragments is NOT (this is the
	// exact input shape profile_materialize.go builds for codex).
	s := NewSurfaces(agent.SurfaceInputs{Context: "the secret color is vermilion"}, "", "", fs)

	handle, err := s.AgentsMD.Deliver(dir)
	require.NoError(t, err)
	require.NotNil(t, handle, "materialize-shaped input (Context, no Fragments) must still deliver codex context")

	data, err := afero.ReadFile(fs, filepath.Join(dir, "AGENTS.md"))
	require.NoError(t, err, "codex now owns a native AGENTS.md context surface")
	assert.Contains(t, string(data), "the secret color is vermilion")

	require.NoError(t, handle.Cleanup())
	exists, _ := afero.Exists(fs, filepath.Join(dir, "AGENTS.md"))
	assert.False(t, exists, "cleanup strips the managed section; wholly-managed file is removed")
}

// Empty context writes nothing to disk (the file never existed, so
// WriteContext("") is a harmless no-op), but — unlike contextSurface's
// hash-file route — Deliver still returns a real handle: agentsMDSurface
// follows claude's own native-file ContextWriter surface,
// which always delivers.
func TestAgentsMDSurface_EmptyContextStillDelivers(t *testing.T) {
	fs := afero.NewMemMapFs()
	s := NewSurfaces(agent.SurfaceInputs{}, "", "", fs)

	handle, err := s.AgentsMD.Deliver("/proj")
	require.NoError(t, err)
	require.NotNil(t, handle, "matches claude's own always-delivers context surface")

	exists, _ := afero.Exists(fs, "/proj/AGENTS.md")
	assert.False(t, exists, "empty content creates nothing")

	require.NoError(t, handle.Cleanup())
}

// Hand-authored AGENTS.md content outside the managed markers survives
// materialize's write byte-for-byte (the other half of the managed-markers
// fix: AGENTS.md is a file codex users may already hand-author).
func TestAgentsMDSurface_DeliverPreservesHandWrittenAGENTSmd(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	require.NoError(t, afero.WriteFile(fs, filepath.Join(dir, "AGENTS.md"), []byte("# Team conventions\nalways use tabs\n"), 0644))
	s := NewSurfaces(agent.SurfaceInputs{Context: "the secret color is vermilion"}, "", "", fs)

	handle, err := s.AgentsMD.Deliver(dir)
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
	s := NewSurfaces(sampleInputs(), "", "", fs)

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

// ---- commands surface (cell-scoped $CODEX_HOME) ----------------------------

// commands Delivery writes prompts under the CELL-SCOPED $CODEX_HOME derived from
// dir (<dir>/.codex/prompts) — NOT the global ~/.codex/prompts — so a
// DirectoryIsolatedCell isolates them. Cleanup reverts the manifest-tracked set.
func TestCommandsSurface_DeliverWritesCellScopedPrompts(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	s := NewSurfaces(sampleInputs(), "", "", fs)

	handle, err := s.Commands.Deliver(dir)
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

// ---- skills surface (cell-scoped $CODEX_HOME) ------------------------------

// skills Delivery writes Agent Skill packages under the CELL-SCOPED $CODEX_HOME
// derived from dir (<dir>/.codex/skills) — NOT the global ~/.codex/skills — so a
// DirectoryIsolatedCell isolates them, mirroring the commands surface. Cleanup
// reverts the manifest-tracked set.
func TestSkillsSurface_DeliverWritesCellScopedSkills(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	s := NewSurfaces(sampleInputs(), "", "", fs)

	handle, err := s.Skills.Deliver(dir)
	require.NoError(t, err)

	skillMD := filepath.Join(dir, ".codex", "skills", "humanize", "SKILL.md")
	exists, _ := afero.Exists(fs, skillMD)
	assert.True(t, exists, "the skill lands under the cell-scoped $CODEX_HOME (<dir>/.codex/skills)")
	assert.Equal(t, filepath.Join(dir, ".codex", "skills"), cellScopedSkillsDir(dir))

	data, err := afero.ReadFile(fs, skillMD)
	require.NoError(t, err)
	assert.Contains(t, string(data), "Body.")

	scriptPath := filepath.Join(dir, ".codex", "skills", "humanize", "scripts", "run.sh")
	info, err := fs.Stat(scriptPath)
	require.NoError(t, err, "scripts/run.sh must be materialized")
	assert.Equal(t, os.FileMode(0755), info.Mode().Perm(), "the exec bit on scripts/run.sh survives codex's skills surface")

	exists, _ = afero.Exists(fs, filepath.Join(dir, ".codex", "skills", "disabled-skill", "SKILL.md"))
	assert.False(t, exists, "a skill with Enabled == false must not be written")

	require.NoError(t, handle.Cleanup())
	exists, _ = afero.Exists(fs, skillMD)
	assert.False(t, exists, "cleanup reverts the manifest-tracked skill")
}

// ---- no SharedRealization (the only path to a shared cwd) ------------------

// A codex surface has no out-of-cwd flag and no SharedRealization, so
// DeliverShared falls back to the well-known write — WARNING to stderr AND
// PROCEEDING (a sanctioned, permitted action, never a fatal abort).
func TestUnsafe_WarnsAndProceeds(t *testing.T) {
	fs := afero.NewMemMapFs()
	cwd := "/live-cwd"
	s := NewSurfaces(sampleInputs(), "", "", fs)

	r, err := agent.Select(s).WithCommands(agent.CommandsWriteUnsafeFile).Build()
	require.NoError(t, err)

	var delivered []agent.Delivered
	stderr := captureStderr(t, func() {
		var errs []error
		delivered, _, errs = r.DeliverShared(cwd)
		require.Empty(t, errs)
	})
	require.Len(t, delivered, 1)

	assert.Contains(t, stderr, "warning:", "the fallback streams a loud WARN")
	assert.Contains(t, stderr, "commands")
	assert.Contains(t, stderr, "shared cwd")

	// It PROCEEDED: the well-known write lands under the shared cwd.
	exists, _ := afero.Exists(fs, filepath.Join(cwd, ".codex", "prompts", "review.md"))
	assert.True(t, exists, "the well-known write proceeded into the shared cwd")
	require.NoError(t, delivered[0].Cleanup())
}

// ---- cells wiring ----------------------------------------------------------

// An isolated cell accepts EVERY codex surface as a plain Delivery — Deliveries()
// is the iteration set for a worktree / container / materialize target, and the
// only cell type codex's Delivery-only surfaces can enter directly. The
// composed context route (agent.ComposedDelivery) counts as ONE delivery here — it
// performs BOTH the hash-file write and the native AGENTS.md write internally.
func TestDirectoryIsolatedCell_AcceptsAllCodexSurfaces(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/worktree"
	s := NewSurfaces(sampleInputs(), "", "", fs)

	// The surfaces reach a cell through the approach-resolved selection — the
	// same path the launch path drives. There is deliberately no raw,
	// unresolved SurfaceSet.Deliveries() to call instead: it materializes the
	// identical tree and has no production caller.
	resolved, err := agent.Select(s).WithEverything().Build()
	require.NoError(t, err)
	ds := resolved.Deliveries()
	require.Len(t, ds, 4, "context (composed), config, commands, skills")

	cell := agent.NewIsolatedCell(dir)
	for _, surface := range ds {
		d, err := cell.Deliver(surface)
		require.NoError(t, err)
		require.NotNil(t, d)
	}

	// The private-dir writes all landed at codex's well-known cell-local locations.
	contextFile(t, fs, dir) // asserts exactly one context file exists in the cell
	exists, _ := afero.Exists(fs, filepath.Join(dir, "AGENTS.md"))
	assert.True(t, exists, "the native AGENTS.md context route also landed")
	exists, _ = afero.Exists(fs, filepath.Join(dir, ".codex", "config.toml"))
	assert.True(t, exists)
	exists, _ = afero.Exists(fs, filepath.Join(dir, ".codex", "prompts", "review.md"))
	assert.True(t, exists)
	exists, _ = afero.Exists(fs, filepath.Join(dir, ".codex", "skills", "humanize", "SKILL.md"))
	assert.True(t, exists)
}

// DeliverShared falls back to the well-known write for every codex surface (codex
// has no SharedRealization), and each warns when it delivers. Still 4 resolved
// surfaces: context (composed: cache file + AGENTS.md), config, commands, and
// skills — the composition happens INSIDE ComposedDelivery.Deliver, so it is
// still one resolved surface at the SurfaceKind level.
func TestSharedCell_AcceptsOnlyUnsafeCodexSurfaces(t *testing.T) {
	fs := afero.NewMemMapFs()
	cwd := "/live"
	s := NewSurfaces(sampleInputs(), "", "", fs)

	r, err := agent.Select(s).WithEverything().Build()
	require.NoError(t, err)

	var delivered []agent.Delivered
	stderr := captureStderr(t, func() {
		var errs []error
		delivered, _, errs = r.DeliverShared(cwd)
		require.Empty(t, errs)
	})
	require.Len(t, delivered, 4, "context (cache file + AGENTS.md), config, commands, and skills each deliver")
	assert.Contains(t, stderr, "warning:", "every codex surface warns when DeliverShared delivers it")

	// The composed context route wrote BOTH its cache file and AGENTS.md into
	// the shared cwd (sampleInputs sets both Fragments and Context).
	exists, _ := afero.Exists(fs, filepath.Join(cwd, "AGENTS.md"))
	assert.True(t, exists, "DeliverShared's context surface wrote AGENTS.md too, not just the cache file")
}

// ---- approach dispatch (v2) -------------------------------------

// SupportedApproaches pins codex's per-surface table. Context declares BOTH the
// Hook route and the native file: codex reads a workspace-fixed AGENTS.md by
// itself, so claiming it has no native-file approach under-reported the engine
// and made `--surface context=unsafe-file` a refusal for something codex
// actually does. Hook stays FIRST because ApproachTable.Default is the first
// entry, and both materialize and launch resolve through WithEverything —
// reordering here would silently change what every default delivery does.
func TestSurfaces_SupportedApproaches(t *testing.T) {
	s := NewSurfaces(sampleInputs(), "", "", afero.NewMemMapFs())

	assert.Equal(t, []agent.Approach{agent.ApproachHook, agent.ApproachUnsafeFile},
		s.SupportedApproaches(agent.SurfaceContext))
	def, ok := s.DefaultApproach(agent.SurfaceContext)
	require.True(t, ok)
	assert.Equal(t, agent.ApproachHook, def,
		"the DEFAULT must remain the composed Hook route; adding a nameable approach must not change what an unqualified materialize or launch delivers")
	assert.Equal(t, []agent.Approach{agent.ApproachUnsafeFile}, s.SupportedApproaches(agent.SurfaceSettings))
	assert.Equal(t, []agent.Approach{agent.ApproachUnsafeFile}, s.SupportedApproaches(agent.SurfaceCommands))
	assert.Equal(t, []agent.Approach{agent.ApproachUnsafeFile}, s.SupportedApproaches(agent.SurfaceSkills))
	assert.Empty(t, s.SupportedApproaches(agent.SurfaceMCP), "MCP folds into config.toml — no distinct surface")
}

// DefaultApproach is Hook for context (codex's only approach), the native file for
// settings/commands, and absent for the folded MCP kind.
func TestSurfaces_DefaultApproach(t *testing.T) {
	s := NewSurfaces(sampleInputs(), "", "", afero.NewMemMapFs())

	a, ok := s.DefaultApproach(agent.SurfaceContext)
	require.True(t, ok)
	assert.Equal(t, agent.ApproachHook, a)

	a, ok = s.DefaultApproach(agent.SurfaceSettings)
	require.True(t, ok)
	assert.Equal(t, agent.ApproachUnsafeFile, a)

	_, ok = s.DefaultApproach(agent.SurfaceMCP)
	assert.False(t, ok, "MCP is folded/absent for codex")

	a, ok = s.DefaultApproach(agent.SurfaceSkills)
	require.True(t, ok)
	assert.Equal(t, agent.ApproachUnsafeFile, a)
}

// SurfaceFor(context, Hook) resolves to the composed agent.ComposedDelivery, which performs BOTH the
// real cache-file write (codex's ONLY DECLARED context approach performs real
// work, unlike claude's Hook no-op) AND the native AGENTS.md write — this is
// the fix for both the materialize path AND the live run/launch path
// (`ctxloom run` resolves surfaces through this SAME
// SurfaceFor(SurfaceContext, Hook) call, so codex's context now genuinely
// reaches the model on both paths).
func TestSurfaceFor_ContextHookWritesCacheFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	s := NewSurfaces(sampleInputs(), "", "", fs)

	d, err := s.SurfaceFor(agent.SurfaceContext, agent.ApproachHook)
	require.NoError(t, err)
	dir := t.TempDir()
	handle, err := d.Deliver(dir)
	require.NoError(t, err)
	require.NotNil(t, handle, "codex's Hook approach performs the real cache-file write")

	contextFile(t, fs, dir) // the hash-file route landed
	data, err := afero.ReadFile(fs, filepath.Join(dir, "AGENTS.md"))
	require.NoError(t, err, "the native AGENTS.md route landed too — SAME Deliver call")
	assert.Contains(t, string(data), "the secret color is vermilion")

}

// TestSurfaceFor_ContextUnsafeFileIsTheNativeFileAlone is the other half, and
// the one that used to be a refusal: naming the native file must deliver
// AGENTS.md and NOTHING else.
//
// Selecting it is how a caller asks for the artifact codex reads on its own —
// the one that outlives ctxloom being uninstalled. Before this it was
// unsupported, so SupportedApproaches under-reported the engine and the flag
// refused a delivery codex performs by default anyway.
func TestSurfaceFor_ContextUnsafeFileIsTheNativeFileAlone(t *testing.T) {
	fs := afero.NewMemMapFs()
	s := NewSurfaces(sampleInputs(), "", "", fs)

	d, err := s.SurfaceFor(agent.SurfaceContext, agent.ApproachUnsafeFile)
	require.NoError(t, err, "codex reads AGENTS.md natively; naming it must be supported")
	dir := t.TempDir()
	_, err = d.Deliver(dir)
	require.NoError(t, err)

	data, rerr := afero.ReadFile(fs, filepath.Join(dir, "AGENTS.md"))
	require.NoError(t, rerr, "the native file is what this approach delivers")
	assert.Contains(t, string(data), "the secret color is vermilion")

	// ALONE is the claim. If the hash-file route rode along, this approach
	// would be a second spelling of Hook rather than a distinct choice, and a
	// caller asking for a portable artifact would also get a cache file that
	// means nothing without ctxloom.
	entries, _ := afero.ReadDir(fs, filepath.Join(dir, agent.SCMContextSubdir))
	assert.Empty(t, entries, "the hook route's cache file must NOT be written for the native-file approach")
}

// A past review claimed the exported Context/AgentsMD fields let a caller
// bypass the routes composition and drop the AGENTS.md write. No production
// caller touches either field (the launch and materialize paths both resolve
// through SurfaceFor / Select().Build(), pinned above), and every sibling
// backend exports the same shape. What actually protects the AGENTS.md write
// is that the exported fields ARE the composed route's parts — one instance
// each, as SurfaceSet.SurfaceFor's contract requires. That is what this
// pins: a future NewSurfaces that hands the field a different instance than
// the route delivers through is the divergence that review feared, and it
// fails here.
func TestSurfaces_ExportedContextFieldsAreTheComposedRouteParts(t *testing.T) {
	s := NewSurfaces(sampleInputs(), "", "", afero.NewMemMapFs())

	d, err := s.SurfaceFor(agent.SurfaceContext, agent.ApproachHook)
	require.NoError(t, err)
	composed, ok := d.(agent.ComposedDelivery)
	require.True(t, ok, "context resolves to the composed route")
	require.Len(t, composed.Parts, 2, "both context routes ride the composition")
	assert.Same(t, s.Context, composed.Parts[0], "the exported hook-route field is the delivered instance")
	assert.Same(t, s.AgentsMD, composed.Parts[1], "the exported AGENTS.md field is the delivered instance")
}

// SharedRealization is absent for every kind: codex has no out-of-cwd redirect,
// so a SHARED-cwd delivery always falls back to the loud well-known write.
func TestSurfaces_SharedRealization_Absent(t *testing.T) {
	s := NewSurfaces(sampleInputs(), "", "", afero.NewMemMapFs())
	for _, kind := range []agent.SurfaceKind{agent.SurfaceContext, agent.SurfaceSettings, agent.SurfaceCommands, agent.SurfaceSkills} {
		_, ok := s.SharedRealization(kind)
		assert.False(t, ok, "%s: codex has no out-of-cwd realization", kind)
	}
}
