// Code generation: llm.pb.go and llm_grpc.pb.go are generated from llm.proto
// by buf (the canonical pipeline — see buf.gen.yaml at the repo root). Run
// `just proto`, or `buf generate` from the repo root inside the devcontainer.
// Deliberately not a //go:generate directive: buf must run from the repo root,
// not this package directory.

package grpc

import (
	"github.com/hashicorp/go-plugin"
)

// HandshakeConfig is used to verify plugin compatibility.
// The magic cookie ensures host and plugin are using the same protocol.
var HandshakeConfig = plugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "CTXLOOM_PLUGIN",
	MagicCookieValue: "ai-backend-v1",
}

// LLMPluginKey is the name used in the plugin map.
const LLMPluginKey = "ai_plugin"

// PluginMap is the map of plugins the host can dispense.
var PluginMap = map[string]plugin.Plugin{
	LLMPluginKey: &LLMGRPCPlugin{},
}
