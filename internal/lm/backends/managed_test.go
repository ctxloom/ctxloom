package backends

import (
	"bytes"
	"testing"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These cover the HOST side of the setup seam (config/profile/bundle resolution
// into the wire-typed ManagedConfig). The agent-side fold (MergeManaged) is
// covered in the per-agent capabilities tests.

// sessionStartCommands returns the SessionStart hook commands in order.
func sessionStartCommands(h wire.UnifiedHooks) []string {
	cmds := make([]string, 0, len(h.SessionStart))
	for _, hook := range h.SessionStart {
		cmds = append(cmds, hook.Command)
	}
	return cmds
}

// TestAssembleManagedHooks_IncludesProfileSessionStartHook locks in the
// writer-parity contract: a profile-shipped SessionStart hook must appear in the
// set AssembleManagedHooks produces (the operations.ApplyHooks path and the
// `ctxloom run` setup payload both build from it). Before this, Setup merged
// default-profile hooks and apply-hooks did not, so the next apply-hooks
// reconcile dropped the profile hook — the drop-on-clobber class that broke
// forward-bind.
func TestAssembleManagedHooks_IncludesProfileSessionStartHook(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		Hooks:        wire.HooksConfig{Plugins: make(map[string]wire.BackendHooks)},
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"p"}}},
		Profiles: config.ProfilesConfig{
			Definitions: map[string]config.Profile{
				"p": {Hooks: wire.HooksConfig{Unified: wire.UnifiedHooks{
					SessionStart: []wire.Hook{{Command: "profile-session-start", Type: "command"}},
				}}},
			},
		},
	})

	assembled := AssembleManagedHooks(cfg, "/tmp", "", nil)

	assert.Contains(t, sessionStartCommands(assembled.Wire().Unified), "profile-session-start",
		"profile-shipped SessionStart hook must be in the assembled set")
}

// TestAssembleManagedHooks_MatchesSetupSeam is the regression guard against the
// two writers diverging across the new host/agent seam: the SessionStart set the
// agent ends up with — host-assembled hooks (no context-injection) plus the
// context-injection hook the agent appends itself — must equal the set
// apply-hooks writes via AssembleManagedHooks(cfg, wd, hash). Divergence here is
// what lets WriteSettings' remove-then-add reconcile drop a managed hook.
func TestAssembleManagedHooks_MatchesSetupSeam(t *testing.T) {
	newCfg := func() *config.Config {
		return config.NewFixture(config.Fixture{
			Hooks: wire.HooksConfig{
				Unified: wire.UnifiedHooks{
					SessionStart: []wire.Hook{{Command: "config-session-start", Type: "command"}},
				},
				Plugins: make(map[string]wire.BackendHooks),
			},
			MCP:          wire.MCPConfig{Servers: make(map[string]wire.MCPServer), Plugins: make(map[string]map[string]wire.MCPServer)},
			DefaultAgent: "default",
			Agents:       map[string]agents.Agent{"default": {Profiles: []string{"p"}}},
			Profiles: config.ProfilesConfig{
				Definitions: map[string]config.Profile{
					"p": {Hooks: wire.HooksConfig{Unified: wire.UnifiedHooks{
						SessionStart: []wire.Hook{{Command: "profile-session-start", Type: "command"}},
					}}},
				},
			},
		})
	}

	const hash, wd = "hash123", "/tmp"

	// Setup payload path: host assembles WITHOUT context-injection, the agent
	// appends it from the plugin-side hash (exactly what MergeManaged does).
	setupCmds := sessionStartCommands(AssembleManagedHooks(newCfg(), wd, "", nil).Wire().Unified)
	for _, h := range agent.NewContextInjectionHooks(hash, wd) {
		setupCmds = append(setupCmds, h.Command)
	}

	// apply-hooks path: AssembleManagedHooks resolves the hash inline.
	applyCmds := sessionStartCommands(AssembleManagedHooks(newCfg(), wd, hash, nil).Wire().Unified)

	assert.Equal(t, applyCmds, setupCmds,
		"agent (host hooks + appended injection) and apply-hooks must produce an identical SessionStart set")
}

// TestAssembleManagedHooks_DoesNotMutateConfig guards the duplication fix:
// apply-hooks calls AssembleManagedHooks once per backend in a loop. If it
// aliased and appended to cfg.GetHooksConfig(), the second backend would accumulate
// duplicate bundle/inject hooks.
func TestAssembleManagedHooks_DoesNotMutateConfig(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		Hooks: wire.HooksConfig{
			Unified: wire.UnifiedHooks{SessionStart: []wire.Hook{{Command: "config-session-start"}}},
			Plugins: make(map[string]wire.BackendHooks),
		},
	})

	first := AssembleManagedHooks(cfg, "/tmp", "hash123", nil)
	second := AssembleManagedHooks(cfg, "/tmp", "hash123", nil)

	assert.Equal(t, len(first.Wire().Unified.SessionStart), len(second.Wire().Unified.SessionStart),
		"repeated calls must not accumulate hooks via shared config state")
	assert.Len(t, cfg.GetHooksConfig().Unified.SessionStart, 1,
		"AssembleManagedHooks must not mutate the caller's config.Hooks")
}

// TestAssembleManagedHooks_WithInvalidProfile must not panic on a default
// profile reference that has no definition.
func TestAssembleManagedHooks_WithInvalidProfile(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		Hooks:        wire.HooksConfig{Plugins: make(map[string]wire.BackendHooks)},
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"non-existent-profile"}}},
		Profiles: config.ProfilesConfig{
			Definitions: map[string]config.Profile{},
		},
	})

	assembled := AssembleManagedHooks(cfg, "/tmp", "hash123", nil)
	assert.NotEmpty(t, assembled.Wire().Unified.SessionStart, "context-injection hook should still be assembled")
}

