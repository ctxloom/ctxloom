package grpc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The plugin map is handed to go-plugin at every dial, and go-plugin decides
// which protocol to speak from the value it finds there. A net/rpc dial (a
// version mismatch, a handshake that negotiates down, a caller that builds its
// own ClientConfig) therefore reaches Server/Client on this type. Those must
// REFUSE, because this plugin only speaks gRPC — a refusal is a diagnosable
// dial failure, whereas a nil embedded plugin.Plugin makes the same call panic
// inside the host.
func TestLLMGRPCPlugin_NetRPCIsRefusedNotPanicked(t *testing.T) {
	p := &LLMGRPCPlugin{}

	require.NotPanics(t, func() {
		_, err := p.Server(nil)
		assert.Error(t, err, "net/rpc Server must be refused")
	})
	require.NotPanics(t, func() {
		_, err := p.Client(nil, nil)
		assert.Error(t, err, "net/rpc Client must be refused")
	})
}

