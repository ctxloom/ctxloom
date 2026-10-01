package agent

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestApprovalHooks_TheTwoRoutesToTheHuman: a run whose approver is the
// human carries exactly two approval hooks — the permission ask for every
// tool its rules left open, and the pre-tool hook for the two tools that
// can only be answered with rewritten input — each one `ctxloom hook
// permission` naming the event it serves, outliving the approval timeout so
// the human's decision can still reach it.
func TestApprovalHooks_TheTwoRoutesToTheHuman(t *testing.T) {
	const approval = 15 * time.Minute
	h := ApprovalHooks(approval)

	require.Len(t, h.PermissionAsk, 1)
	ask := h.PermissionAsk[0]
	assert.Empty(t, ask.Matcher, "every tool the rules left open is asked about; no matcher narrows it")
	assert.True(t, strings.HasSuffix(ask.Command, " hook permission --event PermissionRequest"), ask.Command)

	require.Len(t, h.PreTool, 1)
	pre := h.PreTool[0]
	assert.Equal(t, "AskUserQuestion|ExitPlanMode", pre.Matcher)
	assert.True(t, strings.HasSuffix(pre.Command, " hook permission --event PreToolUse"), pre.Command)
	for _, tool := range []string{"AskUserQuestion", "ExitPlanMode"} {
		assert.Regexp(t, regexp.MustCompile("^(?:"+pre.Matcher+")$"), tool)
	}
	assert.NotRegexp(t, regexp.MustCompile("^(?:"+pre.Matcher+")$"), "Bash")

	for _, hook := range []struct {
		name, cmd, typ string
		timeout        int
	}{
		{"permission_ask", ask.Command, ask.Type, ask.Timeout},
		{"pre_tool", pre.Command, pre.Type, pre.Timeout},
	} {
		assert.True(t, strings.HasPrefix(hook.cmd, shellSingleQuote(CtxloomCommand())+" "), "%s runs ctxloom's own binary: %s", hook.name, hook.cmd)
		assert.Equal(t, "command", hook.typ, hook.name)
		assert.Equal(t, int((approval + ApprovalHookSlack).Seconds()), hook.timeout,
			"%s outlives the approval timeout, so a decision made at the deadline still reaches the engine", hook.name)
	}

	assert.Empty(t, h.PostTool)
	assert.Empty(t, h.SessionStart)
	assert.Empty(t, h.TurnStart)
}
