package kiro

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

func TestMCPRegistrar_Name(t *testing.T) {
	assert.Equal(t, "kiro", MCPRegistrar{}.Name())
}

func TestMCPRegistrar_ConfigPath(t *testing.T) {
	p, err := (MCPRegistrar{}).ConfigPath("/proj", false)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/proj", ".kiro", "settings", "mcp.json"), p)

	// Global scope honors KIRO_HOME.
	t.Setenv("KIRO_HOME", "/custom/kiro-home")
	g, err := (MCPRegistrar{}).ConfigPath("/proj", true)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/custom/kiro-home", "settings", "mcp.json"), g)
}

func TestMCPRegistrar_ConfigPath_GlobalDefaultsToHomeDotKiro(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KIRO_HOME", "")

	g, err := (MCPRegistrar{}).ConfigPath("/proj", true)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".kiro", "settings", "mcp.json"), g)
}

func TestMCPRegistrar_Present(t *testing.T) {
	dir := t.TempDir()
	r := MCPRegistrar{}
	assert.False(t, r.Present(dir, false), "no .kiro dir → not present")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".kiro"), 0o755))
	assert.True(t, r.Present(dir, false))

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KIRO_HOME", "")
	assert.False(t, r.Present(dir, true), "no home .kiro dir → not present globally")
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".kiro"), 0o755))
	assert.True(t, r.Present(dir, true))
}

// TestMCPRegistrar_PresentWarnsWhenPresenceIsUnknowable pins the answerable
// half of a real bug: agent.MCPRegistrar.Present is a boolean predicate, so it
// has exactly one way to say "no" and three reasons to reach it: kiro is
// genuinely absent, $HOME could not be resolved, and stat failed for some
// reason other than absence. Only the first is a fact about kiro. The other
// two make the registrar's caller (taskloom manage's auto-detection) skip
// kiro exactly as if the user had never used it — so they must at least be
// SAID, since the return type cannot carry them.
func TestMCPRegistrar_PresentWarnsWhenPresenceIsUnknowable(t *testing.T) {
	r := MCPRegistrar{}

	t.Run("stat fails for a reason other than absence", func(t *testing.T) {
		// A regular file standing where a directory must be: stat(<file>/.kiro)
		// is ENOTDIR, which is emphatically not "absent" — and needs no
		// permission games to provoke, so it holds for a root test runner too.
		notADir := filepath.Join(t.TempDir(), "notadir")
		require.NoError(t, os.WriteFile(notADir, []byte("x"), 0o644))

		var buf bytes.Buffer
		defer clidiag.SetSink(&buf)()

		assert.False(t, r.Present(notADir, false))
		assert.Contains(t, buf.String(), ".kiro",
			"an undeterminable path must not read as a deliberate absence in silence")
	})

	t.Run("global home cannot be resolved", func(t *testing.T) {
		t.Setenv("KIRO_HOME", "")
		t.Setenv("HOME", "")

		var buf bytes.Buffer
		defer clidiag.SetSink(&buf)()

		assert.False(t, r.Present("/proj", true))
		assert.NotEmpty(t, buf.String(),
			"an unresolvable global home must not read as 'kiro is not installed'")
	})

	t.Run("a genuinely absent kiro stays quiet", func(t *testing.T) {
		var buf bytes.Buffer
		defer clidiag.SetSink(&buf)()

		assert.False(t, r.Present(t.TempDir(), false))
		assert.Empty(t, buf.String(), "absence is the legitimate answer and must not be noisy")
	})
}

func TestMCPRegistrar_InstallUninstallRoundTrip(t *testing.T) {
	r := MCPRegistrar{}
	existing := `{"mcpServers": {"remote": {"serverUrl": "https://example.com/mcp"}}}`

	out, err := r.Install([]byte(existing), "taskloom", wire.MCPServer{Command: "taskloom", Args: []string{"mcp"}})
	require.NoError(t, err)
	ok, err := r.Installed(out, "taskloom")
	require.NoError(t, err)
	assert.True(t, ok)

	// Idempotent.
	again, err := r.Install(out, "taskloom", wire.MCPServer{Command: "taskloom", Args: []string{"mcp"}})
	require.NoError(t, err)
	assert.Equal(t, string(out), string(again))

	// Foreign remote server (serverUrl shape) survives install and uninstall —
	// the same "mcpServers" table shape kiro-cli itself reads from
	// .kiro/settings/mcp.json.
	removed, err := r.Uninstall(out, "taskloom")
	require.NoError(t, err)
	var doc map[string]map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(removed, &doc))
	assert.Contains(t, doc["mcpServers"], "remote")
	assert.NotContains(t, doc["mcpServers"], "taskloom")
}
