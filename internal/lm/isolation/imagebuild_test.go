package isolation

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withFakeSelfExe points resolveSelfExe at a dummy static-binary stand-in so
// ensureImage's build path runs hermetically (no ELF inspection of the real
// test binary, whose linkage is toolchain-dependent).
func withFakeSelfExe(t *testing.T) {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "ctxloom")
	require.NoError(t, os.WriteFile(exe, []byte("#!/bin/true\n"), 0o755))
	orig := resolveSelfExe
	resolveSelfExe = func() (string, error) { return exe, nil }
	t.Cleanup(func() { resolveSelfExe = orig })
}

// TestEnsureImage_PresentIsNoop: `<binary> image inspect` succeeding (binary
// "true") short-circuits — no build attempted, no error.
func TestEnsureImage_PresentIsNoop(t *testing.T) {
	c := NewContainerFor(fakeRuntime{name: "docker", binary: "true", available: true}, "kiro")
	assert.NoError(t, c.ensureImage(context.Background()))
}

// TestEnsureImage_AbsentWithoutRecipeDegrades: an absent image on a profile with
// no embedded Containerfile (the default profile / explicit-image path) errors so
// the caller degrades — exactly the pre-build-support behaviour.
func TestEnsureImage_AbsentWithoutRecipeDegrades(t *testing.T) {
	c := NewContainer(fakeRuntime{name: "docker", binary: "false", available: true}, "img")
	err := c.ensureImage(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not present")
	assert.Contains(t, err.Error(), "no embedded build recipe")
}

// TestEnsureImage_BuildFailureDegrades: an absent image WITH a recipe attempts a
// local build; a failing runtime (binary "false") surfaces a build error so the
// caller degrades — the build is best-effort, never a blocker.
func TestEnsureImage_BuildFailureDegrades(t *testing.T) {
	withFakeSelfExe(t)
	c := NewContainerFor(fakeRuntime{name: "docker", binary: "false", available: true}, "kiro")
	err := c.ensureImage(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "local build of container image")
}

// TestEnsureImage_UnbuildableBinaryDegrades: an absent image with a recipe but a
// running binary that cannot serve in-container (resolveSelfExe errors) degrades
// with a diagnostic pointing at the ahead-of-time recipes.
func TestEnsureImage_UnbuildableBinaryDegrades(t *testing.T) {
	orig := resolveSelfExe
	resolveSelfExe = func() (string, error) { return "", assert.AnError }
	t.Cleanup(func() { resolveSelfExe = orig })

	c := NewContainerFor(fakeRuntime{name: "docker", binary: "false", available: true}, "kiro")
	err := c.ensureImage(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be built from this binary")
}

// TestTailLines bounds the failure diagnostics to the last n lines.
func TestTailLines(t *testing.T) {
	assert.Equal(t, "b\nc", tailLines("a\nb\nc\n", 2))
	assert.Equal(t, "a\nb\nc", tailLines("a\nb\nc", 5), "short input passes through whole")
}
