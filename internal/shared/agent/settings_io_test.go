package agent

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These cover the shared settings-writer helpers the per-agent writer modules
// (claude/antigravity/codex) call directly. They moved here from the host backends
// package along with the helpers themselves.

// TestResolveMCPCommand pins the fix at its narrowest seam: an empty
// override (every cell but an isolated container) returns EXACTLY
// CtxloomCommand()'s value — the host self-exec-absolute invariant is
// byte-for-byte unchanged — while a non-empty override (populated ONLY on the
// container axis, see isolation.Container.MCPCommandOverride) wins outright.
func TestResolveMCPCommand(t *testing.T) {
	assert.Equal(t, CtxloomCommand(), ResolveMCPCommand(""), "empty override must not perturb the host self-exec-absolute default")
	assert.Equal(t, "/usr/local/bin/ctxloom", ResolveMCPCommand("/usr/local/bin/ctxloom"), "a non-empty override wins outright")
}

func TestComputeHookHash(t *testing.T) {
	h1 := wire.Hook{Command: "./test.sh", Matcher: "Bash"}
	h2 := wire.Hook{Command: "./test.sh", Matcher: "Bash"}
	h3 := wire.Hook{Command: "./other.sh", Matcher: "Bash"}

	assert.Equal(t, ComputeHookHash(h1), ComputeHookHash(h2), "identical hooks → identical hash")
	assert.NotEqual(t, ComputeHookHash(h1), ComputeHookHash(h3), "different hooks → different hash")
	assert.Len(t, ComputeHookHash(h1), 16, "hash is 16 hex chars")
}

func TestAtomicWriteFile(t *testing.T) {
	t.Run("writes new file", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		path := "/test/file.json"
		data := []byte(`{"key": "value"}`)

		require.NoError(t, AtomicWriteFile(fs, path, data, "test file"))
		contents, err := afero.ReadFile(fs, path)
		require.NoError(t, err)
		assert.Equal(t, data, contents)
	})

	t.Run("creates backup of existing file", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		path := "/test/file.json"
		original := []byte(`{"original": true}`)
		updated := []byte(`{"updated": true}`)
		require.NoError(t, afero.WriteFile(fs, path, original, 0644))

		require.NoError(t, AtomicWriteFile(fs, path, updated, "test file"))

		// The backup sibling is GONE by design: every writer reaching this
		// function now knows what it owns (ledger or in-file markers) and no
		// longer rewrites a file wholesale, so there is nothing to recover
		// from. Asserting its ABSENCE keeps a future writer from quietly
		// reintroducing the debris.
		bakExists, err := afero.Exists(fs, path+".ctxloom.bak")
		require.NoError(t, err)
		assert.False(t, bakExists, "no .ctxloom.bak sibling may be left beside a written file")
		contents, err := afero.ReadFile(fs, path)
		require.NoError(t, err)
		assert.Equal(t, updated, contents)
	})

	t.Run("cleans up temp file on success", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		path := "/test/file.json"
		require.NoError(t, AtomicWriteFile(fs, path, []byte(`{}`), "test file"))
		exists, _ := afero.Exists(fs, path+".ctxloom.tmp")
		assert.False(t, exists, "temp file is cleaned up")
	})

	t.Run("new file defaults to owner-only mode", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		path := "/test/file.json"
		require.NoError(t, AtomicWriteFile(fs, path, []byte(`{}`), "test file"))
		info, err := fs.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), info.Mode().Perm(), "new settings files are not world-readable")
	})

	t.Run("preserves a tightened existing mode", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		path := "/test/file.json"
		require.NoError(t, afero.WriteFile(fs, path, []byte(`{"original": true}`), 0600))

		require.NoError(t, AtomicWriteFile(fs, path, []byte(`{"updated": true}`), "test file"))

		info, err := fs.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), info.Mode().Perm(), "rewrite must not widen a tightened mode")
		bakExists, err := afero.Exists(fs, path+".ctxloom.bak")
		require.NoError(t, err)
		assert.False(t, bakExists, "no .ctxloom.bak sibling may be left beside a written file")
	})

	// AtomicWriteFile had no len(data)==0 guard, so a caller that
	// accidentally assembled zero bytes (an upstream bug, not an intentional
	// removal — RemoveSettings/dropManaged callers go through fs.Remove, never
	// through this path with empty data) silently truncated a live settings
	// file to zero bytes and reported success. Refuse instead — the same
	// "refuse to overwrite, never self-heal" posture corrupt-config handling
	// already uses elsewhere.
	t.Run("refuses to truncate an existing file to zero bytes", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		path := "/test/file.json"
		original := []byte(`{"real": "settings"}`)
		require.NoError(t, afero.WriteFile(fs, path, original, 0600))

		err := AtomicWriteFile(fs, path, []byte{}, "test file")
		require.Error(t, err, "zero-length data must not silently win a write over a live file")

		contents, readErr := afero.ReadFile(fs, path)
		require.NoError(t, readErr)
		assert.Equal(t, original, contents, "the original file must survive the refused write untouched")
	})
}

func TestGetFS(t *testing.T) {
	memFs := afero.NewMemMapFs()
	assert.Equal(t, memFs, GetFS(memFs), "returns the provided fs")
	assert.NotNil(t, GetFS(nil), "falls back to the OS fs when nil")
}

// TestSettingsStatus_Wired pins SettingsStatus.Wired's definition (see the
// method's own doc). All nine of its call sites are in _test.go, but the most
// important of them is internal/lm/conformance, this repo's cross-agent
// contract check, and nothing else pins what Wired actually MEANS.
//
// The load-bearing part is the omission: SettingsExists is NOT one of the
// disjuncts. A settings file ctxloom merely found is not a file ctxloom wired,
// so `manage hooks remove` leaving a user's own settings.json behind must still
// report Wired() == false. That is exactly what the conformance suite's
// post-removal assertion depends on, and inlining the OR into nine test call
// sites would have left the omission unstated in all nine.
func TestSettingsStatus_Wired(t *testing.T) {
	cases := []struct {
		name   string
		status SettingsStatus
		want   bool
	}{
		{"nothing at all", SettingsStatus{}, false},
		{"a settings file we did not write is NOT wired",
			SettingsStatus{SettingsExists: true}, false},
		{"a managed hook is wired", SettingsStatus{HooksPresent: true}, true},
		{"a managed statusline is wired", SettingsStatus{StatusLine: true}, true},
		{"a managed MCP server is wired", SettingsStatus{MCPPresent: true}, true},
		{"all managed artifacts", SettingsStatus{
			SettingsExists: true, HooksPresent: true, StatusLine: true, MCPPresent: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.status.Wired())
		})
	}
}
