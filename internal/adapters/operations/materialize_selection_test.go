package operations

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestMaterialize_AContextDestinationIsWhereTheContextGoes: context=file:DEST
// writes the context into DEST inside the target, not the engine's own file,
// whatever other kinds are selected beside it.
func TestMaterialize_AContextDestinationIsWhereTheContextGoes(t *testing.T) {
	cfg := materializeCfg(t, "DEST-MARK", "")
	target := t.TempDir()
	res := materialize(t, cfg, MaterializeRequest{Target: target, Engines: []string{"mock"},
		Surfaces: []SurfaceSpec{{Kind: present.Context, Mechanism: "file", Dest: "docs/AGENTS.md"}, {Kind: present.MCP}}})
	assert.Contains(t, fileIn(t, target, "docs/AGENTS.md"), "DEST-MARK")
	assert.Equal(t, filepath.Join(target, "docs", "AGENTS.md"), res.Engines[0].ContextFile)
	assert.NoFileExists(t, filepath.Join(target, mock.ContextFileName))
}

// TestMaterialize_SelectedKindsAreReportedInPlanOrderOnce: kinds selected
// out of order, one of them twice, are each reported once, in the plan's
// order — by a delivery and by a dry run alike. The mock writes nothing
// for this profile's MCP servers or skills, so both are left empty (released).
func TestMaterialize_SelectedKindsAreReportedInPlanOrderOnce(t *testing.T) {
	cfg := materializeCfg(t, "X", "")
	surfaces := []SurfaceSpec{{Kind: present.Skills}, {Kind: present.Skills}, {Kind: present.MCP}}
	for _, dry := range []bool{false, true} {
		res := materialize(t, cfg, MaterializeRequest{Target: t.TempDir(), Engines: []string{"mock"}, Surfaces: surfaces, DryRun: dry})
		require.Len(t, res.Engines, 1)
		assert.Empty(t, res.Engines[0].Wrote, "dry=%v", dry)
		assert.Equal(t, []string{"mcp", "skills"}, res.Engines[0].Released, "dry=%v", dry)
	}
}

// TestMaterialize_NamedProfilesAreTheOnesReported: profiles the request
// names are the run's, not the default agent's.
func TestMaterialize_NamedProfilesAreTheOnesReported(t *testing.T) {
	testsupport.Isolate(t)
	appDir, _ := regenTestApp(t)
	writeRegenBundle(t, appDir, "dev", "version: \"1.0\"\nfragments:\n  rules:\n    tags: [\"security\"]\n    content: \"NAMED-MARK\"\n")
	cfg := cfgWithDirProfiles(t, afero.NewOsFs(), appDir, map[string]config.Profile{
		"reviewer": {SelectTags: []string{"security"}},
		"auditor":  {SelectTags: []string{"security"}},
	}, config.Fixture{DefaultAgent: "default", Agents: map[string]agents.Agent{"default": {Profiles: []string{"reviewer"}}}})
	res := materialize(t, cfg, MaterializeRequest{Profiles: []string{"auditor"}, Target: t.TempDir(), Engines: []string{"mock"}})
	assert.Equal(t, []string{"auditor"}, res.Profiles)
}

// TestMaterialize_WithNoEngineAnywhere: with no engine named, none
// configured and no default in the registry, an implicit target delivers
// nothing (and says so), while an explicit one is refused for want of the
// default it would deliver for.
func TestMaterialize_WithNoEngineAnywhere(t *testing.T) {
	cfg := materializeCfg(t, "X", "")
	reg, err := engine.NewRegistry(mock.New())
	require.NoError(t, err)

	res, err := Materialize(context.Background(), reg, cfg, MaterializeRequest{Target: t.TempDir()})
	require.NoError(t, err)
	assert.Equal(t, MaterializeNothing, res.Status)
	assert.Empty(t, res.Engines)

	_, err = Materialize(context.Background(), reg, cfg, MaterializeRequest{Target: t.TempDir(), Explicit: true})
	require.ErrorContains(t, err, "exactly one names the default")
}

// TestMaterialize_NotCarriedNamesOnlySelectedKinds: a loss is reported
// only for a selected kind — a kind the engine has no approach for, and a
// hook the engine has no event for, alike.
func TestMaterialize_NotCarriedNamesOnlySelectedKinds(t *testing.T) {
	lost := func(res *MaterializeResult) []string {
		require.Len(t, res.Engines, 1)
		var out []string
		for _, l := range res.Engines[0].NotCarried {
			out = append(out, l.Surface)
		}
		return out
	}
	cfg := materializeCfg(t, "X", "")
	res := materialize(t, cfg, MaterializeRequest{Target: t.TempDir(), Engines: []string{string(mock.NameLaunch)},
		Surfaces: []SurfaceSpec{{Kind: present.Context}, {Kind: present.Hooks}}})
	assert.Equal(t, []string{"hooks"}, lost(res), "no approach for hooks; mcp and the rest were not selected")

	hooked, target := materializeHookFixture(t)
	lossy := MaterializeRequest{Profiles: []string{"reviewer"}, Target: target, Engines: []string{string(mock.NameLossy)}}
	lossy.Surfaces = []SurfaceSpec{{Kind: present.Context}, {Kind: present.Hooks}}
	assert.Equal(t, []string{"hooks"}, lost(materialize(t, hooked, lossy)), "the session_start hook has no event")
	lossy.Surfaces = []SurfaceSpec{{Kind: present.Context}}
	assert.Empty(t, lost(materialize(t, hooked, lossy)), "hooks were not selected: their loss is not this run's")
}

