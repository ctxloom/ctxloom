package codex

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// TestWriteSettings_SessionEndIsAnnounced pins a real gap: codex has no
// session-end event, so a configured unified SessionEnd hook is written nowhere
// — and used to be warned nowhere either. Every sibling engine routes the kind,
// so a user who configured one has no way to learn it is inert on codex. The
// drop stays (there is no route to invent), but it now says so.
func TestWriteSettings_SessionEndIsAnnounced(t *testing.T) {
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	fs := afero.NewMemMapFs()
	w := &CodexHookWriter{FS: fs}
	hooks := &wire.HooksConfig{Unified: wire.UnifiedHooks{
		SessionStart: []wire.Hook{{Command: "ctxloom hook inject-context"}},
		SessionEnd:   []wire.Hook{{Command: "ctxloom hook wrap-up"}},
	}}

	require.NoError(t, w.writeSettingsIn(hooks, nil, nil, "/proj", ""))

	cfg := readConfig(t, fs, codexConfigPath("/proj"))
	hookTbl := asMap(cfg["hooks"])
	require.NotNil(t, hookTbl)
	assert.NotEmpty(t, asSlice(hookTbl["SessionStart"]), "SessionStart still routes")
	assert.NotContains(t, hookTbl, "SessionEnd", "codex has no such event to write it under")

	out := buf.String()
	assert.Contains(t, out, "session_end", "the warning must name the dropped hook kind")
	assert.Contains(t, out, "codex", "the warning must name the engine dropping it")
}

// A codex config with no SessionEnd hook must not warn — the user did not ask
// for the kind, so there is nothing inert to report.
func TestWriteSettings_NoSessionEndNoWarning(t *testing.T) {
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	fs := afero.NewMemMapFs()
	w := &CodexHookWriter{FS: fs}
	hooks := &wire.HooksConfig{Unified: wire.UnifiedHooks{
		SessionStart: []wire.Hook{{Command: "ctxloom hook inject-context"}},
	}}
	require.NoError(t, w.writeSettingsIn(hooks, nil, nil, "/proj", ""))

	assert.NotContains(t, strings.ToLower(buf.String()), "session_end")
}
