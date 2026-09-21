package coordgrpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/spool"
)

// TestPeerMessageProto_ProjectsKindOntoTheTypedField is the push side of the
// typed-kind contract: a mailbox message rides the wire with PeerMessage.kind
// set from the closed vocabulary, and `structured` carries the sender's
// companion VERBATIM — no "kind" key is merged in, because nothing on the
// receive side reads one anymore.
func TestPeerMessageProto_ProjectsKindOntoTheTypedField(t *testing.T) {
	for _, kind := range coord.MailKinds() {
		pm, err := PeerMessageToWire(coord.Message{ID: "m-1", From: "child-harp-1", Kind: kind, Body: "hi"})
		require.NoError(t, err, "mail kind %q must project", kind)
		assert.Equal(t, kind, agentcoordpb.LegacyKindName(pm.GetKind()), "typed kind must round-trip for %q", kind)
		assert.Nil(t, pm.GetStructured(), "a message with no companion must not grow one to carry %q", kind)
	}

	pm, err := PeerMessageToWire(coord.Message{
		ID: "m-2", From: "child-harp-1", Kind: coord.KindResult, Body: "hi",
		Structured: json.RawMessage(`{"kind":"approval_request","answer":"yes"}`),
	})
	require.NoError(t, err)
	assert.Equal(t, agentcoordpb.MessageKind_MESSAGE_KIND_RESULT, pm.GetKind())
	assert.Equal(t, map[string]any{"kind": "approval_request", "answer": "yes"}, pm.GetStructured().AsMap(),
		"the sender's companion travels untouched: its kind key is inert, not overwritten")

	_, err = PeerMessageToWire(coord.Message{ID: "m-3", From: "child-harp-1", Kind: "a_kind_nobody_mapped", Body: "hi"})
	require.Error(t, err, "a kind outside the closed vocabulary must not be pushed as UNSPECIFIED")
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
	_, err := SendRequestFromWire(&unknown)
	require.ErrorIs(t, err, coord.ErrInvalidRequest)
	assert.Contains(t, err.Error(), "99", "the refusal names the offending value")

	sr, err := SendRequestFromWire(&agentcoordpb.PeerSendRequest{ToAgentId: "child-1", Text: "x"})
	require.NoError(t, err)
	assert.ErrorContains(t, sr.Validate(), "kind is required")

	for _, k := range []agentcoordpb.MessageKind{
		agentcoordpb.MessageKind_MESSAGE_KIND_APPROVAL_REQUEST,
		agentcoordpb.MessageKind_MESSAGE_KIND_USER_INJECTED,
		agentcoordpb.MessageKind_MESSAGE_KIND_USER_CONTROL,
		agentcoordpb.MessageKind_MESSAGE_KIND_EXITED,
		agentcoordpb.MessageKind_MESSAGE_KIND_STEER,
	} {
		sr, err := SendRequestFromWire(&agentcoordpb.PeerSendRequest{ToAgentId: "child-1", Text: "x", Kind: k})
		require.NoError(t, err)
		err = sr.Validate()
		require.ErrorIs(t, err, coord.ErrInvalidRequest, "%v", k)
		assert.Contains(t, err.Error(), "reserved", "%v", k)
	}
	for _, k := range []agentcoordpb.MessageKind{
		agentcoordpb.MessageKind_MESSAGE_KIND_MESSAGE,
		agentcoordpb.MessageKind_MESSAGE_KIND_RESULT,
		agentcoordpb.MessageKind_MESSAGE_KIND_ERROR,
		agentcoordpb.MessageKind_MESSAGE_KIND_QUESTION,
	} {
		sr, err := SendRequestFromWire(&agentcoordpb.PeerSendRequest{ToAgentId: "child-1", Text: "x", Kind: k})
		require.NoError(t, err)
		assert.NoError(t, sr.Validate(), "%v", k)
	}
	_, err = SendRequestFromWire(&agentcoordpb.PeerSendRequest{ToAgentId: "child-1", ToRole: coord.ParentAddress, Text: "x"})
	assert.ErrorContains(t, err, "exactly one of")
}

