package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
)

// TestRuntimeForPolicy: a container policy exposes its launch runtime (the
// originator awaits the container through it); none/worktree carry none.
func TestRuntimeForPolicy(t *testing.T) {
	rt := isolation.ProbeRuntime("docker") // Host{} when no daemon — still a Runtime
	c := isolation.NewContainerFor(rt, "mock").WithImage("img")
	assert.NotNil(t, RuntimeForPolicy(c), "a container policy carries its runtime")

	assert.Nil(t, RuntimeForPolicy(isolation.None{}), "none has no runtime")
}
