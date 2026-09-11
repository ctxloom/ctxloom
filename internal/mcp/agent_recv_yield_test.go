package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/mcpschema"
	"github.com/ctxloom/ctxloom/internal/agents"
)

// agent_recv allows one live receive per session: a newer receive supersedes
// an older parked one. That supersession is a YIELD, not a failure — nothing
// is lost, the newer receive holds the park — yet it used to surface as a
// plain error, which a harness renders as a failed tool call. A coordinator
// reading "failed" retries, and the retry preempts the receive that was about
// to deliver; observed more than six times in one session, every one caused
// by the verdict itself. These tests pin the contract that stops that loop:
// the superseded call completes successfully with no messages and a
// disposition saying so, and the survivor still gets the mail exactly once.
// The timeout verdicts — a coordinator's is a success, a leaf's an error —
// are pinned beside these in agent_recv_timeout_test.go.

// stdioPreemption stages two overlapping receives on the stdio surface against
// a quiet coordinator mailbox, then sends one message once the yield has been
// observed. It returns the call that yielded, the call that survived to
// receive, and the server for follow-up receives. Which goroutine registers
// first is not controlled — the contract is symmetric, so the test only needs
// exactly one of the two to yield.
type stdioPreemption struct {
	s        *ctxServer
	yield    *agentRecvResult
	yieldErr error
	survivor *agentRecvResult
	survErr  error
	sent     string
}

// instructionToFinish is the word the child's guidance turns on. The pins
// below check for it directly, not only for the guidance constant: the defect
// was a coordinator obeying "finish", and a pin on the constant alone would
// let a rephrased copy of the same instruction back in.
const instructionToFinish = "finish"

type stdioRecvOutcome struct {
	out *agentRecvResult
	err error
}

func stageStdioPreemption(t *testing.T) stdioPreemption {
	t.Helper()
	cfg, c, _ := buildHostCoordinator(t, map[string]agents.Agent{
		"worker": headlessAgent("p1"),
	})
	self := coord.Identity{Harp: "coordinator-harp", Depth: 0}
	s := &ctxServer{
		cfg:    cfg,
		self:   self,
		agents: &agentDelegation{self: self, c: c},
	}
	_, runOut, err := s.handleAgentRun(context.Background(), nil, agentRunInput{Agent: "worker", Prompt: "go"})
	require.NoError(t, err)
	child := coord.Identity{Harp: runOut.Harp, Depth: 1}

	// The spawned child's first turn is bridged into this mailbox unasked.
	// Drain until a receive times out, so the only message that can complete
	// the staged receives is the one this test sends.
	require.Eventually(t, func() bool {
		_, out, rerr := s.handleAgentRecv(context.Background(), nil, agentRecvInput{Wait: 1})
		return rerr == nil && out != nil && out.Disposition == mcpschema.RecvDispositionTimedOut
	}, 30*time.Second, time.Millisecond, "the coordinator mailbox never went quiet")

	outcomes := make(chan stdioRecvOutcome, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, out, rerr := s.handleAgentRecv(context.Background(), nil, agentRecvInput{Wait: 10})
			outcomes <- stdioRecvOutcome{out: out, err: rerr}
		}()
	}

	st := stdioPreemption{s: s, sent: "after the yield"}
	select {
	case first := <-outcomes:
		st.yield, st.yieldErr = first.out, first.err
	case <-time.After(10 * time.Second):
		t.Fatal("neither receive completed, so the older one was never superseded")
	}

	// Only now — with the supersession a fact — does mail land, so it can only
	// be delivered to the survivor.
	_, err = c.AgentSend(child, "parent", coord.KindMessage, st.sent, nil, "")
	require.NoError(t, err)
	select {
	case second := <-outcomes:
		st.survivor, st.survErr = second.out, second.err
	case <-time.After(10 * time.Second):
		t.Fatal("the surviving receive never completed")
	}
	return st
}

// wireShape is the tool result as the harness sees it: the JSON keys, not
// the Go field names, are the contract.
func wireShape(t *testing.T, out *agentRecvResult) map[string]any {
	t.Helper()
	raw, err := json.Marshal(out)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	return m
}

func TestHandleAgentRecv_SupersededReceiveYieldsAsSuccess(t *testing.T) {
	st := stageStdioPreemption(t)

	require.NoError(t, st.yieldErr, "a superseded receive is a yield, never an error: an error renders as a failed call and the caller retries into the receive that would have delivered")
	require.NotNil(t, st.yield)
	assert.Empty(t, st.yield.Messages, "the yielded call delivers nothing; the survivor holds the park")
	shape := wireShape(t, st.yield)
	assert.Equal(t, mcpschema.RecvDispositionYielded, shape["disposition"],
		"the result must SAY it yielded, that nothing was lost, and that it must not be retried")
}