// TestSpoolDoorbell_EnumExhaustiveBothDirections is the drift alarm. The two
// closed vocabularies — spool.Dir and the wire enum — are declared in different
// languages in different files, and the ONLY thing keeping them in step is the
// table in spooldoorbell.go. Adding a sixth spool directory, or a sixth enum
// value, must fail HERE rather than silently not map: an unmapped directory
// means a doorbell that cannot be rung (mail that only ever arrives by sweep,
// i.e. slower with no error anywhere), and an unmapped enum value means an
// inbound doorbell refused as invalid.
func TestSpoolDoorbell_EnumExhaustiveBothDirections(t *testing.T) {
	dirs := spool.Dirs()

	// Every wire enum value except UNSPECIFIED must have a spool.Dir, and the
	// counts must match: this is what catches a value added on ONE side only.
	wireValues := make([]agentcoordpb.SpoolDir, 0, len(agentcoordpb.SpoolDir_name))
	for num := range agentcoordpb.SpoolDir_name {
		if v := agentcoordpb.SpoolDir(num); v != agentcoordpb.SpoolDir_SPOOL_DIR_UNSPECIFIED {
			wireValues = append(wireValues, v)
		}
	}
	assert.Len(t, wireValues, len(dirs),
		"the wire enum and spool.Dir must have the same number of live values; a value added to one side alone has no mapping")

	t.Run("spool.Dir -> wire -> spool.Dir", func(t *testing.T) {
		seen := map[agentcoordpb.SpoolDir]spool.Dir{}
		for _, d := range dirs {
			w, err := SpoolDirToWire(d)
			require.NoError(t, err, "spool directory %q has no wire representation", d)
			assert.NotEqual(t, agentcoordpb.SpoolDir_SPOOL_DIR_UNSPECIFIED, w,
				"%q must not encode as the unspecified value, which is invalid at every consumer", d)
			if prev, dup := seen[w]; dup {
				t.Fatalf("%q and %q both encode as %s: two spool directories collapsed onto one wire value, so a consume-rename could be read out of the wrong directory", prev, d, w)
			}
			seen[w] = d

			back, err := SpoolDirFromWire(w)
			require.NoError(t, err)
			assert.Equal(t, d, back, "the round trip must return the ORIGINAL directory")
		}
	})

	t.Run("wire -> spool.Dir -> wire", func(t *testing.T) {
		for _, w := range wireValues {
			d, err := SpoolDirFromWire(w)
			require.NoError(t, err, "wire value %s has no spool.Dir", w)
			assert.NoError(t, d.Validate(), "%s decoded to %q, which is not in spool's closed set", w, d)

			back, err := SpoolDirToWire(d)
			require.NoError(t, err)
			assert.Equal(t, w, back, "the round trip must return the ORIGINAL wire value")
		}
	})

	t.Run("unspecified and unknown are refused, never defaulted", func(t *testing.T) {
		_, err := SpoolDirFromWire(agentcoordpb.SpoolDir_SPOOL_DIR_UNSPECIFIED)
		assert.Error(t, err, "the zero value must not be a way to arrive unclassified")
		_, err = SpoolDirFromWire(agentcoordpb.SpoolDir(9999))
		assert.Error(t, err, "a value from a future build must fail loudly, not guess a directory")
		_, err = SpoolDirToWire(spool.Dir("in/somewhere-new"))
		assert.Error(t, err, "an unmappable directory must not encode as UNSPECIFIED and become the receiver's problem")
	})
}

// TestStatusFromErr_MapsTypedCausesOnly pins the code each typed cause earns,
// and that an untyped error is INTERNAL rather than guessed at from prose.
func TestStatusFromErr_MapsTypedCausesOnly(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code codes.Code
	}{
		{coord.ErrControlRefused, codes.PermissionDenied},
		{coord.ErrNotInjectable, codes.NotFound},
		{coord.ErrCapabilityUnavailable, codes.FailedPrecondition},
		{coord.ErrAskUnavailable, codes.FailedPrecondition},
		{coord.ErrAskTimeout, codes.DeadlineExceeded},
		{errors.New("permission denied: something that only SAYS so"), codes.Internal},
	} {
		st := StatusFromErr(fmt.Errorf("agent_x: %w", errors.Join(tc.err)))
		assert.EqualValues(t, tc.code, st.GetCode(), tc.err.Error())
		assert.True(t, strings.HasPrefix(st.GetMessage(), "agent_x: "), "the tool names itself in the message")
	}
}

