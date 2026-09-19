package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/selfexec"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// Every run mints a harp, and the mint is the first thing that can fail. A
// run that cannot be named has no session dir, no coordinator to host, no
// transcript home and no address a child could reach back to — every later
// phase would fail for a cause three phases behind it. So the mint's failure
// is the RUN's failure, at the mint, before any engine process exists.
//
// Both halves are observed directly. The store's own error must be the error
// the run exits with (errors.Is through every wrap, not a paraphrase of it),
// and the engine runner must never have been spawned: the binary the run
// re-invokes as `llm serve` is swapped for a stub that leaves a witness file,
// so "no spawn" is the witness's absence rather than the absence of some
// later side effect a spawned-then-refused runner would also lack.
func TestRun_SessionMintFailureRefusesTheRunBeforeAnyEngineSpawn(t *testing.T) {
	dir := runCLIFixture(t)
	// The fixture's config names no engine; the mock backend is the one that
	// needs no binary installed, so it is configured under its own label.
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".ctxloom", "config.yaml"),
		[]byte(fmt.Sprintf("version: %d\nllm:\n  configs:\n    mock:\n      type: mock\n", config.CurrentConfigVersion)), 0o644))
	resetApp()

	witness := filepath.Join(t.TempDir(), "runner-spawned")
	stub := filepath.Join(t.TempDir(), "ctxloom")
	require.NoError(t, os.WriteFile(stub, []byte("#!/bin/sh\n: > "+witness+"\nexit 1\n"), 0o755))
	t.Cleanup(selfexec.SetPathForTesting(stub))

	// A regular file where the sessions root must be a directory: opening the
	// store fails on its first touch, the mint.
	root, err := paths.HomeSessionsDir()
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(root))
	require.NoError(t, os.WriteFile(root, []byte("not a directory\n"), 0o644))

	res := runCLI(t, "run", "--one-shot", "--llm", "mock", "-p", "dev", "hi")

	require.Error(t, res.err, "a run that cannot mint its session must not exit 0")
	assert.ErrorIs(t, res.err, syscall.ENOTDIR,
		"the store's failure is the run's failure — surfaced as-is, not warned past and rediscovered later; got: %v", res.err)
	assert.NoFileExists(t, witness,
		"no engine runner may be spawned for a run that has no session; stderr:\n%s", res.stderr)
}
