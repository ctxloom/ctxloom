//go:build parked_engines

package opencode

import (
	"encoding/json"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestOpencode_InteractivePostureMatrix pins BOTH halves of opencode's interactive
// posture: the opencode.json the TUI resolves (permission/model/instructions) AND
// the argv the TUI receives. Unlike codex, opencode's postures are almost entirely
// CONFIG-driven — the TUI honors the same `permission` block the acp/run paths do —
// so each posture asserts the emitted config block (right thing present, forbidden
// absent) plus the one argv lever, --auto for bypass.
func TestOpencode_InteractivePostureMatrix(t *testing.T) {
	const model = "openrouter/openai/gpt-oss-20b:free"
	const ctx = "assembled context"
	mcp := []agent.ChatMCPServer{{Name: "ctxloom", Command: "ctxloom", Args: []string{"mcp"}}}

	tests := []struct {
		name string
		req  agent.ExecuteRequest
		// config expectations
		wantPermission map[string]string // exact permission block, nil = no permission key
		wantInstr      bool              // instructions key present (context delivered)
		wantMCP        bool              // mcp key present
		// argv expectations
		argvWants    []string
		argvNotWants []string
	}{
		{
			name:           "default: no permission block, no --auto, context+mcp delivered",
			req:            agent.ExecuteRequest{Mode: agent.ModeInteractive, Permissions: agent.PermissionDefault},
			wantPermission: nil,
			wantInstr:      true,
			wantMCP:        true,
			argvNotWants:   []string{"--auto", "exec", "chat"},
		},
		{
			name:           "acceptEdits: opencode has no edit-only tier, follows default",
			req:            agent.ExecuteRequest{Mode: agent.ModeInteractive, Permissions: agent.PermissionAcceptEdits},
			wantPermission: nil,
			wantInstr:      true,
			wantMCP:        true,
			argvNotWants:   []string{"--auto"},
		},
		{
			name:           "plan: genuine deny-edit/deny-bash block, never --auto",
			req:            agent.ExecuteRequest{Mode: agent.ModeInteractive, Permissions: agent.PermissionPlan},
			wantPermission: map[string]string{"edit": "deny", "bash": "deny"},
			wantInstr:      true,
			wantMCP:        true,
			argvNotWants:   []string{"--auto"},
		},
		{
			name:           "bypass: --auto argv, no deny block in config",
			req:            agent.ExecuteRequest{Mode: agent.ModeInteractive, Permissions: agent.PermissionBypass},
			wantPermission: nil,
			wantInstr:      true,
			wantMCP:        true,
			argvWants:      []string{"--auto"},
		},
		{
			name:           "SkipSetup (distill/compaction): read-only, no mcp/instructions, no --auto",
			req:            agent.ExecuteRequest{Mode: agent.ModeInteractive, SkipSetup: true},
			wantPermission: map[string]string{"edit": "deny", "bash": "deny"},
			wantInstr:      false,
			wantMCP:        false,
			argvNotWants:   []string{"--auto"},
		},
		{
			name:           "SkipSetup outranks bypass: read-only, never --auto",
			req:            agent.ExecuteRequest{Mode: agent.ModeInteractive, SkipSetup: true, Permissions: agent.PermissionBypass},
			wantPermission: map[string]string{"edit": "deny", "bash": "deny"},
			wantInstr:      false,
			wantMCP:        false,
			argvNotWants:   []string{"--auto"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// --- config half: write the overlay to a MemMapFs and read it back ---
			fs := afero.NewMemMapFs()
			require.NoError(t, fs.MkdirAll("/work", 0o755))
			mc := interactiveManaged(&tc.req, model, ctx, mcp)
			require.NoError(t, writeOpencodeConfig(fs, "/work", mc))

			cfg := readJSON(t, fs, "/work/opencode.json")
			assert.Equal(t, model, cfg["model"], "model always rides opencode.json")

			if tc.wantPermission == nil {
				assert.NotContains(t, cfg, "permission", "no read-only posture → no permission key")
			} else {
				raw, ok := cfg["permission"].(map[string]any)
				require.True(t, ok, "permission block present")
				for k, v := range tc.wantPermission {
					assert.Equal(t, v, raw[k], "permission[%s]", k)
				}
			}

			_, hasInstr := cfg["instructions"]
			assert.Equal(t, tc.wantInstr, hasInstr, "instructions (context) presence")
			_, hasMCP := cfg["mcp"]
			assert.Equal(t, tc.wantMCP, hasMCP, "mcp presence")

			// --- argv half ---
			b := NewOpencode()
			args := b.buildInteractiveArgs(&tc.req)
			assert.NotContains(t, args, "exec", "opencode TUI is the default subcommand — no token")
			for _, w := range tc.argvWants {
				assert.Contains(t, args, w, "argv %v", args)
			}
			for _, w := range tc.argvNotWants {
				assert.NotContains(t, args, w, "argv %v", args)
			}
		})
	}
}

// TestOpencode_InteractiveArgs_PromptSeedsFirstMessage: a prompt is passed via
// --prompt so the TUI opens with it, and the run still stays interactive (no
// oneshot subcommand token).
func TestOpencode_InteractiveArgs_PromptSeedsFirstMessage(t *testing.T) {
	b := NewOpencode()
	req := agent.ExecuteRequest{Mode: agent.ModeInteractive, Prompt: &agent.Fragment{Content: "hello there"}}
	args := b.buildInteractiveArgs(&req)
	assert.Subset(t, args, []string{"--prompt", "hello there"})
}

func TestOpencode_InteractiveArgs_NoPromptNoFlag(t *testing.T) {
	b := NewOpencode()
	args := b.buildInteractiveArgs(&agent.ExecuteRequest{Mode: agent.ModeInteractive})
	assert.NotContains(t, args, "--prompt")
}

// TestOpencode_InteractiveArgs_PreservesBaseArgs: configured base args survive and
// lead the argv.
func TestOpencode_InteractiveArgs_PreservesBaseArgs(t *testing.T) {
	b := NewOpencode()
	b.Configure(&OpencodeConfig{Args: []string{"--log-level", "ERROR"}})
	args := b.buildInteractiveArgs(&agent.ExecuteRequest{Mode: agent.ModeInteractive, Permissions: agent.PermissionBypass})
	assert.Equal(t, []string{"--log-level", "ERROR"}, args[:2], "base args lead")
	assert.Contains(t, args, "--auto")
}

// TestOpencode_SupportedModes: interactive is now a supported mode.
func TestOpencode_SupportedModes(t *testing.T) {
	modes := NewOpencode().SupportedModes()
	assert.Contains(t, modes, agent.ModeInteractive)
	assert.Contains(t, modes, agent.ModeOneshot)
}

// sanity: the permission map we assert against matches the package's canonical block.
func TestOpencode_ReadOnlyPermissionShape(t *testing.T) {
	b, _ := json.Marshal(readOnlyPermission)
	assert.JSONEq(t, `{"edit":"deny","bash":"deny"}`, string(b))
}