// TestSpoolRefFromProto_RefusesAnInvalidRef is the security pin's decode
// half: a doorbell naming a ref that does not parse — a traversal in the
// name, an unknown directory value, a harp with a separator, an absent
// reference — is refused at the receive chokepoint, and the refusal names
// WHICH field was wrong, or an operator cannot tell a broken mapper from a
// probe. Nothing it refuses can reach the code that resolves a ref into a
// path.
func TestSpoolRefFromProto_RefusesAnInvalidRef(t *testing.T) {
	const doorbellHarp = "doorbell-harp"
	const doorbellName = "00001754919000123456789.00000042.coord.md"
	cases := []struct {
		name string
		msg  *agentcoordpb.SpoolChanged
		want string
	}{
		{
			name: "traversal in the file name",
			msg:  &agentcoordpb.SpoolChanged{Harp: doorbellHarp, Dir: agentcoordpb.SpoolDir_SPOOL_DIR_IN, Name: "../../../etc/passwd"},
			want: "path separator",
		},
		{
			name: "bare dot-dot as the file name",
			msg:  &agentcoordpb.SpoolChanged{Harp: doorbellHarp, Dir: agentcoordpb.SpoolDir_SPOOL_DIR_IN, Name: ".."},
			want: "names a directory",
		},
		{
			name: "unspecified directory",
			msg:  &agentcoordpb.SpoolChanged{Harp: doorbellHarp, Dir: agentcoordpb.SpoolDir_SPOOL_DIR_UNSPECIFIED, Name: doorbellName},
			want: "unknown wire spool directory",
		},
		{
			name: "directory value from a future build",
			msg:  &agentcoordpb.SpoolChanged{Harp: doorbellHarp, Dir: agentcoordpb.SpoolDir(77), Name: doorbellName},
			want: "unknown wire spool directory",
		},
		{
			name: "harp carrying a path separator",
			msg:  &agentcoordpb.SpoolChanged{Harp: "../sibling", Dir: agentcoordpb.SpoolDir_SPOOL_DIR_OUT, Name: doorbellName},
			want: "invalid ref harp",
		},
		{
			name: "empty harp",
			msg:  &agentcoordpb.SpoolChanged{Harp: "", Dir: agentcoordpb.SpoolDir_SPOOL_DIR_OUT, Name: doorbellName},
			want: "invalid ref harp",
		},
		{
			name: "empty name",
			msg:  &agentcoordpb.SpoolChanged{Harp: doorbellHarp, Dir: agentcoordpb.SpoolDir_SPOOL_DIR_OUT, Name: ""},
			want: "file name is required",
		},
		{
			name: "no reference at all",
			msg:  nil,
			want: "carried no reference",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := SpoolRefFromProto(tc.msg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestAgentRequestFromWire_NonStringSpawnInputIsRefused: structpb.Value's
// GetStringValue answers "" for every non-string kind, which is byte-identical
// to omitting the key — and omitting dirty_tree_handler selects the default
// that commits the parent's working tree. Present-but-wrong-type is its own
// input, refused at the decode rather than read as unset.
func TestAgentRequestFromWire_NonStringSpawnInputIsRefused(t *testing.T) {
	in, err := structpb.NewStruct(map[string]any{"prompt": "task", "dirty_tree_handler": true})
	require.NoError(t, err)
	_, err = AgentRequestFromWire(&agentcoordpb.AgentRequest{RequestId: "r-1", Kind: &agentcoordpb.AgentRequest_SpawnAgent{SpawnAgent: &agentcoordpb.SpawnAgentRequest{
		Role: "worker", Input: in,
	}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be a string")
}
