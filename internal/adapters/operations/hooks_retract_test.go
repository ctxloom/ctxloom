package operations

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/agents"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// The invariant these tests pin: ctxloom's managed content lives in a
// project file IFF that file is the surface's delivery route for the engine.
// One predicate (installedThroughProjectFile) answers the question for every
// surface kind, and both sides consult it — `manage hooks install` writes the
// file or retracts what an earlier writer left in it, and `manage check`
// misses the file only where the install would have written it.

// launchOnlyRoute is a synthetic approach whose bytes are announced on argv:
// a static install has nowhere to put them, so it is not a project file.
type launchOnlyRoute struct{}

func (launchOnlyRoute) LaunchOnly()                                            {}
func (launchOnlyRoute) Present(present.Start) present.Presentation             { return present.Presentation{} }
func (launchOnlyRoute) Deliver(present.Start) (agent.Delivered, error)         { return nil, nil }
func (launchOnlyRoute) DeliverIsolated(present.Start) (agent.Delivered, error) { return nil, nil }

func TestInstalledThroughProjectFile_IsFalseWhereTheEngineDeclaresARider(t *testing.T) {
	claude := backends.Declared("claude-code")

	assert.False(t, installedThroughProjectFile(claude, agent.SurfaceContext),
		"claude declares hook-carried context: the install routes context through the settings-carried hook, so CLAUDE.md is not the route")
	for _, kind := range []agent.SurfaceKind{agent.SurfaceSettings, agent.SurfaceMCP, agent.SurfaceCommands, agent.SurfaceSkills} {
		assert.True(t, installedThroughProjectFile(claude, kind), "claude's %s surface lands under the project root", kind)
	}
}

func TestInstalledThroughProjectFile_IsTrueForAFileOnlyEngine(t *testing.T) {
	mock := backends.Declared("mock")
	for _, kind := range []agent.SurfaceKind{agent.SurfaceContext, agent.SurfaceSettings, agent.SurfaceMCP, agent.SurfaceCommands, agent.SurfaceSkills} {
		assert.True(t, installedThroughProjectFile(mock, kind), "mock's %s surface has one route and it is a project file", kind)
	}
}

func TestInstalledThroughProjectFile_IsFalseForAnUndeclaredKind(t *testing.T) {
	assert.False(t, installedThroughProjectFile(agent.Declaration{}, agent.SurfaceContext),
		"an engine that declares no route for a kind delivers nothing through a file for it")
}

func TestInstalledThroughProjectFile_IsFalseForALaunchOnlyDefault(t *testing.T) {
	decl := agent.Declaration{
		agent.SurfaceContext: agent.Presents("synthetic", agent.SurfaceContext, "argv", func(agent.SurfaceInputs, afero.Fs) agent.Approach {
			return launchOnlyRoute{}
		}),
	}
	assert.False(t, installedThroughProjectFile(decl, agent.SurfaceContext),
		"a launch-only route has no at-rest write, so no project file carries it")
}

// The first route on the seam that is neither a rider nor launch-only and
// still not a project file: claude's hew-record settings write lands beneath
// the ENGINE HOME. A marker-enumerating predicate would have classed it a
// project file; the predicate asks the presenter, so an engine whose default
// settings route is the record write installs nothing through a project
// file for that kind — and `manage check` is not sent looking for one.
func TestInstalledThroughProjectFile_IsFalseForAnEngineHomeDefault(t *testing.T) {
	record, ok := backends.Declared("claude-code")[agent.SurfaceSettings].Construct(claude.ApproachHewRecord, agent.SurfaceInputs{}, nil)
	require.True(t, ok, "claude declares settings=hew-record")
	decl := agent.Declaration{
		agent.SurfaceSettings: agent.Presents("synthetic", agent.SurfaceSettings, claude.ApproachHewRecord, func(agent.SurfaceInputs, afero.Fs) agent.Approach {
			return record
		}),
	}
	assert.False(t, installedThroughProjectFile(decl, agent.SurfaceSettings),
		"a record write beneath the engine home is not delivered through a project file")
}

func TestInstallRoute_PrefersTheRiderOverTheDefault(t *testing.T) {
	name, route, ok := installRoute(backends.Declared("claude-code"), agent.SurfaceContext)
	require.True(t, ok)
	assert.Equal(t, agent.ApproachHook, name)
	_, rider := route.(agent.Rider)
	assert.True(t, rider, "the chosen route must be the Rider itself, not merely named like one")

	name, _, ok = installRoute(backends.Declared("mock"), agent.SurfaceContext)
	require.True(t, ok)
	assert.Equal(t, agent.ApproachUnsafeFile, name, "with no rider declared the install takes the engine's default")
}

// --- retraction: hooks install strips what an earlier writer left behind ------

