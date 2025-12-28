package grpc

import (
	"github.com/hashicorp/go-plugin"
)

// HandshakeConfig is used to verify plugin compatibility.
// The magic cookie ensures host and plugin are using the same protocol.
var HandshakeConfig = plugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "SCM_PLUGIN",
	MagicCookieValue: "ai-backend-v1",
}

// PluginName is the name used in the plugin map.
const PluginName = "ai_plugin"

// PluginMap is the map of plugins the host can dispense.
var PluginMap = map[string]plugin.Plugin{
	PluginName: &AIPluginGRPC{},
}
