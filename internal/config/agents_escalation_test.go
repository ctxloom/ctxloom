package config

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The orchestrator-routed approval ladder is gone: a human answers the ENGINE'S
// OWN permission prompt in its tmux window rather than ctxloom brokering a
// second approval UI. The `escalation:` key that configured that ladder is
// still parsed onto agents.Agent, still normalised, and still echoed by
// `agent show` as "escalation: N rung(s)" — while nothing consumes it.
//
// That is worse than a silent no-op: the CLI CONFIRMS a setting that has no
// effect, which is this project's characteristic defect wearing a success
// message. Same shape and same reason as the stranded-agents-directory
// signpost above — a key nothing reads has to say so.
func TestLoadAgents_RetiredEscalationKey_IsReported(t *testing.T) {
	cfg := NewFixture(Fixture{
		Agents: map[string]agents.Agent{"coder": {
			LLM:        "claude-code",
			Escalation: []agents.EscalationRung{{Action: "relay"}},
		}},
	})

	mark := strictness.Checkpoint()
	got := cfg.LoadAgents()

	// The binding still resolves — a retired key does not take the working
	// half of the agent down with it.
	require.Len(t, got, 1, "the agent itself is still usable")
	assert.Equal(t, "coder", got[0].Name)

	findings := strictness.Since(mark)
	require.Len(t, findings, 1, "an `escalation:` ladder nothing reads must be reported")
	assert.Equal(t, strictness.ClassMigration, findings[0].Class)
	assert.Contains(t, findings[0].Message, "coder", "the finding names the agent carrying the dead key")
	assert.Contains(t, findings[0].Message, "escalation", "and the key itself")
	// The REMEDY, not just the complaint: this refusal is the entire user
	// interface for the failure, so it must name the action that ends it.
	assert.Contains(t, findings[0].FixIt, "escalation", "the fix-it names what to delete")
}

// The guarded half. Without it the assertion above could pass because EVERY
// agent fires the finding, which would make the signpost noise rather than a
// signal — and would fire on every project that never used escalation at all.
func TestLoadAgents_NoEscalationKey_IsSilent(t *testing.T) {
	cfg := NewFixture(Fixture{
		Agents: map[string]agents.Agent{"coder": {LLM: "claude-code"}},
	})

	mark := strictness.Checkpoint()
	require.Len(t, cfg.LoadAgents(), 1)

	assert.Empty(t, strictness.Since(mark),
		"an agent that never declared an escalation ladder must produce no finding")
}
