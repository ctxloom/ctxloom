//go:build integration && !windows

package integration

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// ownerRefusalPollTimeout bounds the wait for the first session's mock engine
// to echo the readiness sentinel back through the pty. Generous for CI: the
// plugin spawn is a real self-exec + go-plugin handshake, and the
// coordinator standup precedes it.
const ownerRefusalPollTimeout = 20 * time.Second

// ownerRefusalSentinel is the line typed into the first session's pty whose
// echo proves that session is fully up: `ctxloom run` hosts its coordinator
// BEFORE it starts the engine transport, so a mock parked in its echo loop
// implies the owner lock is already held.
const ownerRefusalSentinel = "owner-refusal-sentinel"

// ownerLocks lists every owner lock under the isolated HOME's coordinator
// state root, so the test can assert on exactly how many processes hold a
// project claim — on disk, never from a log line.
func ownerLocks(t *testing.T, home string) []string {
	t.Helper()
	root := filepath.Join(home, paths.AppDirName, paths.CoordDirName)
	locks, err := filepath.Glob(filepath.Join(root, "*", coord.OwnerLockFileName))
	require.NoError(t, err)
	return locks
}

// journalsBeside lists the .jsonl journals next to an owner lock.
func journalsBeside(t *testing.T, lock string) []string {
	t.Helper()
	journals, err := filepath.Glob(filepath.Join(filepath.Dir(lock), "*.jsonl"))
	require.NoError(t, err)
	return journals
}

// TestSecondOwner_IsRefusedLoudly_AndTheFirstKeepsItsState proves the
// ONE-coordinator-per-project invariant across two REAL `ctxloom` processes:
// while a `ctxloom run` holds a project's owner lock, a second `ctxloom run`
// against the same project exits with the fatal-findings code carrying the
// owned-state refusal by name — it does not degrade to an ephemeral
// coordinator of its own — and the first's lock and journals are untouched
// by the attempt. Once the first exits, a fresh run claims the project
// normally.
//
// This is the two-process property no in-process gate can see
// (internal/adapters/mcp's shim_never_hosts_test proves the shim's own
// construction path is gone; this proves the cross-process defence,
// coord.claimOwner, refuses rather than shares or degrades).
func TestSecondOwner_IsRefusedLoudly_AndTheFirstKeepsItsState(t *testing.T) {
	env := setupTestEnv(t)
	_, err := env.SetupMockLM()
	require.NoError(t, err)
	writeFragment(t, env, "owner-fragment", nil, "owner refusal test content")

	first, err := env.RunPTY(80, 24, []string{"CTXLOOM_MOCK_ECHO_STDIN=1"}, "run", "-f", "owner-fragment")
	require.NoError(t, err, "start the first ctxloom run")
	// Registered the moment the session exists, unconditionally (see
	// mock_reap_hardkill_test.go for the invariant every cleanup here keeps).
	t.Cleanup(first.Close)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("first session pty output: %s", first.Output())
		}
	})

	_, err = first.Write([]byte(ownerRefusalSentinel + "\n"))
	require.NoError(t, err)
	require.True(t, first.WaitForOutput(ownerRefusalPollTimeout, func(out string) bool {
		return strings.Contains(out, "mock echo: "+ownerRefusalSentinel)
	}), "the first session never echoed %q — it is not standing; output:\n%s", ownerRefusalSentinel, first.Output())

	locks := ownerLocks(t, env.HomeDir)
	require.Len(t, locks, 1, "the first session must hold exactly one owner lock; found %v", locks)
	lock := locks[0]
	raw, err := os.ReadFile(lock)
	require.NoError(t, err)
	require.Equal(t, strconv.Itoa(first.PID()), strings.TrimSpace(string(raw)), "the owner lock must be stamped with the first session's pid")
	journalsBefore := journalsBeside(t, lock)
	require.NotEmpty(t, journalsBefore, "the first session's coordinator must have opened its journals beside the lock")

	// THE SECOND CLAIMANT: another `ctxloom run` on the same project while
	// the first holds the lock. A one-shot is enough — every `ctxloom run`
	// hosts its coordinator before it starts the engine.
	second := env.Command(nil, "run", "--one-shot", "-f", "owner-fragment", "second claimant")
	out, runErr := second.CombinedOutput()
	var exit *exec.ExitError
	require.True(t, errors.As(runErr, &exit), "the second run must exit non-zero, not %v; output:\n%s", runErr, out)
	require.Equal(t, strictness.ExitCodeFatalFindings, exit.ExitCode(), "the second run must exit with the fatal-findings code; output:\n%s", out)
	require.Contains(t, string(out), "["+string(strictness.ClassOwner)+"]", "the refusal must be a typed finding in its own class; output:\n%s", out)
	require.Contains(t, string(out), coord.ErrStateOwned.Error(), "the refusal must name the owned state; output:\n%s", out)

	// The loser left the winner alone: same single lock, same pid, journals
	// still there, and no second state dir claimed anywhere.
	locksAfter := ownerLocks(t, env.HomeDir)
	require.Equal(t, locks, locksAfter, "the refused run must not add or remove an owner lock")
	rawAfter, err := os.ReadFile(lock)
	require.NoError(t, err)
	require.Equal(t, string(raw), string(rawAfter), "the refused run must not restamp the first session's lock")
	require.Equal(t, journalsBefore, journalsBeside(t, lock), "the refused run must not touch the first session's journals")

	// The first session ends; its lock is released with it.
	_, err = first.Write([]byte("quit\n"))
	require.NoError(t, err)
	exited, _ := first.Wait(ownerRefusalPollTimeout)
	require.True(t, exited, "the first session did not exit after quit; output:\n%s", first.Output())
	require.Empty(t, ownerLocks(t, env.HomeDir), "the first session must release its owner lock on exit")

	// With the project unowned, a fresh run claims it normally.
	third := env.Command(nil, "run", "--one-shot", "-f", "owner-fragment", "third claimant")
	out, err = third.CombinedOutput()
	require.NoError(t, err, "a run after the owner exited must succeed; output:\n%s", out)
	require.NotContains(t, string(out), coord.ErrStateOwned.Error(), "nothing owns the project any more; output:\n%s", out)
}
