//go:build integration && !windows

package integration

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// treesPollTimeout bounds the wait for the first session's mock engine to
// echo the readiness sentinel back through the pty. Generous for CI: the
// plugin spawn is a real self-exec + go-plugin handshake, and the
// coordinator standup precedes it.
const treesPollTimeout = 20 * time.Second

// treesSentinel is the line typed into the first session's pty whose echo
// proves that session is fully up: `ctxloom run` hosts its coordinator BEFORE
// it starts the engine transport, so a mock parked in its echo loop implies
// its root's owner lock is already held.
const treesSentinel = "independent-trees-sentinel"

// ownerLocks lists every owner lock file under the isolated HOME's
// coordinator state root: one per coordinator root still on disk
// (coord/<project-key>/<root-harp>/owner.lock). Whether a process HOLDS one is
// coord.ProbeOwner's answer, never the file's presence.
func ownerLocks(t *testing.T, home string) []string {
	t.Helper()
	root := filepath.Join(home, paths.AppDirName, paths.CoordDirName)
	locks, err := filepath.Glob(filepath.Join(root, "*", "*", coord.OwnerLockFileName))
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

// TestSecondRun_FoundsItsOwnTree_AndTheFirstKeepsItsState proves the
// independent-trees property across two REAL `ctxloom` processes: while a
// `ctxloom run` owns its root in a project, a second `ctxloom run` against the
// same project is NOT refused — it founds a root of its own, runs to
// completion, and leaves that root behind unowned for a later resume — and
// the first's lock, stamp and journals are untouched throughout.
//
// This is the two-process property no in-process gate can see: two
// processes, two owner locks, one project.
func TestSecondRun_FoundsItsOwnTree_AndTheFirstKeepsItsState(t *testing.T) {
	env := setupTestEnv(t)
	_, err := env.SetupMockLM()
	require.NoError(t, err)
	writeFragment(t, env, "trees-fragment", nil, "independent trees test content")

	first, err := env.RunPTY(80, 24, []string{"CTXLOOM_MOCK_ECHO_STDIN=1"}, "run", "-f", "trees-fragment")
	require.NoError(t, err, "start the first ctxloom run")
	// Registered the moment the session exists, unconditionally (see
	// mock_reap_hardkill_test.go for the invariant every cleanup here keeps).
	t.Cleanup(first.Close)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("first session pty output: %s", first.Output())
		}
	})

	_, err = first.Write([]byte(treesSentinel + "\n"))
	require.NoError(t, err)
	require.True(t, first.WaitForOutput(treesPollTimeout, func(out string) bool {
		return strings.Contains(out, "mock echo: "+treesSentinel)
	}), "the first session never echoed %q — it is not standing; output:\n%s", treesSentinel, first.Output())

	locks := ownerLocks(t, env.HomeDir)
	require.Len(t, locks, 1, "the first session must hold exactly one root; found %v", locks)
	lock := locks[0]
	owner, err := coord.ProbeOwner(filepath.Dir(lock))
	require.NoError(t, err)
	require.True(t, owner.Held, "the first session must hold its root's owner lock")
	require.Equal(t, first.PID(), owner.PID, "the root must be stamped with the first session's pid")
	require.Equal(t, coord.OwnerInteractive, owner.Mode)
	require.Equal(t, owner.Harp, filepath.Base(filepath.Dir(lock)), "a fresh run's root is named by its own harp")
	journalsBefore := journalsBeside(t, lock)
	require.NotEmpty(t, journalsBefore, "the first session's coordinator must have opened its journals in its root")

	// THE SECOND RUN: another `ctxloom run` on the same project while the
	// first owns its root. A one-shot is enough — every `ctxloom run` hosts
	// its coordinator before it starts the engine.
	second := env.Command(nil, "run", "--one-shot", "-f", "trees-fragment", "second tree")
	out, err := second.CombinedOutput()
	require.NoError(t, err, "a second run in an owned project must succeed on a tree of its own; output:\n%s", out)
	require.NotContains(t, string(out), coord.ErrStateOwned.Error(), "a fresh run is never refused; output:\n%s", out)
	require.NotContains(t, string(out), "coordinator startup failed", "the second run must have hosted its own coordinator; output:\n%s", out)

	// The second tree left the first alone — same owner, same journals — and
	// its own root stays, unowned, for a `--session` resume.
	locksAfter := ownerLocks(t, env.HomeDir)
	require.Len(t, locksAfter, 2, "each run has a root of its own; found %v", locksAfter)
	require.Contains(t, locksAfter, lock)
	for _, l := range locksAfter {
		if l == lock {
			continue
		}
		require.Equal(t, filepath.Dir(filepath.Dir(lock)), filepath.Dir(filepath.Dir(l)), "both roots belong to the one project")
		st, err := coord.ProbeOwner(filepath.Dir(l))
		require.NoError(t, err)
		require.False(t, st.Held, "the exited second run holds nothing")
	}
	ownerAfter, err := coord.ProbeOwner(filepath.Dir(lock))
	require.NoError(t, err)
	require.Equal(t, owner, ownerAfter, "the second run must not take or restamp the first session's root")
	require.Equal(t, journalsBefore, journalsBeside(t, lock), "the second run must not touch the first session's journals")

	// The first session ends; its lock is released with it.
	_, err = first.Write([]byte("quit\n"))
	require.NoError(t, err)
	exited, _ := first.Wait(treesPollTimeout)
	require.True(t, exited, "the first session did not exit after quit; output:\n%s", first.Output())
	released, err := coord.ProbeOwner(filepath.Dir(lock))
	require.NoError(t, err)
	require.False(t, released.Held, "the first session must release its root on exit")
}