// TestAssembleManagedMCP_MergesProfileServers folds config-level then
// default-profile MCP servers (the MCP half of the old MergeConfigHooks, now
// host-side).
func TestAssembleManagedMCP_MergesProfileServers(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		MCP: wire.MCPConfig{
			Servers: map[string]wire.MCPServer{"config-mcp": {Command: "config-mcp-cmd"}},
			Plugins: make(map[string]map[string]wire.MCPServer),
		},
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"p"}}},
		Profiles: config.ProfilesConfig{
			Definitions: map[string]config.Profile{
				"p": {MCP: wire.MCPConfig{
					Servers: map[string]wire.MCPServer{"profile-mcp": {Command: "profile-mcp-cmd"}},
				}},
			},
		},
	})

	mcp := AssembleManagedMCP(cfg, nil)
	assert.Contains(t, mcp.Servers, "config-mcp")
	assert.Contains(t, mcp.Servers, "profile-mcp")
}

// A BROKEN inline profile (circular parent inheritance) must be diagnosed as
// such — not silently mistaken for "not an inline profile" and retried
// against the directory loader, whose own (unrelated) not-found error then
// masks the real cause. Before the fix, all three managed.go resolvers
// (`if resolved, err := config.ResolveProfile(...); err == nil`) discarded
// ANY non-nil error the same way, inline or not.
func TestAssembleManagedMCP_CircularInlineProfileIsWarnedNotMasked(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		MCP:          wire.MCPConfig{Servers: make(map[string]wire.MCPServer), Plugins: make(map[string]map[string]wire.MCPServer)},
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"loopy"}}},
		Profiles: config.ProfilesConfig{
			Definitions: map[string]config.Profile{
				"loopy": {Parents: []string{"loopy"}},
			},
		},
	})

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	AssembleManagedMCP(cfg, nil)

	assert.Contains(t, buf.String(), "inheritance",
		"the real cause (inheritance) must reach the warning, not the directory loader's unrelated not-found error: got %q", buf.String())
}

func TestAssembleManagedHooks_CircularInlineProfileIsWarnedNotMasked(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		Hooks:        wire.HooksConfig{Plugins: make(map[string]wire.BackendHooks)},
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"loopy"}}},
		Profiles: config.ProfilesConfig{
			Definitions: map[string]config.Profile{
				"loopy": {Parents: []string{"loopy"}},
			},
		},
	})

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	AssembleManagedHooks(cfg, "/tmp", "", nil)

	assert.Contains(t, buf.String(), "inheritance",
		"the real cause (inheritance) must reach the warning: got %q", buf.String())
}

func TestAssembleManagedDenyTools_CircularInlineProfileIsWarnedNotMasked(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"loopy"}}},
		Profiles: config.ProfilesConfig{
			Definitions: map[string]config.Profile{
				"loopy": {Parents: []string{"loopy"}},
			},
		},
	})

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	AssembleManagedDenyTools(cfg, nil)

	assert.Contains(t, buf.String(), "inheritance",
		"the real cause (inheritance) must reach the warning: got %q", buf.String())
}

// The "default profile %q unresolved" warning must not say "default" when
// the caller passed an EXPLICIT --profile/-p selection — only a name pulled
// from the config's own defaults (scopedProfiles' fallback branch, no
// profileNames given) is a "default profile". AssembleManagedHooks already
// gets this right ("profile %q unresolved"); AssembleManagedMCP and
// AssembleManagedDenyTools did not.
func TestAssembleManagedMCP_ExplicitProfileWarningOmitsDefault(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		MCP: wire.MCPConfig{Servers: make(map[string]wire.MCPServer), Plugins: make(map[string]map[string]wire.MCPServer)},
	})

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	AssembleManagedMCP(cfg, []string{"explicitly-selected-and-missing"})

	assert.NotContains(t, buf.String(), "default profile",
		"an explicitly-selected profile must not be misreported as a default: got %q", buf.String())
}

func TestAssembleManagedDenyTools_ExplicitProfileWarningOmitsDefault(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{})

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	AssembleManagedDenyTools(cfg, []string{"explicitly-selected-and-missing"})

	assert.NotContains(t, buf.String(), "default profile",
		"an explicitly-selected profile must not be misreported as a default: got %q", buf.String())
}

// TestCommandExportsFor resolves each backend's per-prompt enablement + metadata
// from the same bundle content, and returns nil for an unknown backend.
func TestCommandExportsFor(t *testing.T) {
	enabled := true
	c := &bundles.LoadedContent{Name: "x", Content: "body"}
	c.LLM.ClaudeCode.Enabled = &enabled
	c.LLM.ClaudeCode.Description = "claude desc"
	c.LLM.ClaudeCode.ArgumentHint = "hint"
	c.LLM.Antigravity.Enabled = &enabled
	c.LLM.Antigravity.Description = "antigravity desc"
	prompts := []*bundles.LoadedContent{c}

	claudeEx := CommandExportsFor("claude-code", prompts)
	require.Len(t, claudeEx, 1)
	assert.Equal(t, "claude desc", claudeEx[0].Description)
	assert.Equal(t, "hint", claudeEx[0].ArgumentHint)
	assert.True(t, claudeEx[0].Enabled)

	antigravityEx := CommandExportsFor("antigravity", prompts)
	require.Len(t, antigravityEx, 1)
	assert.Equal(t, "antigravity desc", antigravityEx[0].Description)

	assert.Nil(t, CommandExportsFor("unknown-backend", prompts))
}
