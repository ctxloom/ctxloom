package grpc

import (
	"testing"

	"github.com/hashicorp/go-plugin"
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

// Every entry the host can dispense must be usable over net/rpc without
// crashing the host, for the same reason.
func TestPluginMap_EntriesRefuseNetRPC(t *testing.T) {
	for key, p := range PluginMap() {
		require.NotPanics(t, func() {
			_, err := p.Server(nil)
			assert.Error(t, err, "plugin %q: net/rpc Server must be refused", key)
		}, "plugin %q", key)
	}
}

// PluginMap must hand out a FRESH map per dial: a package-level mutable map is
// shared by every caller, so one mutation (a test registering an extra plugin,
// a caller pruning an entry) silently changes what every later dial dispenses.
func TestPluginMap_IsNotSharedMutableState(t *testing.T) {
	first := PluginMap()
	require.Contains(t, first, LLMPluginKey)

	first["injected"] = &LLMGRPCPlugin{}
	delete(first, LLMPluginKey)

	second := PluginMap()
	assert.Contains(t, second, LLMPluginKey, "a later dial must still dispense the LLM plugin")
	assert.NotContains(t, second, "injected", "a mutation of one caller's map must not reach the next dial")

	var _ map[string]plugin.Plugin = second
}
