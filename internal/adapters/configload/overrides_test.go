package configload

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/confload"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestRead_BodyMapKeyCasePreserved pins the invariant that the read path
// never routes the document through a decoder that lowercases map keys —
// which would corrupt a case-sensitive pass-through map in an LLM label's
// Body (the mock's control map today; once a backend's credential block,
// which reached the launched process lower-cased so the engine never saw
// it). This exercises the FULL read (file + override resolution +
// remarshal/unmarshal into the value), not just the file-reading step.
func TestRead_BodyMapKeyCasePreserved(t *testing.T) {
	home := testsupport.Isolate(t)
	appDir := filepath.Join(home, "proj", config.AppDirName)
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	require.NoError(t, os.WriteFile(paths.ConfigPath(appDir), []byte(`version: 6
llm:
  configs:
    m:
      type: mock
      mock_control:
        CTXLOOM_MOCK_RESPONSE: canned
`), 0o644))

	// An unrelated env override elsewhere in the tree must not perturb the
	// case-sensitive map — the override resolution path runs alongside the
	// file layers, not instead of them. editor.command is ScopePreference
	// (settable from every layer, unlike default_agent).
	cfg, err := Load(WithAppDir(appDir), WithOverrides(confload.Overrides{
		Env: map[string]any{"EDITOR_COMMAND": "alpha"},
	}))
	require.NoError(t, err)

	entry, ok := cfg.ToFixture().LM.Configs["m"]
	require.True(t, ok)
	assert.Equal(t, "canned", entry.Body["mock_control"].(map[string]any)["CTXLOOM_MOCK_RESPONSE"],
		"CTXLOOM_MOCK_RESPONSE must survive the full read with its exact casing — no decoder may have lowercased this map")
	assert.Equal(t, "alpha", cfg.ToFixture().Editor.Command)
}

// TestNew_CapturesEnvAndConfigSetFlag_NotBusinessFlags is the composition
// root's contract end to end: New reads both an env override and a
// --config-set flag from the inputs it is handed, and the very next Read
// resolves them. The FlagSet ALSO carries an unrelated business flag sharing
// its name with a real config key ("workspace", as `run --workspace`
// declares) to prove New never scans it — only --config-set contributes.
func TestNew_CapturesEnvAndConfigSetFlag_NotBusinessFlags(t *testing.T) {
	testsupport.Isolate(t)
	appDir := t.TempDir()
	require.NoError(t, os.WriteFile(paths.ConfigPath(appDir), []byte("version: 6\nworkspace: none\n"), 0o644))

	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.StringArray(confload.ConfigSetFlagName, nil, "")
	fs.String("workspace", "", "") // an ordinary command flag, NOT a config override
	require.NoError(t, fs.Set(confload.ConfigSetFlagName, "default_agent=from-flag"))
	require.NoError(t, fs.Set("workspace", "worktree"))

	src, err := New(fs, []string{"CTXLOOM_CONFIG_DEFAULT_AGENT=from-env"}, WithAppDir(appDir))
	require.NoError(t, err)
	cfg, _, err := src.Read(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "from-flag", cfg.ToFixture().DefaultAgent, "--config-set must beat env")
	assert.Equal(t, "none", cfg.ToFixture().Workspace, "the --workspace BUSINESS flag must never be scanned as a config override")
}
