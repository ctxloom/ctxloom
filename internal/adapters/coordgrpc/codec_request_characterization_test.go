package coordgrpc

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// TestAgentRequestFromWire_DecodesEveryKind pins the agent-request decode for
// every kind: the plane-2 verbs decode to their coord request, a peer send is
// refused as local-only, and a request naming no kind is unsupported.
func TestAgentRequestFromWire_DecodesEveryKind(t *testing.T) {
	input, err := structpb.NewStruct(map[string]any{"prompt": "do it", "workspace": "worktree", "dirty_tree_handler": "commit"})
	require.NoError(t, err)
	args, err := structpb.NewStruct(map[string]any{"q": "x"})
	require.NoError(t, err)
	wantArgs, err := json.Marshal(map[string]any{"q": "x"})
	require.NoError(t, err)

	cases := []struct {
		name string
		kind any
		want any
	}{
		{"spawn", &agentcoordpb.AgentRequest_SpawnAgent{SpawnAgent: &agentcoordpb.SpawnAgentRequest{Role: "finder", Input: input}},
			coord.SpawnRequest{Agent: "finder", Prompt: "do it", Workspace: "worktree", DirtyTree: "commit"}},
		{"spawn without input", &agentcoordpb.AgentRequest_SpawnAgent{SpawnAgent: &agentcoordpb.SpawnAgentRequest{Role: "finder"}},
			coord.SpawnRequest{Agent: "finder"}},
		{"list runs", &agentcoordpb.AgentRequest_ListRuns{ListRuns: &agentcoordpb.ListRunsRequest{Role: "finder", IncludeTerminal: true}},
			coord.RosterRequest{Role: "finder", IncludeTerminal: true}},
		{"stop run", &agentcoordpb.AgentRequest_StopRun{StopRun: &agentcoordpb.StopRun{RunId: "r-1", Reason: "done"}},
			coord.StopRun{RunID: "r-1", Reason: "done"}},
		{"control run", &agentcoordpb.AgentRequest_ControlRun{ControlRun: &agentcoordpb.ControlRun{Verb: &agentcoordpb.ControlRun_Steer{Steer: &agentcoordpb.ControlSteer{Harp: "h", Text: "left"}}}},
			coord.ControlRequest{Verb: coord.ControlVerbSteer, Harp: "h", Body: "left"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := &agentcoordpb.AgentRequest{RequestId: "q-1", Timeout: durationpb.New(3 * time.Second)}
			setAgentRequestKind(t, req, tc.kind)
			got, err := AgentRequestFromWire(req)
			require.NoError(t, err)
			assert.Equal(t, "q-1", got.RequestID)
			assert.Equal(t, 3*time.Second, got.Timeout)
			assert.Equal(t, tc.want, got.Kind)
		})
	}

	t.Run("host", func(t *testing.T) {
		got, err := AgentRequestFromWire(&agentcoordpb.AgentRequest{Kind: &agentcoordpb.AgentRequest_Host{Host: &agentcoordpb.HostRequest{Tool: "search", Args: args}}})
		require.NoError(t, err)
		host, ok := got.Kind.(coord.HostRequest)
		require.True(t, ok)
		assert.Equal(t, "search", host.Tool)
		assert.JSONEq(t, string(wantArgs), string(host.Args))
	})
	t.Run("control run refusal passes through", func(t *testing.T) {
		_, err := AgentRequestFromWire(&agentcoordpb.AgentRequest{Kind: &agentcoordpb.AgentRequest_ControlRun{ControlRun: &agentcoordpb.ControlRun{}}})
		require.Error(t, err)
	})
	t.Run("peer send is local", func(t *testing.T) {
		_, err := AgentRequestFromWire(&agentcoordpb.AgentRequest{Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &agentcoordpb.PeerSendRequest{}}})
		require.ErrorIs(t, err, coord.ErrPeerSendIsLocal)
	})
	t.Run("no kind is unsupported", func(t *testing.T) {
		_, err := AgentRequestFromWire(&agentcoordpb.AgentRequest{})
		require.ErrorIs(t, err, coord.ErrUnsupportedRequest)
	})
}

