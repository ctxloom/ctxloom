package agent

import (
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestNewSkillMatesHook_InvokesTheSkillMatesCallback pins what the installed
// PostToolUse hook runs, and that its budget accounts for reading the
// transcript (the "already invoked" derivation) rather than only its stdin.
//
// MUTATION -- change what NewSkillMatesHook invokes, or give it
// ToolReflectTimeout -- turns this red.
func TestNewSkillMatesHook_InvokesTheSkillMatesCallback(t *testing.T) {
	h := NewSkillMatesHook()

	assert.Equal(t, "command", h.Type)
	assert.Equal(t, []string{"hook", "skill-mates"}, h.Args, "no arguments: the session is resolved at fire time")
	assert.Greater(t, h.Timeout, ToolReflectTimeout,
		"reading a transcript needs longer than a hook that only reads its own stdin")
	assert.Equal(t, SkillMatesTimeout, h.Timeout)
}

// TestNewSkillMatesHook_MatchesOnlyTheSkillTool pins that the engine is asked
// to fire this hook for skill calls alone — narrowed by the neutral skill
// tool class, which each engine's hooks approach maps to its own tool, never
// by one engine's tool name. Every other tool call would pay a process spawn
// plus a transcript read to learn it has nothing to say.
//
// MUTATION -- drop the Tool class -- turns this red.
func TestNewSkillMatesHook_MatchesOnlyTheSkillTool(t *testing.T) {
	h := NewSkillMatesHook()
	assert.Equal(t, wire.ToolSkill, h.Tool)
	assert.Empty(t, h.Matcher, "the native matcher is the engine's to write")
}
