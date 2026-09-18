package agent

import (
	"strings"
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
	assert.Contains(t, h.Command, "hook skill-mates")
	assert.Greater(t, h.Timeout, ToolReflectTimeout,
		"reading a transcript needs longer than a hook that only reads its own stdin")
	assert.Equal(t, SkillMatesTimeout, h.Timeout)
}

// TestNewSkillMatesHook_MatchesOnlyTheSkillTool pins that the engine is asked
// to fire this hook for Skill calls alone. Every other tool call would pay a
// process spawn plus a transcript read to learn it has nothing to say.
//
// MUTATION -- drop the Matcher -- turns this red.
func TestNewSkillMatesHook_MatchesOnlyTheSkillTool(t *testing.T) {
	assert.Equal(t, "Skill", NewSkillMatesHook().Matcher)
}

// TestNewSkillMatesHook_QuotesTheBinaryPathAndTakesNoArguments: the self-exec
// path is shell-quoted, and the callback resolves its session from the
// environment at fire time -- the installed command outlives the session that
// wrote it.
func TestNewSkillMatesHook_QuotesTheBinaryPathAndTakesNoArguments(t *testing.T) {
	h := NewSkillMatesHook()
	assert.True(t, strings.HasPrefix(h.Command, "'"), "the binary path must be single-quoted: %q", h.Command)
	assert.Equal(t, shellSingleQuote(CtxloomCommand())+" hook skill-mates", h.Command)
}
