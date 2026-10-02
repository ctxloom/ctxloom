package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// A wake is a prompt the hook must RECOGNISE: its nonce is consumed (the
// wake's acknowledgement), and a wake that finds no mail is blocked so it
// costs the owner no model turn. A human's prompt is never either.

// promptCmd is mailDrainCmd with the payload's prompt set to prompt.
func promptCmd(t *testing.T, out *bytes.Buffer, prompt string) *cobra.Command {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"session_id": "s", "hook_event_name": "UserPromptSubmit", "prompt": prompt})
	require.NoError(t, err)
	c := &cobra.Command{}
	c.SetIn(bytes.NewReader(payload))
	c.SetOut(out)
	c.SetErr(&bytes.Buffer{})
	c.SetContext(context.Background())
	return c
}

func armOwnerWake(t *testing.T) string {
	t.Helper()
	nonce, err := spool.ArmWake(spool.NewHomeMapper(), mailDrainOwner)
	require.NoError(t, err)
	return nonce
}

func outstanding(t *testing.T) []string {
	t.Helper()
	out, err := spool.OutstandingWake(spool.NewHomeMapper(), mailDrainOwner)
	require.NoError(t, err)
	return out
}

// decoded parses stdout as the UserPromptSubmit output, whatever its shape.
func decoded(t *testing.T, out *bytes.Buffer) claude.UserPromptSubmitOutput {
	t.Helper()
	var env claude.UserPromptSubmitOutput
	require.NoError(t, json.Unmarshal(out.Bytes(), &env), "stdout is not the output the engine parses:\n%s", out.String())
	return env
}

func TestDrainMail_AWakeWithNothingPendingIsBlockedAndConsumed(t *testing.T) {
	testsupport.Isolate(t)
	nonce := armOwnerWake(t)

	var out bytes.Buffer
	require.NoError(t, drainMail(promptCmd(t, &out, engine.WakeText(nonce)), mailDrainOwner))

	env := decoded(t, &out)
	assert.Equal(t, claude.DecisionBlock, env.Decision, "a stale wake must not cost a model turn")
	assert.NotEmpty(t, env.Reason, "the block names why, for the human who sees it")
	assert.Nil(t, env.HookSpecificOutput, "a blocked prompt carries no context")
	assert.Empty(t, outstanding(t), "the wake was redeemed")
}

// A wake whose nonce was already redeemed (a human's prompt drained the mail
// first) is still the ctxloom's own text, and still has nothing to say.
func TestDrainMail_AnAlreadyRedeemedWakeIsStillBlocked(t *testing.T) {
	testsupport.Isolate(t)
	var out bytes.Buffer
	require.NoError(t, drainMail(promptCmd(t, &out, engine.WakeText("0123456789abcdef")), mailDrainOwner))
	assert.Equal(t, claude.DecisionBlock, decoded(t, &out).Decision)
}

func TestDrainMail_AWakeWithMailDeliversItAndConsumesTheNonce(t *testing.T) {
	testsupport.Isolate(t)
	nonce := armOwnerWake(t)
	name := seedOwnerMail(t, "child-one", "report", "FINAL: done\n")

	var out bytes.Buffer
	require.NoError(t, drainMail(promptCmd(t, &out, engine.WakeText(nonce)), mailDrainOwner))

	env := drainedEnvelope(t, &out)
	assert.Empty(t, env.Decision, "a wake that found mail is a turn")
	assert.Contains(t, env.HookSpecificOutput.AdditionalContext, "FINAL: done")
	assert.Empty(t, outstanding(t), "the wake was redeemed")
	assert.Equal(t, []string{name}, spoolNames(t, spool.DirInConsumed))
}

// A human prompt that QUOTES the wake text is not the wake: it is never
// blocked and redeems nothing. (A human prompt that delivers mail answers
// every wake — TestDrainMail_ADrainClearsEveryOutstandingWake — so the
// distinction shows on a turn with nothing to deliver.)
func TestDrainMail_AHumanPromptQuotingTheWakeIsNotTheWake(t *testing.T) {
	testsupport.Isolate(t)
	nonce := armOwnerWake(t)

	var out bytes.Buffer
	require.NoError(t, drainMail(promptCmd(t, &out, "what did the child say? "+engine.WakeText(nonce)), mailDrainOwner))

	assert.Empty(t, out.String(), "a human's prompt is never blocked, and there is nothing to deliver")
	assert.Equal(t, []string{nonce}, outstanding(t), "quoting the wake text is not the wake")
}