// setAgentRequestKind sets req's oneof from one of its wrapper types.
func setAgentRequestKind(t *testing.T, req *agentcoordpb.AgentRequest, kind any) {
	t.Helper()
	switch k := kind.(type) {
	case *agentcoordpb.AgentRequest_SpawnAgent:
		req.Kind = k
	case *agentcoordpb.AgentRequest_ListRuns:
		req.Kind = k
	case *agentcoordpb.AgentRequest_StopRun:
		req.Kind = k
	case *agentcoordpb.AgentRequest_ControlRun:
		req.Kind = k
	default:
		t.Fatalf("unhandled request kind %T", kind)
	}
}

// TestControlRequestFromWire_DecodesEveryVerb pins the control-verb decode:
// each verb maps to its coord verb with its body, a verb with no harp — or a
// body verb with an empty body — is refused, and a ControlRun naming no verb
// is refused.
func TestControlRequestFromWire_DecodesEveryVerb(t *testing.T) {
	cases := []struct {
		name string
		run  *agentcoordpb.ControlRun
		want coord.ControlRequest
	}{
		{"steer", &agentcoordpb.ControlRun{Verb: &agentcoordpb.ControlRun_Steer{Steer: &agentcoordpb.ControlSteer{Harp: "h", Text: "t"}}},
			coord.ControlRequest{Verb: coord.ControlVerbSteer, Harp: "h", Body: "t"}},
		{"question", &agentcoordpb.ControlRun{Verb: &agentcoordpb.ControlRun_Question{Question: &agentcoordpb.ControlQuestion{Harp: "h", Text: "q"}}},
			coord.ControlRequest{Verb: coord.ControlVerbQuestion, Harp: "h", Body: "q"}},
		{"summarize", &agentcoordpb.ControlRun{Verb: &agentcoordpb.ControlRun_Summarize{Summarize: &agentcoordpb.ControlSummarize{Harp: "h", Focus: "f"}}},
			coord.ControlRequest{Verb: coord.ControlVerbSummarize, Harp: "h", Body: "f"}},
		{"pause", &agentcoordpb.ControlRun{Verb: &agentcoordpb.ControlRun_Pause{Pause: &agentcoordpb.ControlPause{Harp: "h", Reason: "why"}}},
			coord.ControlRequest{Verb: coord.ControlVerbPause, Harp: "h", Body: "why"}},
		{"resume", &agentcoordpb.ControlRun{Verb: &agentcoordpb.ControlRun_Resume{Resume: &agentcoordpb.ControlResume{Harp: "h"}}},
			coord.ControlRequest{Verb: coord.ControlVerbResume, Harp: "h"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := controlRequestFromWire(tc.run)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}

	refused := map[string]*agentcoordpb.ControlRun{
		"steer without harp":      {Verb: &agentcoordpb.ControlRun_Steer{Steer: &agentcoordpb.ControlSteer{Text: "t"}}},
		"steer without text":      {Verb: &agentcoordpb.ControlRun_Steer{Steer: &agentcoordpb.ControlSteer{Harp: "h"}}},
		"question without harp":   {Verb: &agentcoordpb.ControlRun_Question{Question: &agentcoordpb.ControlQuestion{Text: "q"}}},
		"question without text":   {Verb: &agentcoordpb.ControlRun_Question{Question: &agentcoordpb.ControlQuestion{Harp: "h"}}},
		"summarize without harp":  {Verb: &agentcoordpb.ControlRun_Summarize{Summarize: &agentcoordpb.ControlSummarize{Focus: "f"}}},
		"summarize without focus": {Verb: &agentcoordpb.ControlRun_Summarize{Summarize: &agentcoordpb.ControlSummarize{Harp: "h"}}},
		"pause without harp":      {Verb: &agentcoordpb.ControlRun_Pause{Pause: &agentcoordpb.ControlPause{}}},
		"resume without harp":     {Verb: &agentcoordpb.ControlRun_Resume{Resume: &agentcoordpb.ControlResume{}}},
		"no verb":                 {},
	}
	for name, run := range refused {
		t.Run(name, func(t *testing.T) {
			got, err := controlRequestFromWire(run)
			require.Error(t, err)
			assert.Equal(t, coord.ControlRequest{}, got)
		})
	}
}
