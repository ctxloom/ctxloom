package coord

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
)

// The coordinator IS the verb set: a transport that holds a Verbs holds the
// coordinator, and a verb missing from the interface is a compile error here.
var _ Verbs = (*Coordinator)(nil)

// validating is what every request type is: the ONE validation site.
type validating interface{ Validate() error }

// TestVerbs_EveryRequestValidatesItselfOnce pins the contract every transport
// relies on: a request refused by Validate is refused with ErrInvalidRequest,
// and a well-formed one passes — so a handler that re-checked a field would
// be duplicating a refusal, not adding one.
func TestVerbs_EveryRequestValidatesItselfOnce(t *testing.T) {
	cases := []struct {
		name    string
		req     validating
		refused string // "" = valid
	}{
		{"spawn ok", SpawnRequest{Agent: "worker", Prompt: "go"}, ""},
		{"spawn with axes", SpawnRequest{Agent: "worker", Prompt: "go", Workspace: "worktree", DirtyTree: "fail"}, ""},
		{"spawn needs an agent", SpawnRequest{Prompt: "go"}, "agent is required"},
		{"spawn needs a prompt", SpawnRequest{Agent: "worker"}, "prompt is required"},
		{"spawn refuses an unknown workspace", SpawnRequest{Agent: "worker", Prompt: "go", Workspace: "sandbox"}, "workspace"},
		{"spawn refuses an unknown dirty-tree handler", SpawnRequest{Agent: "worker", Prompt: "go", DirtyTree: "yolo"}, "dirty_tree_handler"},
		{"send ok", SendRequest{To: "child-1", Kind: KindMessage, Body: "hello"}, ""},
		{"send reply carries its own kind", SendRequest{To: ParentAddress, Body: "42", InReplyTo: "ask-1"}, ""},
		{"send needs a recipient", SendRequest{Kind: KindMessage, Body: "hello"}, "to is required"},
		{"send needs a body", SendRequest{To: "child-1", Kind: KindMessage}, "body is required"},
		{"send refuses a reserved kind", SendRequest{To: "child-1", Kind: KindExited, Body: "x"}, "reserved"},
		{"send refuses a body over the cap", SendRequest{To: "child-1", Kind: KindMessage, Body: strings.Repeat("x", MaxSendBodyBytes+1)}, "MaxSendBodyBytes"},
		{"send at the cap passes", SendRequest{To: "child-1", Kind: KindMessage, Body: strings.Repeat("x", MaxSendBodyBytes)}, ""},
		{"stop one child", StopRequest{Harp: "child-1"}, ""},
		{"stop all needs a reason", StopRequest{}, "reason is required"},
		{"stop all with a reason", StopRequest{Reason: "done"}, ""},
		{"report ok", ReportRequest{Scope: "progress", Body: "halfway"}, ""},
		{"report needs a body", ReportRequest{Scope: "progress"}, "body is required"},
		{"fetch ok", FetchRequest{Harp: "child-1", ArtifactID: "plan/x"}, ""},
		{"fetch needs an artifact id", FetchRequest{Harp: "child-1"}, "artifact_id is required"},
		{"control steer ok", ControlRequest{Verb: ControlVerbSteer, Harp: "child-1", Body: "rebase"}, ""},
		{"control pause takes no body", ControlRequest{Verb: ControlVerbPause, Harp: "child-1"}, ""},
		{"control steer needs a body", ControlRequest{Verb: ControlVerbSteer, Harp: "child-1"}, "body is required"},
		{"control refuses an unknown verb", ControlRequest{Verb: "nudge", Harp: "child-1"}, "verb"},
		{"host needs a tool", HostRequest{}, "tool is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.req.Validate()
			if tc.refused == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidRequest)
			assert.Contains(t, err.Error(), tc.refused)
		})
	}
}

// TestVerbs_SendRefusesTheBodyCapByName: the cap is refused at the verb, by
// its name, with nothing written — the recipient's spool holds no file for a
// send that was refused.
func TestVerbs_SendRefusesTheBodyCapByName(t *testing.T) {
	err := SendRequest{To: "child-1", Kind: KindMessage, Body: strings.Repeat("x", MaxSendBodyBytes+1)}.Validate()
	require.ErrorIs(t, err, ErrBodyTooLarge)
	assert.Contains(t, err.Error(), "MaxSendBodyBytes")
}

// TestSendRequestFromWire_KindIngress pins the wire-side ingress of the
// closed kind vocabulary at the decode: an enum NUMBER this build does not
// declare is refused by number (proto3 enums are open — 99 survives
// Unmarshal as itself), the unset kind reaches Validate as "required", a
// coordinator-reserved kind is refused as reserved, and every sender-allowed
// kind passes.
func TestSendRequestFromWire_KindIngress(t *testing.T) {
	var unknown agentcoordpb.PeerSendRequest
	require.NoError(t, proto.Unmarshal([]byte{7 << 3, 99}, &unknown), "field 7 (kind), varint 99")
	require.EqualValues(t, 99, unknown.GetKind(), "proto3 keeps the unrecognised number")
	_, err := sendRequestFromWire(&unknown)
	require.ErrorIs(t, err, ErrInvalidRequest)
	assert.Contains(t, err.Error(), "99", "the refusal names the offending value")

	sr, err := sendRequestFromWire(&agentcoordpb.PeerSendRequest{ToAgentId: "child-1", Text: "x"})
	require.NoError(t, err)
	assert.ErrorContains(t, sr.Validate(), "kind is required")

	for _, k := range []agentcoordpb.MessageKind{
		agentcoordpb.MessageKind_MESSAGE_KIND_APPROVAL_REQUEST,
		agentcoordpb.MessageKind_MESSAGE_KIND_USER_INJECTED,
		agentcoordpb.MessageKind_MESSAGE_KIND_USER_CONTROL,
		agentcoordpb.MessageKind_MESSAGE_KIND_EXITED,
		agentcoordpb.MessageKind_MESSAGE_KIND_STEER,
	} {
		sr, err := sendRequestFromWire(&agentcoordpb.PeerSendRequest{ToAgentId: "child-1", Text: "x", Kind: k})
		require.NoError(t, err)
		err = sr.Validate()
		require.ErrorIs(t, err, ErrInvalidRequest, "%v", k)
		assert.Contains(t, err.Error(), "reserved", "%v", k)
	}
	for _, k := range []agentcoordpb.MessageKind{
		agentcoordpb.MessageKind_MESSAGE_KIND_MESSAGE,
		agentcoordpb.MessageKind_MESSAGE_KIND_RESULT,
		agentcoordpb.MessageKind_MESSAGE_KIND_ERROR,
		agentcoordpb.MessageKind_MESSAGE_KIND_QUESTION,
	} {
		sr, err := sendRequestFromWire(&agentcoordpb.PeerSendRequest{ToAgentId: "child-1", Text: "x", Kind: k})
		require.NoError(t, err)
		assert.NoError(t, sr.Validate(), "%v", k)
	}
	_, err = sendRequestFromWire(&agentcoordpb.PeerSendRequest{ToAgentId: "child-1", ToRole: ParentAddress, Text: "x"})
	assert.ErrorContains(t, err, "exactly one of")
}
