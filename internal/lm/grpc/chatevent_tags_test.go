package grpc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// TestChatEvent_TagsAreStableAndRawIsOutsideTheOneof pins ChatEvent's wire
// layout, including the part that reads as a numbering mistake and is not:
// `raw` is tag 5 and sits OUTSIDE the `event` oneof, while `terminal` is tag 6
// and sits inside it, so the oneof's members are numerically interleaved with a
// sibling field.
//
// Two invariants, and the second is why the first must not be "tidied".
//
//  1. raw is a plain sibling, never a oneof member. The proto states the reason:
//     permissions never ride on raw, and keeping it out of the oneof is what
//     makes that structurally impossible rather than merely conventional. raw
//     is also emitted ALONGSIDE entry (a `_meta` supplement), which a oneof
//     member could not be.
//
//  2. The tags are wire identity, not cosmetics. This decodes a hand-built
//     frame rather than a marshal/unmarshal round trip, so it is stated in the
//     bytes an already-deployed peer sends: renumbering to make the oneof
//     contiguous would silently reroute those bytes to unknown fields — a
//     forwarded terminal request would vanish and its answer would never come.
//     The interleaving is the ordinary shape of additive proto evolution here
//     (raw arrived with IR3, terminal with B1/gap G6), and calling it an
//     error is a readability objection whose only remedy is a wire break.
func TestChatEvent_TagsAreStableAndRawIsOutsideTheOneof(t *testing.T) {
	fields := (&ChatEvent{}).ProtoReflect().Descriptor().Fields()

	raw := fields.ByName("raw")
	require.NotNil(t, raw, "ChatEvent.raw must exist")
	assert.EqualValues(t, 5, raw.Number(), "raw is wire tag 5")
	assert.Nil(t, raw.ContainingOneof(),
		"raw must stay OUTSIDE the event oneof: it is emitted alongside entry, and keeping it out is what stops a permission ever riding on it")

	terminal := fields.ByName("terminal")
	require.NotNil(t, terminal, "ChatEvent.terminal must exist")
	assert.EqualValues(t, 6, terminal.Number(), "terminal is wire tag 6")
	require.NotNil(t, terminal.ContainingOneof(), "terminal must be a member of the event oneof")
	assert.Equal(t, "event", string(terminal.ContainingOneof().Name()))

	// A frame as an already-deployed peer encodes it: tag 6 (LEN) carrying a
	// ChatTerminalRequest{id: "t1"}, then tag 5 (LEN) carrying raw bytes "hi".
	frame := []byte{
		0x32, 0x04, 0x0a, 0x02, 't', '1', // field 6, LEN(4): {field 1, LEN(2): "t1"}
		0x2a, 0x02, 'h', 'i', // field 5, LEN(2): "hi"
	}
	var got ChatEvent
	require.NoError(t, proto.Unmarshal(frame, &got))
	assert.Equal(t, "t1", got.GetTerminal().GetId(), "tag 6 must decode as the forwarded terminal request")
	assert.Equal(t, []byte("hi"), got.GetRaw(), "tag 5 must decode as the raw side channel")
}
