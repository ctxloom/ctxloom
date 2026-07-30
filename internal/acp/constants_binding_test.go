package acp_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/acp"
	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/operations"
)

// This file is an EXTERNAL test package on purpose. internal/acp cannot import
// agentcoord/coord or internal/operations — both pull in internal/lm/backends,
// which registers acp.NewACP() and so imports acp back — which is why the two
// env-var names below are hand-copied literals there. An external test package
// is not in that cycle: it can import acp AND the packages above it, so the
// copies can be bound to their originals by a test even though the production
// code cannot bind them by an import.
//
// Without this, a rename on either canonical side leaves the copy stale and the
// only symptom is a container whose engine silently never reaches the surface
// the variable names — no compile error anywhere.

// TestMCPSocketEnvVarMatchesCoord binds the ACP container transport's copy of
// the runner-MCP socket variable to coord.EnvMCPSocket, which the runner exports
// and the in-container shim reads.
func TestMCPSocketEnvVarMatchesCoord(t *testing.T) {
	assert.Equal(t, coord.EnvMCPSocket, acp.MCPSocketEnvVarForTest,
		"the reach-back reads the variable the runner writes; a drift makes the in-container engine silently MCP-less")
}

// TestFsUpstreamEnvVarMatchesOperations binds the ACP driver's copy of the
// fs-upstream address variable to operations.FsUpstreamEnvVar, the only writer.
func TestFsUpstreamEnvVarMatchesOperations(t *testing.T) {
	assert.Equal(t, operations.FsUpstreamEnvVar, acp.FsUpstreamEnvVarForTest,
		"a drift here silently serves fs/* from local disk instead of the connected editor")
}
