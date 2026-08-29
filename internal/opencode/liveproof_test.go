package opencode

import (
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestLiveProof_OverlayInPlaceAtLaunch drives launchInteractive with a fake pty
// launcher that snapshots the on-disk state AT THE MOMENT the TUI would launch,
// proving (payload, not exit code):
//  1. the managed opencode.json (model + context instructions + per-posture
//     permission) plus the .opencode/ctxloom-context.md are on disk WHEN the
//     process starts, and the launcher receives the TUI argv (no subcommand token,
//     --auto only for bypass), and
//  2. everything is reverted to the ORIGINAL project state after the interactive
//     session ends — the transient overlay's lifetime spans the whole run.
func TestLiveProof_OverlayInPlaceAtLaunch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		perm     agent.PermissionMode
		wantPerm string // "deny" edit expected, or "" for none
		wantAuto bool
	}{
		{"default", agent.PermissionDefault, "", false},
		{"plan", agent.PermissionPlan, "deny", false},
		{"bypass", agent.PermissionBypass, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cfgPath := filepath.Join(dir, "opencode.json")
			ctxPath := filepath.Join(dir, opencodeContextFile)
			osfs := afero.NewOsFs()

			// A pre-existing user opencode.json with a foreign key that must survive.
			require.NoError(t, afero.WriteFile(osfs, cfgPath, []byte(`{"theme":"tokyonight"}`), 0o644))

			b := NewOpencode()
			b.SetWorkDir(dir)
			b.model = "openrouter/openai/gpt-oss-20b:free"
			b.pendingContext = "SENTINEL-CONTEXT-abc123"

			var argvAtLaunch []string
			var cfgAtLaunch, ctxAtLaunch string
			var cfgExistedAtLaunch, ctxExistedAtLaunch bool
			b.SetLauncher(func(_ context.Context, spec agent.LaunchSpec, _ io.Reader, _, _ io.Writer, _ <-chan agent.WindowSize) (int32, error) {
				argvAtLaunch = spec.Args
				if data, err := afero.ReadFile(osfs, cfgPath); err == nil {
					cfgExistedAtLaunch, cfgAtLaunch = true, string(data)
				}
				if data, err := afero.ReadFile(osfs, ctxPath); err == nil {
					ctxExistedAtLaunch, ctxAtLaunch = true, string(data)
				}
				return 0, nil
			})

			req := &agent.ExecuteRequest{
				Mode:        agent.ModeInteractive,
				WorkDir:     dir,
				Permissions: tc.perm,
				Model:       "openrouter/openai/gpt-oss-20b:free",
			}
			res, err := b.launchInteractive(context.Background(), req, &agent.ModelInfo{}, io.Discard, io.Discard)
			require.NoError(t, err)
			require.Equal(t, int32(0), res.ExitCode)

			// --- at launch: overlay present with our payload ---
			require.True(t, cfgExistedAtLaunch, "opencode.json present at launch")
			assert.Contains(t, cfgAtLaunch, "openrouter/openai/gpt-oss-20b:free", "model delivered")
			assert.Contains(t, cfgAtLaunch, "tokyonight", "foreign key preserved")
			assert.Contains(t, cfgAtLaunch, opencodeContextFile, "instructions reference present")
			require.True(t, ctxExistedAtLaunch, "context file present at launch")
			assert.Contains(t, ctxAtLaunch, "SENTINEL-CONTEXT-abc123", "assembled context materialized")

			if tc.wantPerm == "deny" {
				assert.Contains(t, cfgAtLaunch, `"permission"`, "plan writes a permission block")
				assert.Contains(t, cfgAtLaunch, `"deny"`, "plan denies")
			} else {
				assert.NotContains(t, cfgAtLaunch, `"permission"`, "non-plan writes no permission block")
			}

			assert.NotContains(t, argvAtLaunch, "exec", "TUI is the default subcommand")
			if tc.wantAuto {
				assert.Contains(t, argvAtLaunch, "--auto", "bypass adds --auto")
			} else {
				assert.NotContains(t, argvAtLaunch, "--auto")
			}

			// --- after: reverted to the exact original project state ---
			after, err := afero.ReadFile(osfs, cfgPath)
			require.NoError(t, err)
			assert.JSONEq(t, `{"theme":"tokyonight"}`, string(after), "opencode.json restored to original")
			exists, _ := afero.Exists(osfs, ctxPath)
			assert.False(t, exists, "context file removed after the session")
		})
	}
}