// claudeHooksInstall runs `manage hooks install` for claude-code into /project
// on fs, capturing the diagnostics it prints.
func claudeHooksInstall(t *testing.T, fs afero.Fs, regenerate bool) (*ApplyHooksResult, string) {
	t.Helper()
	loader := func() (*config.Config, error) {
		return cfgWithProfileHooks(t, fs, "/project/.ctxloom", wire.HooksConfig{Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{{Command: "echo test", Type: "command"}},
		}}, config.Fixture{}), nil
	}
	var diag bytes.Buffer
	restore := clidiag.SetSink(&diag)
	defer restore()
	result, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend:           "claude-code",
		RegenerateContext: regenerate,
		FS:                fs,
		Cfg:               loaded(t, loader),
		WorkDir:           "/project",
	})
	require.NoError(t, err)
	require.Equal(t, "applied", result.Status)
	return result, diag.String()
}

// materializeClaudeContext writes managed context into /project/CLAUDE.md on
// fs through claude's own native-file route — the write `profile materialize`
// performs — so the retraction is exercised against the real marker layout.
func materializeClaudeContext(t *testing.T, fs afero.Fs, managed string) {
	t.Helper()
	route, ok := backends.Declared("claude-code").Construct(agent.SurfaceContext, agent.ApproachUnsafeFile, agent.SurfaceInputs{Context: managed}, fs)
	require.True(t, ok)
	_, err := route.Deliver(present.ProjectOnHost("/project"))
	require.NoError(t, err)
}

func TestApplyHooks_ClaudeCode_RetractsTheManagedSectionAndKeepsAuthoredContent(t *testing.T) {
	fs := afero.NewMemMapFs()
	authored := "# Team conventions\nalways use tabs, never spaces\n"
	testsupport.WriteFileString(t, fs, "/project/CLAUDE.md", authored, 0o644)
	materializeClaudeContext(t, fs, "LEFT BEHIND BY MATERIALIZE")
	before, err := afero.ReadFile(fs, "/project/CLAUDE.md")
	require.NoError(t, err)
	require.Contains(t, string(before), agent.ManagedContextBegin, "the fixture must carry a managed section or this test retracts nothing")

	result, diag := claudeHooksInstall(t, fs, true)

	after, err := afero.ReadFile(fs, "/project/CLAUDE.md")
	require.NoError(t, err)
	assert.Equal(t, authored, string(after), "everything outside ctxloom's markers survives byte-for-byte; only the managed section goes")
	assert.NotContains(t, string(after), "LEFT BEHIND BY MATERIALIZE")

	require.Len(t, result.Retracted, 1, "a destructive edit is reported, never silent")
	assert.Contains(t, result.Retracted[0], "CLAUDE.md")
	assert.Contains(t, diag, result.Retracted[0], "the retraction is printed for callers that discard the result (the MCP server's startup apply)")
}

func TestApplyHooks_ClaudeCode_RetractionRemovesAWhollyManagedFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	materializeClaudeContext(t, fs, "LEFT BEHIND BY MATERIALIZE")

	result, _ := claudeHooksInstall(t, fs, true)

	exists, err := afero.Exists(fs, "/project/CLAUDE.md")
	require.NoError(t, err)
	assert.False(t, exists, "a file that held nothing but ctxloom's section is removed, never left as an empty husk")
	require.Len(t, result.Retracted, 1)
	assert.Contains(t, result.Retracted[0], "removed CLAUDE.md")
}

func TestApplyHooks_ClaudeCode_RetractsNothingWhenThereIsNoManagedSection(t *testing.T) {
	fs := afero.NewMemMapFs()
	authored := "# Team conventions\nalways use tabs, never spaces\n"
	testsupport.WriteFileString(t, fs, "/project/CLAUDE.md", authored, 0o644)

	result, diag := claudeHooksInstall(t, fs, true)

	after, err := afero.ReadFile(fs, "/project/CLAUDE.md")
	require.NoError(t, err)
	assert.Equal(t, authored, string(after))
	assert.Empty(t, result.Retracted, "nothing of ctxloom's was there, so nothing is reported retracted")
	assert.NotContains(t, diag, "retract")
}

// TestApplyHooks_ClaudeCode_NoRegenerationRetractsNothing is the data-loss
// guard: a round that has no fresh context — the caller asked not to
// regenerate, or regeneration failed — must not touch the native file at all.
// Retraction rides the same skip as delivery: a NIL payload delivers nothing
// AND retracts nothing, so a config that failed to parse can never be the
// reason a user's managed section disappears.
func TestApplyHooks_ClaudeCode_NoRegenerationRetractsNothing(t *testing.T) {
	fs := afero.NewMemMapFs()
	materializeClaudeContext(t, fs, "LEFT BEHIND BY MATERIALIZE")
	before, err := afero.ReadFile(fs, "/project/CLAUDE.md")
	require.NoError(t, err)

	result, _ := claudeHooksInstall(t, fs, false)

	after, err := afero.ReadFile(fs, "/project/CLAUDE.md")
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "no regeneration this round: the managed section is left exactly as it was")
	assert.Empty(t, result.Retracted)
}

