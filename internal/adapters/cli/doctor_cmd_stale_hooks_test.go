package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
)

// The verb set is the command tree's, read at run time: a subcommand renamed
// (or aliased) there is what the stale-hook check sees, with no list to edit.
func TestHookVerbsOf_ReadsTheTree(t *testing.T) {
	hook := &cobra.Command{Use: "hook"}
	hook.AddCommand(
		&cobra.Command{Use: "session-begin", Aliases: []string{"sb"}, Run: func(*cobra.Command, []string) {}},
		&cobra.Command{Use: "hud", Run: func(*cobra.Command, []string) {}},
	)

	assert.Equal(t, []string{"hud", "sb", "session-begin"}, hookVerbsOf(hook))
}

func TestHookVerbs_AreEveryRegisteredHookSubcommand(t *testing.T) {
	verbs := hookVerbsOf(hookCmd)
	require.NotEmpty(t, verbs)
	for _, c := range hookCmd.Commands() {
		assert.Contains(t, verbs, c.Name())
	}
}

const staleClaudeSettings = `{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [
          {"type": "command", "command": "ctxloom hook inject-context abc123"},
          {"type": "command", "command": "ctxloom hook session-start"}
        ]
      }
    ]
  }
}
`

// End to end through the real command tree and the real engine registry: a
// leftover `hook inject-context` entry in the project's engine settings is
// reported, `--fix` takes exactly it out, and the report is then clean.
func TestDoctorCmd_StaleHookEntryIsReportedThenFixed(t *testing.T) {
	root, _ := setupProject(t, "claude-code")
	settings := filepath.Join(root, ".claude", "settings.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(settings), 0o755))
	require.NoError(t, os.WriteFile(settings, []byte(staleClaudeSettings), 0o644))

	out, err := runDoctor(t, root, "--format", "json")
	require.NoError(t, err)
	check := doctorCheckNamed(t, out, "DOCTOR-CHECK-STALE-HOOKS-n5")
	assert.Equal(t, operations.DoctorWarn, check.Status)
	assert.Contains(t, check.Detail, settings)
	assert.Contains(t, check.Detail, "SessionStart")
	assert.Contains(t, check.Detail, "inject-context")
	assert.NotContains(t, check.Detail, "session-start", "a live verb is not reported")
	assert.Equal(t, "ctxloom doctor --fix", check.Remedy)

	out, err = runDoctor(t, root, "--format", "json", "--fix")
	require.NoError(t, err)
	assert.Equal(t, operations.DoctorOK, doctorCheckNamed(t, out, "DOCTOR-CHECK-STALE-HOOKS-n5").Status)

	got, err := os.ReadFile(settings)
	require.NoError(t, err)
	want := strings.Replace(staleClaudeSettings,
		"          {\"type\": \"command\", \"command\": \"ctxloom hook inject-context abc123\"},\n", "", 1)
	assert.Equal(t, want, string(got), "the fix takes out the stale entry and nothing else")
}

// The report judges against THIS binary's command tree: an entry for every
// subcommand registered under `hook` is live, so none is reported.
func TestDoctorCmd_EveryRegisteredHookSubcommandIsLive(t *testing.T) {
	root, _ := setupProject(t, "claude-code")
	var entries []string
	for _, c := range hookCmd.Commands() {
		entries = append(entries, fmt.Sprintf(`{"type": "command", "command": "ctxloom hook %s"}`, c.Name()))
	}
	settings := filepath.Join(root, ".claude", "settings.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(settings), 0o755))
	require.NoError(t, os.WriteFile(settings,
		[]byte(`{"hooks": {"SessionStart": [{"hooks": [`+strings.Join(entries, ",")+`]}]}}`), 0o644))

	out, err := runDoctor(t, root, "--format", "json")
	require.NoError(t, err)
	check := doctorCheckNamed(t, out, "DOCTOR-CHECK-STALE-HOOKS-n5")
	assert.Equal(t, operations.DoctorOK, check.Status, check.Detail)
}

// A tracked settings file on a machine with no claims record keeps the
// `hook next-step` callback an older ctxloom delivered (delivery withdraws
// only what its record names, or a superseded spelling of what it installs
// now). The verb is gone from this binary, so doctor reports the entry —
// in the exec form ctxloom writes — and `--fix` takes it out.
func TestDoctorCmd_RetiredNextStepCallbackIsStale(t *testing.T) {
	root, _ := setupProject(t, "claude-code")
	settings := filepath.Join(root, ".claude", "settings.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(settings), 0o755))
	require.NoError(t, os.WriteFile(settings, []byte(`{"hooks": {"Stop": [{"hooks": [
  {"type": "command", "command": "ctxloom", "args": ["hook", "next-step", "--engine", "claude-code"], "timeout": 5},
  {"type": "command", "command": "ctxloom", "args": ["hook", "mail-drain", "--engine", "claude-code"], "timeout": 5}
]}]}}`), 0o644))

	out, err := runDoctor(t, root, "--format", "json")
	require.NoError(t, err)
	check := doctorCheckNamed(t, out, "DOCTOR-CHECK-STALE-HOOKS-n5")
	assert.Equal(t, operations.DoctorWarn, check.Status)
	assert.Contains(t, check.Detail, "next-step")
	assert.NotContains(t, check.Detail, "mail-drain", "a live verb is not reported")

	_, err = runDoctor(t, root, "--format", "json", "--fix")
	require.NoError(t, err)
	got, err := os.ReadFile(settings)
	require.NoError(t, err)
	assert.NotContains(t, string(got), "next-step", "the fix takes the retired callback out")
	assert.Contains(t, string(got), "mail-drain", "and leaves the live one")
}
