package jsonrpc

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type unmarshalable struct{ Ch chan int }

// U013-F02: marshalResult turned a result-marshalling failure into a
// SUCCESSFUL response carrying "result":null — the peer is told the request
// succeeded and receives zero payload.
func TestMarshalResult_FailureIsAnError(t *testing.T) {
	raw, rerr := marshalResult(&unmarshalable{Ch: make(chan int)})
	assert.Nil(t, raw)
	require.NotNil(t, rerr, "an unmarshalable result must become a JSON-RPC error, not a null success")
	assert.Equal(t, CodeInternalError, rerr.Code)

	raw, rerr = marshalResult(nil)
	assert.Nil(t, rerr, "a nil result is still a valid content-less success")
	assert.Equal(t, json.RawMessage("null"), raw)
}

// U013-F03: mustParams dropped params on a marshal failure and the frame went
// out anyway — an outbound request/notification with its payload silently
// removed.
func TestMarshalParams_FailureRefusesTheFrame(t *testing.T) {
	_, err := marshalParams("session/prompt", &unmarshalable{Ch: make(chan int)})
	assert.Error(t, err, "a frame whose params cannot be marshalled must not be sent stripped")
}

// U013-F17: Notify("") emitted {"jsonrpc":"2.0"} — a frame with no method and
// no id, which the peer must drop as garbage. Nothing validated the method.
func TestNotify_EmptyMethodIsRefused(t *testing.T) {
	c := &Conn{}
	assert.Error(t, c.Notify("", nil))
	_, err := c.Go("", nil)
	assert.Error(t, err)
}
