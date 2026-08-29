package codex

import (
	"bytes"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// TestRemoveSettings_SaysNothingToRemove is the uninstall half of the declared
// absence. `ctxloom manage hooks uninstall` names codex among the backends it
// cleaned; under the declaration it removes nothing home-keyed, and an
// uninstall that removes nothing SILENTLY is indistinguishable from one that
// missed the file — which sends the user hunting, or deleting the wrong
// directory.
//
// PAYLOAD, NOT EXISTENCE: the message must carry the declared reason (so the
// user learns WHERE their settings actually are) and must name the legacy
// directory as theirs (D3 — the pre-relocation <workDir>/.codex gets no
// handling at all, and "ctxloom did not create it" is the clause that stops the
// obvious wrong fix).
func TestRemoveSettings_SaysNothingToRemove(t *testing.T) {
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	fs := afero.NewMemMapFs()
	w := &CodexHookWriter{FS: fs}

	require.NoError(t, w.RemoveSettings("/proj"),
		"nothing to remove is not a failure: a whole-project uninstall must not report partial for it")

	out := buf.String()
	assert.Contains(t, out, LaunchOnlySettingsReason, "the note must quote the declared reason")
	assert.Contains(t, out, "/proj", "the note must name the project it is talking about")
	assert.Contains(t, out, "ctxloom did not create it",
		"the note must say whose the legacy <workDir>/.codex is, or the user deletes the wrong thing")
}

// TestRemoveSettings_TouchesNothingOnDisk is the payload half of the same
// claim, and the one a message-only assertion cannot make: a pre-existing
// <workDir>/.codex/config.toml — the legacy directory D3 leaves alone — must
// still be there, byte-for-byte, after an uninstall.
func TestRemoveSettings_TouchesNothingOnDisk(t *testing.T) {
	fs := afero.NewMemMapFs()
	w := &CodexHookWriter{FS: fs}
	legacy := w.settingsPathIn("/proj")
	const original = "model = \"o3\"\n[mcp_servers.ctxloom]\ncommand = \"ctxloom\"\n"
	require.NoError(t, afero.WriteFile(fs, legacy, []byte(original), 0o644))

	require.NoError(t, w.RemoveSettings("/proj"))

	data, err := afero.ReadFile(fs, legacy)
	require.NoError(t, err, "the legacy directory is not ctxloom's to delete")
	assert.Equal(t, original, string(data), "not one byte of it is ctxloom's to rewrite either")
}

// TestSettingsPath_IsTheDeclaredAbsence pins the empty string itself. It is
// load-bearing in a way an ordinary path is not: filepath.Join(projectDir, "")
// is projectDir, so a caller that treats this as a relative path ends up
// statting a DIRECTORY and reporting it unreadable — which is exactly what
// doctor's MCP-invocation check did until it was taught the difference.
func TestSettingsPath_IsTheDeclaredAbsence(t *testing.T) {
	assert.Empty(t, (&CodexHookWriter{}).SettingsPath("/proj"))
	assert.Empty(t, (&CodexHookWriter{}).SettingsPath(""))
}

// TestDeliveryHome_DistinguishesItsTwoRefusals is the mutation guard for the
// one thing an enum-shaped refusal can get wrong quietly: reporting a harpless
// caller as if it had hit the host home, or the reverse. The two are told
// apart by their MESSAGES, and only one of them mentions config_home as the
// fix for a run.
func TestDeliveryHome_DistinguishesItsTwoRefusals(t *testing.T) {
	_, why := deliveryHome("")
	require.Equal(t, homeLaunchOnly, why)

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()
	warnUndelivered("hooks and MCP servers", homeLaunchOnly)

	out := buf.String()
	assert.Contains(t, out, LaunchOnlySettingsReason,
		"the launch-only refusal quotes the declared reason")
	assert.Contains(t, out, "AGENTS.md",
		"it must also say what DID land, or a user reads it as 'codex got nothing'")
	assert.NotContains(t, out, "YOUR OWN codex home",
		"a harpless caller never touched the user's home; saying so sends them to the wrong fix")
}

// TestLaunchOnlyRefusals_AllQuoteOneReason is the anti-drift gate on the
// declaration itself. Four writers decline in four idioms — an empty path, two
// errors, and a warning — and the whole value of DECLARING an absence is that
// the user reads one sentence rather than four paraphrases of it.
func TestLaunchOnlyRefusals_AllQuoteOneReason(t *testing.T) {
	require.NotEmpty(t, LaunchOnlySettingsReason)

	writeErr := (&CodexHookWriter{}).WriteSettings(&wire.HooksConfig{}, nil, "/proj")
	require.Error(t, writeErr)
	assert.Contains(t, writeErr.Error(), LaunchOnlySettingsReason, "CodexHookWriter.WriteSettings")

	_, registrarErr := (MCPRegistrar{}).ConfigPath("/proj", false)
	require.Error(t, registrarErr)
	assert.Contains(t, registrarErr.Error(), LaunchOnlySettingsReason, "MCPRegistrar.ConfigPath")

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()
	warnUndelivered("slash-command prompts", homeLaunchOnly)
	(&CodexHookWriter{FS: afero.NewMemMapFs()}).RemoveSettings("/proj") //nolint:errcheck // nil by contract; the message is the subject
	out := buf.String()
	assert.Equal(t, 2, bytes.Count([]byte(out), []byte(LaunchOnlySettingsReason)),
		"both the delivery refusal and the uninstall note quote the ONE reason verbatim")
}

// TestMCPRegistrar_PresentIsFalseForProjectScope pins the degradation path:
// ConfigPath's project-scope refusal must read as "codex is not present for
// this scope" to `taskloom manage`'s register-wherever-present flow, not as a
// crash and not as a present engine with an empty path.
func TestMCPRegistrar_PresentIsFalseForProjectScope(t *testing.T) {
	assert.False(t, (MCPRegistrar{}).Present("/proj", false),
		"a scope with no config file is not a scope codex is present in")
}

// TestStatus_ReportsNothingRatherThanFailing keeps `ctxloom manage check`
// readable: one backend with nothing to report must not blot the cross-backend
// status line with an error.
func TestStatus_ReportsNothingRatherThanFailing(t *testing.T) {
	status, err := (&CodexHookWriter{FS: afero.NewMemMapFs()}).Status("/proj")
	require.NoError(t, err)
	assert.Equal(t, agent.SettingsStatus{}, status)
}