func TestDrainMail_AHumanPromptWithNothingPendingWritesNothing(t *testing.T) {
	testsupport.Isolate(t)
	var out bytes.Buffer
	require.NoError(t, drainMail(promptCmd(t, &out, "hello"), mailDrainOwner))
	assert.Empty(t, out.String(), "a human's prompt with no mail is silent — never a block")
}

// A payload the hook cannot read as a prompt (an engine that sends none) is
// treated as a human's turn: delivered, never blocked.
func TestDrainMail_APayloadWithoutAPromptIsNotAWake(t *testing.T) {
	testsupport.Isolate(t)
	seedOwnerMail(t, "child-one", "report", "FINAL: done\n")
	for _, payload := range []string{`{"event":"turn_start"}`, `not json`, ``} {
		var out bytes.Buffer
		c := mailDrainCmd(&out)
		c.SetIn(strings.NewReader(payload))
		require.NoError(t, drainMail(c, mailDrainOwner))
		if out.Len() > 0 {
			assert.Empty(t, decoded(t, &out).Decision, "payload %q", payload)
		}
	}
}

func TestDrainMail_ADuplicateMessageIsDeliveredOnce(t *testing.T) {
	testsupport.Isolate(t)
	w, err := spool.NewWriter(spool.NewHomeMapper(), mailDrainOwner, spool.DirIn, "coord")
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, err := w.Write(&spool.Message{Kind: "report", FromHarp: "child-one", To: mailDrainOwner, OriginID: "msg-1", Body: "FINAL: once\n"})
		require.NoError(t, err)
	}

	var out bytes.Buffer
	require.NoError(t, drainMail(promptCmd(t, &out, "hi"), mailDrainOwner))
	assert.Equal(t, 1, strings.Count(drainedEnvelope(t, &out).HookSpecificOutput.AdditionalContext, "FINAL: once"))

	_, err = w.Write(&spool.Message{Kind: "report", FromHarp: "child-one", To: mailDrainOwner, OriginID: "msg-1", Body: "FINAL: once\n"})
	require.NoError(t, err)
	out.Reset()
	require.NoError(t, drainMail(promptCmd(t, &out, "again"), mailDrainOwner))
	assert.Empty(t, out.String(), "a re-send of a delivered message is not delivered again")
	assert.Len(t, spoolNames(t, spool.DirInConsumed), 3)
}

// TestDrainMail_ADrainClearsEveryOutstandingWake is F1 at the hook: a human's
// prompt that drains the mail a wake announced also answers that wake —
// otherwise its nonce stays armed and refuses every later wake. Every nonce
// is cleared, not only one the prompt names.
// MUTATION — drop the ClearWakes call after a delivering drain — turns this red.
func TestDrainMail_ADrainClearsEveryOutstandingWake(t *testing.T) {
	testsupport.Isolate(t)
	armOwnerWake(t)
	armOwnerWake(t)
	seedOwnerMail(t, "child", "result", "done\n")

	var out bytes.Buffer
	require.NoError(t, drainMail(promptCmd(t, &out, "what did the child say?"), mailDrainOwner))

	require.NotNil(t, decoded(t, &out).HookSpecificOutput, "the mail was delivered")
	assert.Empty(t, outstanding(t), "a delivering turn answers every wake that announced its mail")
}

// TestDrainMail_ATurnWithNoMailLeavesWakesArmed: a prompt that delivered
// nothing answered nothing — a wake in flight for mail that arrives next is
// still owed its turn.
func TestDrainMail_ATurnWithNoMailLeavesWakesArmed(t *testing.T) {
	testsupport.Isolate(t)
	armOwnerWake(t)

	var out bytes.Buffer
	require.NoError(t, drainMail(promptCmd(t, &out, "hello"), mailDrainOwner))

	assert.Empty(t, out.String())
	assert.Len(t, outstanding(t), 1)
}
