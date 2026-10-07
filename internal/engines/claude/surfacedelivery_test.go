package claude

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/shared/ledger"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// fakePlacement is defined in contextdelivery_test.go (same package): a local
// placement double whose Dir() returns a fixed temp dir.

// TestFileTemplateDelivery_DeliverCommands verifies the commands surface is written
// into the injected Placement identically to WriteCommandFiles, and that Cleanup
// reverts the manifest-tracked set while preserving user-authored commands.
func TestFileTemplateDelivery_DeliverCommands(t *testing.T) {
	commands := []agent.CommandExport{
		{Name: "review", Content: "Review {{file}}", Enabled: true, Description: "Code review"},
		{Name: "simple", Content: "Simple command", Enabled: true},
	}

	deliverDir := t.TempDir()
	d := newFileTemplateDelivery(fakePlacement{dir: deliverDir}, safefs.New())
	handle, err := d.DeliverCommands(commands)
	require.NoError(t, err)

	// Control: the existing writer targeted at a separate dir.
	controlDir := t.TempDir()
	require.NoError(t, WriteCommandFiles(controlDir, commands))

	for _, rel := range []string{"review.md", "simple.md", ledger.Name} {
		got, err := os.ReadFile(filepath.Join(deliverDir, ".claude", "commands", rel))
		require.NoError(t, err, "DeliverCommands must write %s", rel)
		want, err := os.ReadFile(filepath.Join(controlDir, ".claude", "commands", rel))
		require.NoError(t, err)
		assert.Equal(t, string(want), string(got), "DeliverCommands must match WriteCommandFiles for %s", rel)
	}

	// Seed a user-authored command that ctxloom does not track.
	userCmd := filepath.Join(deliverDir, ".claude", "commands", "user.md")
	require.NoError(t, os.WriteFile(userCmd, []byte("mine"), 0o644))

	// Cleanup removes the manifest-tracked set (and manifest), not the user file.
	require.NoError(t, handle.Cleanup())
	assert.NoFileExists(t, filepath.Join(deliverDir, ".claude", "commands", "review.md"))
	assert.NoFileExists(t, filepath.Join(deliverDir, ".claude", "commands", "simple.md"))
	assert.NoFileExists(t, filepath.Join(deliverDir, ".claude", "commands", ledger.Name))
	assert.FileExists(t, userCmd, "user-authored command must survive cleanup")
}

// writeRenderedHomeCommand pre-seeds homeDir/.claude/commands/<name>.md with
// exactly the bytes WriteCommandFiles would render for cmd, so a dedup check
// against it is a true byte-identical comparison rather than a coincidence.
func writeRenderedHomeCommand(t *testing.T, homeDir string, cmd agent.CommandExport) {
	t.Helper()
	homeCommandsDir := filepath.Join(homeDir, ".claude", "commands")
	require.NoError(t, os.MkdirAll(homeCommandsDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(homeCommandsDir, cmd.Name+".md"),
		[]byte(TransformToClaudeCommand(cmd)), 0o644))
}

// TestFileTemplateDelivery_DeliverCommands_DedupsIdenticalHomeCopy verifies the
// wiring added to close the WithDedupHomeDir gap (never called in production
// before this fix, which left the option unwired): a project delivery skips a
// command whose rendered bytes are byte-identical to the same-named file already
// in the user-global ~/.claude/commands (here faked via $HOME), and the skip
// is not manifest-tracked, while a project-unique command still lands normally.
func TestFileTemplateDelivery_DeliverCommands_DedupsIdenticalHomeCopy(t *testing.T) {
	fakeHome := testsupport.Isolate(t)

	dup := agent.CommandExport{Name: "recover", Content: "Recovering context", Enabled: true}
	writeRenderedHomeCommand(t, fakeHome, dup)

	projectDir := t.TempDir()
	keep := agent.CommandExport{Name: "keep", Content: "Project-only command", Enabled: true}

	d := newFileTemplateDelivery(fakePlacement{dir: projectDir}, safefs.New())
	_, err := d.DeliverCommands([]agent.CommandExport{dup, keep})
	require.NoError(t, err)

	commandsDir := filepath.Join(projectDir, ".claude", "commands")
	assert.NoFileExists(t, filepath.Join(commandsDir, "recover.md"), "identical home copy must not be duplicated into the project scope")
	assert.FileExists(t, filepath.Join(commandsDir, "keep.md"), "a project-unique command is still written")

	manifest, err := os.ReadFile(filepath.Join(commandsDir, ledger.Name))
	require.NoError(t, err)
	assert.NotContains(t, string(manifest), "recover.md", "a deduped command must not be manifest-tracked")
	assert.Contains(t, string(manifest), "keep.md")
}

// TestFileTemplateDelivery_DeliverCommands_DivergentHomeCopyWritesNormally
// verifies a same-named home file that differs from the rendered project bytes
// (version skew) is never silently hidden: the project copy is still written.
func TestFileTemplateDelivery_DeliverCommands_DivergentHomeCopyWritesNormally(t *testing.T) {
	fakeHome := testsupport.Isolate(t)

	old := agent.CommandExport{Name: "recover", Content: "OLD BODY", Enabled: true}
	writeRenderedHomeCommand(t, fakeHome, old)

	projectDir := t.TempDir()
	updated := agent.CommandExport{Name: "recover", Content: "NEW BODY", Enabled: true}

	d := newFileTemplateDelivery(fakePlacement{dir: projectDir}, safefs.New())
	_, err := d.DeliverCommands([]agent.CommandExport{updated})
	require.NoError(t, err)

	commandsDir := filepath.Join(projectDir, ".claude", "commands")
	got, err := os.ReadFile(filepath.Join(commandsDir, "recover.md"))
	require.NoError(t, err)
	assert.Equal(t, TransformToClaudeCommand(updated), string(got), "a divergent home copy must not suppress the project write")

	manifest, err := os.ReadFile(filepath.Join(commandsDir, ledger.Name))
	require.NoError(t, err)
	assert.Contains(t, string(manifest), "recover.md")
}