func TestHandleAgentRecv_SurvivingReceiveDeliversThePendingMessageExactlyOnce(t *testing.T) {
	st := stageStdioPreemption(t)

	require.NoError(t, st.survErr)
	require.NotNil(t, st.survivor)
	seen := 0
	for _, m := range st.survivor.Messages {
		if m.Body == st.sent {
			seen++
		}
	}
	assert.Equal(t, 1, seen, "the message queued across the supersession reaches the survivor exactly once; got %+v", st.survivor.Messages)
	assert.Empty(t, wireShape(t, st.survivor)["disposition"], "a delivering receive carries no yield disposition")

	// The next receive cursor-acks the batch; the message must not come back.
	_, again, err := st.s.handleAgentRecv(context.Background(), nil, agentRecvInput{Wait: 1})
	require.NoError(t, err)
	assert.Empty(t, again.Messages, "nothing else is pending; got %+v", again.Messages)
	assert.Equal(t, mcpschema.RecvDispositionTimedOut, again.Disposition)
}

// TestRecvOutcome_TimeoutVerdictFollowsTheAudience: one sentinel, two
// audiences, two verdict SHAPES. The coord sentinel cannot know who is
// parked, so the classification happens where the audience is known: a
// leaf's timeout is an error carrying the child's instruction, with the
// sentinel identity intact for every errors.Is caller; a coordinator's is a
// successful empty receive that never sees that instruction.
func TestRecvOutcome_TimeoutVerdictFollowsTheAudience(t *testing.T) {
	disposition, leaf := recvOutcome(coord.ErrRecvTimeout, 5*time.Second, true)
	require.ErrorIs(t, leaf, coord.ErrRecvTimeout)
	assert.Empty(t, disposition, "a leaf's timeout is a failure, not a disposition")
	assert.Contains(t, leaf.Error(), recvTimeoutLeafGuidance)

	disposition, coordinator := recvOutcome(coord.ErrRecvTimeout, 5*time.Second, false)
	require.NoError(t, coordinator, "a coordinator's timeout is not a failure")
	assert.Equal(t, mcpschema.RecvDispositionTimedOut, disposition)
	assert.NotContains(t, disposition, instructionToFinish)

	assert.NotContains(t, coord.ErrRecvTimeout.Error(), instructionToFinish,
		"the shared sentinel must stay audience-neutral; the child's instruction belongs only where a child reads it")

	for _, leaf := range []bool{true, false} {
		disposition, failure := recvOutcome(coord.ErrRecvPreempted, time.Second, leaf)
		require.NoError(t, failure, "a yield is a success for every audience")
		assert.Equal(t, mcpschema.RecvDispositionYielded, disposition)
	}

	other := errors.New("something else")
	disposition, failure := recvOutcome(other, time.Second, true)
	assert.Equal(t, other, failure, "only the timeout gains guidance")
	assert.Empty(t, disposition)
}

// The runner surface parks locally in the Home, so the supersession is
// observable with no coordinator behind it.
func runnerRecv(t *testing.T, h mcp.ToolHandler, wait int) (*mcp.CallToolResult, error) {
	t.Helper()
	args, err := json.Marshal(map[string]any{"wait": wait})
	require.NoError(t, err)
	return h(context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: args}})
}

type runnerRecvOutcome struct {
	res *mcp.CallToolResult
	err error
}

func TestRecvHandler_SupersededReceiveYieldsAsSuccess(t *testing.T) {
	h := recvHandler(testHome(t), false)

	outcomes := make(chan runnerRecvOutcome, 2)
	for i := 0; i < 2; i++ {
		go func() {
			res, err := runnerRecv(t, h, 2)
			outcomes <- runnerRecvOutcome{res: res, err: err}
		}()
	}

	var yield runnerRecvOutcome
	select {
	case yield = <-outcomes:
	case <-time.After(10 * time.Second):
		t.Fatal("neither receive completed")
	}
	require.NoError(t, yield.err, "a superseded receive is a yield, never an error")
	shape, ok := yield.res.StructuredContent.(map[string]any)
	require.True(t, ok, "structured content is the object the schema declares; got %T", yield.res.StructuredContent)
	assert.Empty(t, shape["messages"])
	assert.Equal(t, mcpschema.RecvDispositionYielded, shape["disposition"])

	// The survivor then times out on the quiet Home — as a coordinator, so
	// successfully, with no instruction to finish.
	var survivor runnerRecvOutcome
	select {
	case survivor = <-outcomes:
	case <-time.After(10 * time.Second):
		t.Fatal("the surviving receive never completed")
	}
	require.NoError(t, survivor.err)
	shape, ok = survivor.res.StructuredContent.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, mcpschema.RecvDispositionTimedOut, shape["disposition"])
}
