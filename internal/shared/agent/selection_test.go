package agent_test

// This file exercises the SurfaceSelection builder (Select/Build/ResolvedSelection
// — vital-tiger v2) against the REAL backend Surfaces (claude/codex/kiro),
// proving the per-provider dispatch tables (S2) integrate correctly with the
// generic builder BEFORE any caller is wired onto it (materialize/apply/remove/
// launch are migrated separately, plan S4). It is an EXTERNAL test package
// (agent_test, not agent) because internal test files cannot import a package
// that itself imports the package under test — claude/codex/kiro all import
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
	"github.com/ctxloom/ctxloom/internal/codex"
	"github.com/ctxloom/ctxloom/internal/kiro"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
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

// fixedPlacement is a trivial agent.Placement for building claude's Surfaces
// against a fixed out-of-cwd scratch dir.
type fixedPlacement struct{ dir string }

func (p fixedPlacement) Dir() string { return p.dir }

// Build validates a named approach against the backend's SupportedApproaches:
// SystemPrompt is claude-only — kiro and codex (native-file-only / hook-only
// context, respectively) both reject it.
func TestBuild_RejectsSystemPrompt_OnKiroAndCodex(t *testing.T) {
	kiroSet := kiro.NewSurfaces(agent.SurfaceInputs{}, nil)
	_, err := agent.Select(kiroSet).WithContext(agent.ContextWriteSystemPrompt).Build()
	assert.Error(t, err, "kiro's context is native-file-only; system-prompt is unsupported")

	codexSet := codex.NewSurfaces(agent.SurfaceInputs{}, "", "", nil)
	_, err = agent.Select(codexSet).WithContext(agent.ContextWriteSystemPrompt).WithSettings(agent.SettingsWriteUnsafeFile).Build()
	assert.Error(t, err, "codex's context is hook-only; system-prompt is unsupported")
}

// An unsupported (kind, approach) pair is rejected LOUDLY rather than
// downgraded to the backend's default — a caller who asked for one delivery and
// silently received another would have no way to tell.
//
// kiro is the example because it reads a native steering file and has no hook
// route at all. This test used to use CODEX and unsafe-file, on the grounds that
// codex had no native context file; codex reads a workspace-fixed AGENTS.md and
// now declares that approach, so the pair it asserted was unsupported is
// supported and the test was pinning a limitation rather than a contract.
func TestBuild_RejectsUnsupportedContextApproach(t *testing.T) {
	kiroSet := kiro.NewSurfaces(agent.SurfaceInputs{}, nil)
	_, err := agent.Select(kiroSet).WithContext(agent.ContextWriteHook).WithSettings(agent.SettingsWriteUnsafeFile).Build()
	assert.Error(t, err, "kiro's context is native-file-only; hook is unsupported and must be refused, not downgraded")
}

// The Hook approach rides the settings-carried inject hook: naming it without
// also selecting settings in the SAME Build() is rejected (there is no hook to
// carry the injection — an unread cache file, or nothing at all).
func TestBuild_RejectsContextHookWithoutSettings(t *testing.T) {
	codexSet := codex.NewSurfaces(agent.SurfaceInputs{}, "", "", nil)
	_, err := agent.Select(codexSet).WithContext(agent.ContextWriteHook).Build()
	assert.Error(t, err, "Hook without Settings selected in the same Build() must fail")

	_, err = agent.Select(codexSet).WithContext(agent.ContextWriteHook).WithSettings(agent.SettingsWriteUnsafeFile).Build()
	assert.NoError(t, err, "Hook WITH Settings selected builds cleanly")
}

// DeliverUnder has no argv sink for the out-of-cwd system-prompt flag, so a
// selection resolved at SystemPrompt is a per-surface failure at rest (Build
// itself succeeds — SystemPrompt IS a claude-supported approach; only the at-rest
// terminal rejects it).
func TestDeliverUnder_RejectsSystemPrompt(t *testing.T) {
	fs := afero.NewMemMapFs()
	claudeSet := claude.NewSurfaces(agent.SurfaceInputs{Context: "hello"}, fixedPlacement{dir: "/isolated"}, fs)

	r, err := agent.Select(claudeSet).WithContext(agent.ContextWriteSystemPrompt).Build()
	require.NoError(t, err, "SystemPrompt is a valid claude approach — Build succeeds")

	dir := "/target"
	_, _, errs := r.DeliverUnder(dir)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "system-prompt")
	exists, _ := afero.Exists(fs, filepath.Join(dir, "CLAUDE.md"))
	assert.False(t, exists, "the rejected surface must not fall back to a native write")
}

// claude is the ONLY backend with a SharedRealization: a SHARED-cwd delivery of
// its context surface ALWAYS converts to the out-of-cwd system-prompt scratch —
// never the native CLAUDE.md write, and never the Hook no-op (WithEverything
// picks UnsafeFile as claude's default, yet DeliverShared still converts it).
func TestDeliverShared_ClaudeContextConvertsToSystemPrompt_NeverHook(t *testing.T) {
	fs := afero.NewMemMapFs()
	isolated := "/isolated-scratch"
	sharedCwd := "/live/project"
	set := claude.NewSurfaces(agent.SurfaceInputs{Context: "project rules"}, fixedPlacement{dir: isolated}, fs)

	r, err := agent.Select(set).WithEverything().Build()
	require.NoError(t, err)

	stderr := captureStderr(t, func() {
		delivered, _, errs := r.DeliverShared(sharedCwd)
		require.Empty(t, errs)
		assert.Len(t, delivered, 5, "context, mcp, settings, commands, and skills (Unsafe) all deliver")
	})

	exists, _ := afero.Exists(fs, filepath.Join(sharedCwd, "CLAUDE.md"))
	assert.False(t, exists, "context must NEVER land as a native file in the shared cwd")
	assert.Equal(t, isolated, filepath.Dir(set.Context.Path()), "context converted to the out-of-cwd system-prompt scratch")
	assert.Contains(t, stderr, "commands", "commands has no realization and must warn")
	assert.Contains(t, stderr, "skills", "skills has no realization and must warn")
	assert.Equal(t, 2, strings.Count(stderr, "warning:"),
		"only commands and skills (no SharedRealization) warn — context/mcp/settings convert silently via SharedRealization")
}

// A backend with NO SharedRealization for any surface (codex, antigravity, kiro —
// only claude has one) falls back to the loud well-known write for EVERY
// surface: the exact warning format survives (the substrings existing assertions
// pin: "warning:", the surface name, "shared cwd"), and the write still proceeds.
func TestDeliverShared_NoRealization_WarnsThenWritesWellKnown(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/live"
	set := kiro.NewSurfaces(agent.SurfaceInputs{
		Commands: []agent.CommandExport{{Name: "review", Content: "do it", Enabled: true}},
	}, fs)

	r, err := agent.Select(set).WithCommands(agent.CommandsWriteUnsafeFile).Build()
	require.NoError(t, err)

	var delivered []agent.Delivered
	stderr := captureStderr(t, func() {
		var errs []error
		delivered, _, errs = r.DeliverShared(dir)
		require.Empty(t, errs)
	})
	require.Len(t, delivered, 1)
	assert.Contains(t, stderr, "warning:")
	assert.Contains(t, stderr, "commands")
	assert.Contains(t, stderr, "shared cwd")

	exists, _ := afero.Exists(fs, filepath.Join(dir, ".kiro", "skills", "review", "SKILL.md"))
	assert.True(t, exists, "the well-known write proceeded into the shared cwd despite the warning")
}