// TestMaterialize_ReleaseAndWriteFailuresSetTheStatus: a release that takes
// the kinds out reports them released; a write or a release the filesystem
// refuses fails the run, the error prefixed with the engine.
func TestMaterialize_ReleaseAndWriteFailuresSetTheStatus(t *testing.T) {
	cfg := materializeCfg(t, "STATUS-MARK", "")
	mem := afero.NewMemMapFs()
	rw, ro := safefs.NewMem(mem), safefs.NewMem(afero.NewReadOnlyFs(mem))
	target := t.TempDir()
	req := MaterializeRequest{Target: target, Engines: []string{"mock"}, Surfaces: []SurfaceSpec{{Kind: present.Context}}}

	req.Root = ro
	require.NoError(t, mem.MkdirAll(target, 0o755))
	failed := materialize(t, cfg, req)
	assert.Equal(t, MaterializeFailed, failed.Status, "the only engine's write failed")
	require.Len(t, failed.Errors, 1)
	assert.Regexp(t, "^mock: ", failed.Errors[0])

	req.Root = rw
	require.Equal(t, MaterializeApplied, materialize(t, cfg, req).Status)

	req.Release, req.Root = true, ro
	refused := materialize(t, cfg, req)
	assert.Equal(t, MaterializeFailed, refused.Status, "the only engine's release failed")
	require.Len(t, refused.Errors, 1)
	assert.Regexp(t, "^mock: ", refused.Errors[0])
	assert.Empty(t, refused.Engines[0].Released)

	req.Root = rw
	released := materialize(t, cfg, req)
	assert.Equal(t, MaterializeReleased, released.Status)
	assert.Equal(t, []string{"context"}, released.Engines[0].Released)
	assert.Empty(t, released.Errors)
	ok, err := afero.Exists(mem, filepath.Join(target, mock.ContextFileName))
	require.NoError(t, err)
	assert.False(t, ok, "the context file held only what materialize wrote")
}

// TestMaterialize_OneEngineFailingIsPartial: with two engines and a disk that
// refuses only the mock's context file, a write and a release alike are
// partial: the one error is the mock's, and claude-code's kind lands.
func TestMaterialize_OneEngineFailingIsPartial(t *testing.T) {
	cfg := materializeCfg(t, "PARTIAL-MARK", "")
	mem := afero.NewMemMapFs()
	mockFile := func(name string) bool { return filepath.Base(name) == mock.ContextFileName }
	faulty := safefs.NewMem(&failRemoveFs{Fs: &failWriteFs{Fs: mem, fail: mockFile}, fail: mockFile})
	target := t.TempDir()
	require.NoError(t, mem.MkdirAll(target, 0o755))
	req := MaterializeRequest{Target: target, Engines: []string{"mock", "claude-code"}, Surfaces: []SurfaceSpec{{Kind: present.Context}}, Root: faulty}
	partial := func(res *MaterializeResult) {
		t.Helper()
		assert.Equal(t, MaterializePartial, res.Status)
		require.Len(t, res.Errors, 1)
		assert.Regexp(t, "^mock: ", res.Errors[0])
		require.Len(t, res.Engines, 2)
		assert.Equal(t, "mock", res.Engines[0].Engine)
		assert.NotEmpty(t, res.Engines[0].Warnings)
		assert.Empty(t, res.Engines[1].Warnings)
	}

	written := materialize(t, cfg, req)
	partial(written)
	assert.Empty(t, written.Engines[0].Wrote, "the mock's write was refused")
	assert.Equal(t, []string{"context"}, written.Engines[1].Wrote)
	b, err := afero.ReadFile(mem, filepath.Join(target, claude.ContextFileName))
	require.NoError(t, err)
	assert.Contains(t, string(b), "PARTIAL-MARK")

	req.Root = safefs.NewMem(mem)
	require.Equal(t, MaterializeApplied, materialize(t, cfg, req).Status)
	req.Release, req.Root = true, faulty
	released := materialize(t, cfg, req)
	partial(released)
	assert.Empty(t, released.Engines[0].Released, "the mock's release was refused")
	assert.Equal(t, []string{"context"}, released.Engines[1].Released)
}
