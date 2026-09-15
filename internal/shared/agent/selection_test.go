package agent_test

// This file exercises the SurfaceSelection builder (Select/Build/ResolvedSelection)
// against the REAL engine Declarations, proving each engine's declared
// approaches integrate correctly with the generic builder. It is an EXTERNAL test package
// (agent_test, not agent) because internal test files cannot import a package
// that itself imports the package under test — the engine packages all import
// internal/shared/agent, so this file must live outside it to avoid the Go
// toolchain's "import cycle not allowed in test" restriction.

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/agent/present"
)

// captureStderr redirects os.Stderr around fn and returns everything written to
// it, the way the internal cells_test.go helper does — DeliverShared's fallback
// WARN streams through clidiag.Warn → os.Stderr with no recorded finding.
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

// runRoots advises a run rooted at project with its out-of-cwd scratch at
// scratch and its relocated engine home at engineHome, on the host — the
// three roots a shared-cwd claude delivery reads. They are distinct so a test
// can tell which root a surface landed under.
func runRoots(project, scratch, engineHome string) present.Start {
	return present.New(present.OnHost(present.Paths{
		ProjectRoot: present.Root{Host: project},
		Scratch:     present.Root{Host: scratch},
		EngineHome:  present.Root{Host: engineHome},
	}))
}

// Build validates a named approach against the engine's Declaration:
// system-prompt is claude's own name, so an engine whose context surface
// declares native-file delivery alone rejects it — with NO edit to any shared
// list having been needed for claude to declare it in the first place.
func TestBuild_RejectsSystemPrompt_OnANativeFileOnlyBackend(t *testing.T) {
	mock := backends.Declared(config.BackendMock)
	_, err := agent.Select(mock).With(agent.SurfaceContext, claude.ApproachSystemPrompt).Build(agent.SurfaceInputs{}, nil)
	assert.Error(t, err, "a native-file-only context surface must reject system-prompt")
}

// An unsupported (kind, approach) pair is rejected LOUDLY rather than
// downgraded to the backend's default — a caller who asked for one delivery and
// silently received another would have no way to tell.
//
// mock is the example because its context surface declares unsafe-file ALONE —
// a native file with no hook route at all — so (context, hook) is a genuinely
// unsupported pair rather than a limitation that might later be declared.
func TestBuild_RejectsUnsupportedContextApproach(t *testing.T) {
	mock := backends.Declared(config.BackendMock)
	_, err := agent.Select(mock).With(agent.SurfaceContext, agent.ApproachHook).With(agent.SurfaceSettings, agent.ApproachUnsafeFile).Build(agent.SurfaceInputs{}, nil)
	assert.Error(t, err, "a native-file-only context surface must refuse hook, not downgrade to it")
}

// The Hook approach rides the settings-carried inject hook: naming it without
// also selecting settings in the SAME Build() is rejected (there is no hook to
// carry the injection — an unread cache file, or nothing at all).
func TestBuild_RejectsContextHookWithoutSettings(t *testing.T) {
	_, err := agent.Select(claude.Surfaces).With(agent.SurfaceContext, agent.ApproachHook).Build(agent.SurfaceInputs{}, nil)
	assert.Error(t, err, "Hook without Settings selected in the same Build() must fail")

	_, err = agent.Select(claude.Surfaces).With(agent.SurfaceContext, agent.ApproachHook).With(agent.SurfaceSettings, agent.ApproachUnsafeFile).Build(agent.SurfaceInputs{}, nil)
	assert.NoError(t, err, "Hook WITH Settings selected builds cleanly")
}

// DeliverUnder has no argv sink for the out-of-cwd system-prompt flag, so a
// selection resolved at SystemPrompt is a per-surface failure at rest (Build
// itself succeeds — SystemPrompt IS a claude-supported approach; only the at-rest
// terminal rejects it).
func TestDeliverUnder_RejectsSystemPrompt(t *testing.T) {
	fs := afero.NewMemMapFs()

	r, err := agent.Select(claude.Surfaces).With(agent.SurfaceContext, claude.ApproachSystemPrompt).Build(agent.SurfaceInputs{Context: "hello"}, fs)
	require.NoError(t, err, "SystemPrompt is a valid claude approach — Build succeeds")

	dir := "/target"
	_, _, errs := r.DeliverUnder(present.ProjectOnHost(dir))
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "system-prompt")
	exists, _ := afero.Exists(fs, filepath.Join(dir, "CLAUDE.md"))
	assert.False(t, exists, "the rejected surface must not fall back to a native write")
}

