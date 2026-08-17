package operations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/lm/isolation"
)

// TestRuntimeForPolicy: a container policy exposes its launch runtime for the
// docker-exec launcher; none/worktree carry none (nil).
func TestRuntimeForPolicy(t *testing.T) {
	rt := isolation.ProbeRuntime("docker") // Host{} when no daemon — still a Runtime
	c := isolation.NewContainerFor(rt, "mock").WithImage("img")
	assert.NotNil(t, RuntimeForPolicy(c), "a container policy carries its runtime")

	assert.Nil(t, RuntimeForPolicy(isolation.None{}), "none has no runtime")
}

// TestContainerPersistDirForPolicy: a container policy reports the in-container,
// home-rebased persist path (where the turn reads the handoff); none returns "".
func TestContainerPersistDirForPolicy(t *testing.T) {
	c := isolation.NewContainerFor(isolation.ProbeRuntime("docker"), "mock").WithImage("img")
	got := ContainerPersistDirForPolicy(c, "regal-rash-dash")
	assert.True(t, strings.HasSuffix(got, "/.ctxloom/sessions/regal-rash-dash/persist"),
		"in-container persist path under the fresh container HOME, got %q", got)
	assert.True(t, strings.HasPrefix(got, "/home/"), "rebased under the container HOME, not the host home")

	assert.Equal(t, "", ContainerPersistDirForPolicy(isolation.None{}, "regal-rash-dash"), "none has no container persist dir")
	assert.Equal(t, "", ContainerPersistDirForPolicy(c, ""), "a blank harp has no session state")
}
