package acp

// The env-var literals this package cannot import, re-exported for the
// cross-package binding test in constants_binding_test.go (package acp_test).
// Their canonical homes — agentcoord/coord and internal/operations — both sit
// ABOVE this package in the import graph, so only an EXTERNAL test package can
// see both sides at once.
const (
	MCPSocketEnvVarForTest  = mcpSocketEnvVar
	FsUpstreamEnvVarForTest = fsUpstreamEnvVar
)