func TestApplyHooks_ClaudeCode_RegenerationFailureRetractsNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workDir := t.TempDir()
	appDir := filepath.Join(workDir, ".ctxloom")
	bundlesDir := authoredV1(appDir)
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "dev.yaml"), []byte(`version: "1.0"
fragments:
  rules:
    tags: ["security"]
    content: "Always validate input"
`), 0o644))
	loader := func() (*config.Config, error) {
		return cfgWithDirProfiles(t, afero.NewOsFs(), appDir, map[string]config.Profile{
			"default": {SelectTags: []string{"security"}},
		}, config.Fixture{
			DefaultAgent: "default",
			Agents:       map[string]agents.Agent{"default": {Profiles: []string{"default"}}},
		}), nil
	}
	route, ok := backends.Declared("claude-code").Construct(agent.SurfaceContext, agent.ApproachUnsafeFile, agent.SurfaceInputs{Context: "LEFT BEHIND BY MATERIALIZE"}, afero.NewOsFs())
	require.True(t, ok)
	_, err := route.Deliver(present.ProjectOnHost(workDir))
	require.NoError(t, err)
	before, err := os.ReadFile(filepath.Join(workDir, "CLAUDE.md"))
	require.NoError(t, err)

	// Make regeneration genuinely fail: the context cache needs
	// .ctxloom/cache to be a directory it can create under; a plain file at
	// that path makes the real MkdirAll error, like a disk fault would.
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "cache"), []byte("not a directory"), 0o644))

	result, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend:           "claude-code",
		RegenerateContext: true,
		Cfg:               loaded(t, loader),
		WorkDir:           workDir,
	})
	require.NoError(t, err)
	require.Equal(t, "partial", result.Status, "a regeneration failure is never reported as a clean apply")

	after, err := os.ReadFile(filepath.Join(workDir, "CLAUDE.md"))
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "a failed regeneration has no verdict on the file, so it retracts nothing")
	assert.Empty(t, result.Retracted)
}

func TestApplyHooks_ClaudeCode_DryRunRetractsNothing(t *testing.T) {
	fs := afero.NewMemMapFs()
	materializeClaudeContext(t, fs, "LEFT BEHIND BY MATERIALIZE")
	before, err := afero.ReadFile(fs, "/project/CLAUDE.md")
	require.NoError(t, err)

	loader := func() (*config.Config, error) {
		return cfgWithProfileHooks(t, fs, "/project/.ctxloom", wire.HooksConfig{}, config.Fixture{}), nil
	}
	result, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend: "claude-code", RegenerateContext: true, DryRun: true,
		FS: fs, Cfg: loaded(t, loader), WorkDir: "/project",
	})
	require.NoError(t, err)

	after, err := afero.ReadFile(fs, "/project/CLAUDE.md")
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "a dry run writes nothing, and retraction is a write")
	assert.Empty(t, result.Retracted)
}

// TestApplyHooks_FileRouteEngine_IsNeverRetracted pins the other side of the
// predicate: an engine whose context route IS the file keeps its managed
// section written, not stripped, by the same install.
func TestApplyHooks_FileRouteEngine_IsNeverRetracted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workDir := t.TempDir()
	appDir := filepath.Join(workDir, ".ctxloom")
	bundlesDir := authoredV1(appDir)
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "dev.yaml"), []byte(`version: "1.0"
fragments:
  rules:
    tags: ["security"]
    content: "Always validate input"
`), 0o644))
	loader := func() (*config.Config, error) {
		return cfgWithDirProfiles(t, afero.NewOsFs(), appDir, map[string]config.Profile{
			"default": {SelectTags: []string{"security"}},
		}, config.Fixture{
			DefaultAgent: "default",
			Agents:       map[string]agents.Agent{"default": {Profiles: []string{"default"}}},
		}), nil
	}
	result, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend: "mock", RegenerateContext: true, Cfg: loaded(t, loader), WorkDir: workDir,
	})
	require.NoError(t, err)
	require.Equal(t, "applied", result.Status)

	data, err := os.ReadFile(filepath.Join(workDir, "MOCK_CONTEXT.md"))
	require.NoError(t, err)
	assert.True(t, strings.Contains(string(data), "Always validate input"), "the file IS the route, so the install writes it")
	assert.Empty(t, result.Retracted)
}
