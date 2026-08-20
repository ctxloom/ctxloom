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
	cfg := dirProfileCfg(t, []string{"p"}, map[string]string{
		"p": "hooks:\n  unified:\n    session_start:\n      - command: profile-session-start\n        type: command\n",
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
		// Two profiles rather than a config block plus a profile: the seam this
		// guards is between the two WRITERS, not between hook sources, so what
		// matters is that more than one hook reaches the set.
		return dirProfileCfg(t, []string{"base", "p"}, map[string]string{
			"base": "hooks:\n  unified:\n    session_start:\n      - command: base-session-start\n        type: command\n",
			"p":    "hooks:\n  unified:\n    session_start:\n      - command: profile-session-start\n        type: command\n",
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
// aliased and appended to the hooks its source handed back, the second backend
// would accumulate duplicate bundle/inject hooks.
//
// The source is a directory profile now that the config-level block is gone, so
// the aliasing risk sits in the resolved profile rather than in the config, and
// a THIRD call is what makes the assertion mean something: two equal lengths
// could both already be wrong.
func TestAssembleManagedHooks_DoesNotMutateConfig(t *testing.T) {
	cfg := dirProfileCfg(t, []string{"p"}, map[string]string{
		"p": "hooks:\n  unified:\n    session_start:\n      - command: profile-session-start\n        type: command\n",
	})

	first := AssembleManagedHooks(cfg, "/tmp", "hash123", nil)
	second := AssembleManagedHooks(cfg, "/tmp", "hash123", nil)
	third := AssembleManagedHooks(cfg, "/tmp", "hash123", nil)

	assert.Equal(t, len(first.Wire().Unified.SessionStart), len(second.Wire().Unified.SessionStart),
		"repeated calls must not accumulate hooks via shared state")
	assert.Equal(t, len(first.Wire().Unified.SessionStart), len(third.Wire().Unified.SessionStart),
		"and the count must be STABLE, not merely equal between two already-grown calls")
	assert.Contains(t, commandsOf(first.For("session_start")), "profile-session-start",
		"the profile hook is present, so the counts above are counting something")
}

// TestAssembleManagedHooks_WithInvalidProfile must not panic on a default
// profile reference that has no definition.
func TestAssembleManagedHooks_WithInvalidProfile(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"non-existent-profile"}}},
	})

	assembled := AssembleManagedHooks(cfg, "/tmp", "hash123", nil)
	assert.NotEmpty(t, assembled.Wire().Unified.SessionStart, "context-injection hook should still be assembled")
}

func TestAssembleManagedHooks_CircularProfileIsWarnedNotMasked(t *testing.T) {
	cfg := dirProfileCfg(t, []string{"loopy"}, map[string]string{
		"loopy": "parents:\n  - loopy\n",
	})

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	AssembleManagedHooks(cfg, "/tmp", "", nil)

	assert.Contains(t, buf.String(), "inheritance",
		"the real cause (inheritance) must reach the warning: got %q", buf.String())
}

func TestAssembleManagedDenyTools_CircularProfileIsWarnedNotMasked(t *testing.T) {
	cfg := dirProfileCfg(t, []string{"loopy"}, map[string]string{
		"loopy": "parents:\n  - loopy\n",
	})

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	AssembleManagedDenyTools(cfg, nil)

	assert.Contains(t, buf.String(), "inheritance",
		"the real cause (inheritance) must reach the warning: got %q", buf.String())
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
	c.LLM.Codex.Enabled = &enabled
	c.LLM.Codex.Description = "codex desc"
	prompts := []*bundles.LoadedContent{c}

	claudeEx := CommandExportsFor("claude-code", prompts)
	require.Len(t, claudeEx, 1)
	assert.Equal(t, "claude desc", claudeEx[0].Description)
	assert.Equal(t, "hint", claudeEx[0].ArgumentHint)
	assert.True(t, claudeEx[0].Enabled)

	codexEx := CommandExportsFor("codex", prompts)
	require.Len(t, codexEx, 1)
	assert.Equal(t, "codex desc", codexEx[0].Description)

	assert.Nil(t, CommandExportsFor("unknown-backend", prompts))
}
