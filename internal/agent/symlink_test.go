// Symlink tests verify the executable path resolution.
package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetExecutablePath(t *testing.T) {
	// Set a test path
	SetExecutablePathForTesting("/test/path/to/ctxloom")
	defer SetExecutablePathForTesting("") // Reset after test

	path, err := GetExecutablePath()
	assert.NoError(t, err)
	assert.Equal(t, "/test/path/to/ctxloom", path)
}

// TestCtxloomPathSkewed pins the comparison that drives the startup
// version-skew warning: a different ctxloom earlier on PATH than the
// running binary is the one failure bare commands can't catch.
func TestCtxloomPathSkewed(t *testing.T) {
	assert.True(t, ctxloomPathSkewed("/home/u/go/bin/ctxloom", "/usr/bin/ctxloom"),
		"a different binary on PATH is skew")
	assert.False(t, ctxloomPathSkewed("/home/u/go/bin/ctxloom", "/home/u/go/bin/ctxloom"),
		"same path is not skew")
	assert.False(t, ctxloomPathSkewed("/home/u/go/bin/ctxloom", ""),
		"not on PATH is not treated as skew")
}
