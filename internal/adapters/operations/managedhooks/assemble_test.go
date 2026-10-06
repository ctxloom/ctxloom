package managedhooks

import (
	"bytes"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/stretchr/testify/assert"
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

// TestAssemble_IncludesProfileSessionStartHook locks in the
// writer-parity contract: a profile-shipped SessionStart hook must appear in the
// set Assemble produces (the operations.ApplyHooks path and the
// `ctxloom run` setup payload both build from it). Before this, Setup merged
// default-profile hooks and apply-hooks did not, so the next apply-hooks
// reconcile dropped the profile hook — the drop-on-clobber class that broke
// forward-bind.
func TestAssemble_IncludesProfileSessionStartHook(t *testing.T) {
	cfg := dirProfileCfg(t, []string{"p"}, map[string]string{
		"p": "hooks:\n  unified:\n    session_start:\n      - command: profile-session-start\n        type: command\n",
	})

	assembled := Assemble(cfg, nil)

	assert.Contains(t, sessionStartCommands(assembled.Wire().Unified), "profile-session-start",
		"profile-shipped SessionStart hook must be in the assembled set")
}

// TestAssemble_DoesNotMutateConfig guards the duplication fix:
// apply-hooks calls Assemble once per backend in a loop. If it
// aliased and appended to the hooks its source handed back, the second backend
// would accumulate duplicate bundle hooks.
//
// The source is a directory profile now that the config-level block is gone, so
// the aliasing risk sits in the resolved profile rather than in the config, and
// a THIRD call is what makes the assertion mean something: two equal lengths
// could both already be wrong.
func TestAssemble_DoesNotMutateConfig(t *testing.T) {
	cfg := dirProfileCfg(t, []string{"p"}, map[string]string{
		"p": "hooks:\n  unified:\n    session_start:\n      - command: profile-session-start\n        type: command\n",
	})

	first := Assemble(cfg, nil)
	second := Assemble(cfg, nil)
	third := Assemble(cfg, nil)

	assert.Equal(t, len(first.Wire().Unified.SessionStart), len(second.Wire().Unified.SessionStart),
		"repeated calls must not accumulate hooks via shared state")
	assert.Equal(t, len(first.Wire().Unified.SessionStart), len(third.Wire().Unified.SessionStart),
		"and the count must be STABLE, not merely equal between two already-grown calls")
	assert.Contains(t, commandsOf(first.For("session_start")), "profile-session-start",
		"the profile hook is present, so the counts above are counting something")
}

// TestAssemble_WithInvalidProfile must not panic on a default
// profile reference that has no definition.
func TestAssemble_WithInvalidProfile(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"non-existent-profile"}}},
	})

	assembled := Assemble(cfg, nil)
	assert.NotEmpty(t, commandsOf(assembled.For("turn_end")), "ctxloom's own hooks are still assembled around the unresolvable profile")
}

func TestAssemble_CircularProfileIsWarnedNotMasked(t *testing.T) {
	cfg := dirProfileCfg(t, []string{"loopy"}, map[string]string{
		"loopy": "parents:\n  - loopy\n",
	})

	// The profile set resolves through the Config's reporter; render it the
	// way a composition root would, so the warning reaches the sink under test.
	cfg.SetReporter(strictness.Sink("ctxloom"))
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	Assemble(cfg, nil)

	assert.Contains(t, buf.String(), "inheritance",
		"the real cause (inheritance) must reach the warning: got %q", buf.String())
}