// TestFileTemplateDelivery_DeliverCommands_DedupConvergence verifies the manifest
// reconcile: a file a PREVIOUS run delivered (and manifest-tracked) that this
// run's dedup now skips is not just left stale on disk — it is removed and
// dropped from the manifest, so the project scope actually converges to
// matching the global copy instead of leaving an orphaned duplicate.
func TestFileTemplateDelivery_DeliverCommands_DedupConvergence(t *testing.T) {
	fakeHome := testsupport.Isolate(t)
	// No home copy yet for this run.

	projectDir := t.TempDir()
	cmd := agent.CommandExport{Name: "recover", Content: "Recovering context", Enabled: true}
	d := newFileTemplateDelivery(fakePlacement{dir: projectDir}, safefs.New())

	// Run 1: delivered and manifest-tracked normally (no home copy to dedup against).
	_, err := d.DeliverCommands([]agent.CommandExport{cmd})
	require.NoError(t, err)
	commandsDir := filepath.Join(projectDir, ".claude", "commands")
	require.FileExists(t, filepath.Join(commandsDir, "recover.md"), "precondition: run 1 delivered the file")
	manifest, err := os.ReadFile(filepath.Join(commandsDir, ledger.Name))
	require.NoError(t, err)
	require.Contains(t, string(manifest), "recover.md", "precondition: run 1 manifest-tracked the file")

	// A byte-identical home copy now appears.
	writeRenderedHomeCommand(t, fakeHome, cmd)

	// Run 2: dedup now applies -> the stale project copy must be removed and
	// dropped from the manifest, not merely left un-rewritten.
	_, err = d.DeliverCommands([]agent.CommandExport{cmd})
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(commandsDir, "recover.md"), "run 2 must remove the now-deduped stale project copy")
	if manifest, err := os.ReadFile(filepath.Join(commandsDir, ledger.Name)); err == nil {
		assert.NotContains(t, string(manifest), "recover.md", "run 2 must drop the deduped file from the manifest")
	}
}

// TestFileTemplateDelivery_DeliverCommands_HomeScopeDeliveryDisablesDedup
// verifies a directory never dedups against itself: when the delivery target
// IS the resolved global commands directory (workDir == $HOME), the file is
// still delivered even though it is byte-identical to "itself".
func TestFileTemplateDelivery_DeliverCommands_HomeScopeDeliveryDisablesDedup(t *testing.T) {
	fakeHome := testsupport.Isolate(t)

	cmd := agent.CommandExport{Name: "recover", Content: "Recovering context", Enabled: true}
	// Pre-seed the exact file the delivery is about to (re)write, at the same
	// path DeliverCommands targets — this is the "identical to itself" case.
	writeRenderedHomeCommand(t, fakeHome, cmd)

	d := newFileTemplateDelivery(fakePlacement{dir: fakeHome}, safefs.New())
	_, err := d.DeliverCommands([]agent.CommandExport{cmd})
	require.NoError(t, err)

	commandsDir := filepath.Join(fakeHome, ".claude", "commands")
	assert.FileExists(t, filepath.Join(commandsDir, "recover.md"), "a home-scope delivery must still write, never self-dedup")
	manifest, err := os.ReadFile(filepath.Join(commandsDir, ledger.Name))
	require.NoError(t, err)
	assert.Contains(t, string(manifest), "recover.md", "a home-scope delivery must still manifest-track its write")
}

// TestFileTemplateDelivery_DeliverCommands_SelfContainedSkipsHomeDedup verifies
// the materialize-only opt-out (sour-feed): `profile materialize --target` must
// produce a PORTABLE, self-contained tree, so a command that happens to be
// deduped against the DELIVERING machine's ~/.claude/commands must still land
// in the target when selfContainedCommands is set — the launch environment is
// not this host and would otherwise silently lose it. The default (false)
// behavior — dedup against home, as live launch/apply/container want — must be
// unchanged.
func TestFileTemplateDelivery_DeliverCommands_SelfContainedSkipsHomeDedup(t *testing.T) {
	fakeHome := testsupport.Isolate(t)

	dup := agent.CommandExport{Name: "recover", Content: "Recovering context", Enabled: true}
	writeRenderedHomeCommand(t, fakeHome, dup)

	// Default (false): unchanged — still dedups against home.
	projectDir := t.TempDir()
	d := newFileTemplateDelivery(fakePlacement{dir: projectDir}, safefs.New())
	_, err := d.DeliverCommands([]agent.CommandExport{dup})
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(projectDir, ".claude", "commands", "recover.md"),
		"default (non-self-contained) delivery must still dedup against home")

	// selfContainedCommands = true: the portable target must keep the command even
	// though it shadows a command in the delivering machine's home.
	selfContainedDir := t.TempDir()
	sc := newFileTemplateDelivery(fakePlacement{dir: selfContainedDir}, safefs.New())
	sc.selfContainedCommands = true
	_, err = sc.DeliverCommands([]agent.CommandExport{dup})
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(selfContainedDir, ".claude", "commands", "recover.md"),
		"selfContainedCommands delivery must NOT dedup against the delivering machine's home — the portable target must keep every command")
}