// RESOLUTION of U100-F05. A RAW builder call — Select(decl).WithEverything()
// .Build(), with no launch involved — resolves context to the engine's
// declared default (claude: ApproachUnsafeFile), and that approach VALUE has
// NO out-of-cwd form, so DeliverShared honors it: the native CLAUDE.md write
// lands in the shared cwd, loudly warned, exactly like commands/skills (which
// never had one). This is deliberately NOT the "no-preference shared launch"
// behaviour — that is a LAUNCH concern, not a builder concern: the
// default-derivation step that keeps a real claude launch on the scratch
// (preferring the pair that DOES realize when the caller named no preference)
// lives in launch_backend.go's deliverSet (SharedCell-only, unexported —
// SurfaceSet gained no new exported surface for this fix), and is proven
// end-to-end by the claude package's own Setup()-driven tests
// (TestSetup_ContextPayloadStillReachesTheLaunchFlag,
// TestClaudeCode_BuildArgs_NativeContextFlag, TestEngineCLI_*) rather than
// here: those exercise the real launch path this external package cannot
// reach (deliverSet is unexported, and this file is package agent_test).
func TestDeliverShared_ClaudeContextRawBuilderResolvesTableDefault_U100F05(t *testing.T) {
	fs := afero.NewMemMapFs()
	isolated := "/isolated-scratch"
	engineHome := "/engine-home"
	sharedCwd := "/live/project"
	r, err := agent.Select(claude.Surfaces).WithEverything().Build(agent.SurfaceInputs{Context: "project rules"}, fs)
	require.NoError(t, err)

	stderr := captureStderr(t, func() {
		delivered, _, errs := r.DeliverShared(runRoots(sharedCwd, isolated, engineHome))
		require.Empty(t, errs)
		assert.Len(t, delivered, 5, "context, mcp, settings, commands, and skills all deliver (context via the well-known write, not a realization)")
	})

	exists, _ := afero.Exists(fs, filepath.Join(sharedCwd, "CLAUDE.md"))
	assert.True(t, exists, "no realization for (context, unsafe-file) — the caller's table-default request is honored, not silently converted")
	assert.Contains(t, stderr, "context", "context has no realization at this pair and must warn")
	assert.Contains(t, stderr, "commands", "commands has no realization and must warn")
	assert.Contains(t, stderr, "skills", "skills has no realization and must warn")
	assert.Equal(t, 3, strings.Count(stderr, "warning:"),
		"context, commands, and skills all warn; mcp's default is already the private file and settings converts silently via its out-of-cwd form")
}

// An approach with NO out-of-cwd form (only claude's have one) falls back to
// the loud well-known write for that surface: the exact warning
// format survives (the substrings existing assertions pin: "warning:", the
// surface name, "shared cwd"), and the write still proceeds.
func TestDeliverShared_NoRealization_WarnsThenWritesWellKnown(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/live"
	r, err := agent.Select(backends.Declared(config.BackendMock)).With(agent.SurfaceSkills, agent.ApproachUnsafeFile).Build(agent.SurfaceInputs{
		Skills: []agent.SkillExport{{Name: "review", Enabled: true,
			Files: []agent.PackageFile{{RelPath: "SKILL.md", Content: []byte("do it")}}}},
	}, fs)
	require.NoError(t, err)

	var delivered []agent.Delivered
	stderr := captureStderr(t, func() {
		var errs []error
		delivered, _, errs = r.DeliverShared(present.ProjectOnHost(dir))
		require.Empty(t, errs)
	})
	require.Len(t, delivered, 1)
	assert.Contains(t, stderr, "warning:")
	assert.Contains(t, stderr, "skills")
	assert.Contains(t, stderr, "shared cwd")

	exists, _ := afero.DirExists(fs, filepath.Join(dir, ".mock", "skills"))
	assert.True(t, exists, "the well-known write proceeded into the shared cwd despite the warning")
}
