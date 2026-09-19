package sessionlock

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// Every test here runs under testsupport.Isolate: the lock file is a real
// kernel lock on a real file, and that file must be a fixture's, never the
// developer's own ~/.ctxloom/sessions/<harp>.lock — a probe against a live
// harp on this machine would be exactly the rehearsal the row forbids.

func lockPath(t *testing.T, harp string) string {
	t.Helper()
	p, err := paths.HarpLockPath(harp)
	require.NoError(t, err)
	return p
}

// trustEverything makes the filesystem gate a no-op for tests that are
// about the lock, so a CI runner whose temp dir is exotic cannot turn a lock
// assertion into a filesystem one. Tests ABOUT the gate set fsTrust
// themselves.
func trustEverything(t *testing.T) {
	t.Helper()
	prev := fsTrust
	fsTrust = func(string) (bool, string, error) { return true, "test", nil }
	t.Cleanup(func() { fsTrust = prev })
}

// TestVerdict_OnlyDeadMayReclaim pins the single permission the package ever
// grants: Dead. Alive and Indeterminate — and any verdict value that does
// not exist — are refusals. This is the "never treat cannot-determine as
// permission" rule as an assertion rather than a comment.
func TestVerdict_OnlyDeadMayReclaim(t *testing.T) {
	assert.True(t, Dead.MayReclaim())
	assert.False(t, Alive.MayReclaim())
	assert.False(t, Indeterminate.MayReclaim())
	assert.False(t, Verdict(99).MayReclaim(), "an unknown verdict is a refusal, not a permission")
	assert.False(t, Probe{}.Verdict.MayReclaim(), "the zero Probe refuses")
}

// TestInspect_NoLockFile_IsIndeterminateAndCreatesNothing: a harp with no
// lock file is one that started before the lock existed, or one whose Hold
// failed. Neither proves the owner dead, so the answer is Indeterminate —
// and the probe must not CREATE the file, because a file it created would be
// unlocked and read as Dead on the very next probe.
func TestInspect_NoLockFile_IsIndeterminateAndCreatesNothing(t *testing.T) {
	testsupport.Isolate(t)
	trustEverything(t)

	p := Inspect("never-held-harp")
	assert.Equal(t, Indeterminate, p.Verdict)
	assert.False(t, p.Verdict.MayReclaim())
	assert.NotEmpty(t, p.Reason)
	assert.NoFileExists(t, lockPath(t, "never-held-harp"), "a probe must never mint the file it is probing")
}

// TestHold_ThenInspect_IsAliveWithThisPid: the holder's own process is the
// simplest live owner. The content is this pid, for a human reading the
// file — and the probe reports it — but the verdict comes from the lock.
func TestHold_ThenInspect_IsAliveWithThisPid(t *testing.T) {
	testsupport.Isolate(t)
	trustEverything(t)
	require.NoError(t, Hold("held-harp"))
	t.Cleanup(func() { Release("held-harp") })

	raw, err := os.ReadFile(lockPath(t, "held-harp"))
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(os.Getpid())+"\n", string(raw), "the file's content is the holder's pid, newline-terminated")

	p := Inspect("held-harp")
	assert.Equal(t, Alive, p.Verdict)
	assert.False(t, p.Verdict.MayReclaim())
	assert.Equal(t, os.Getpid(), p.PID)
}

// TestHold_IsIdempotentWithinAProcess: a second Hold for a harp this process
// already holds is a no-op success, not a self-deadlock and not an error —
// flock(2) would refuse a second descriptor on the same file, so the package
// has to remember what it already holds.
func TestHold_IsIdempotentWithinAProcess(t *testing.T) {
	testsupport.Isolate(t)
	trustEverything(t)
	require.NoError(t, Hold("twice-harp"))
	t.Cleanup(func() { Release("twice-harp") })
	require.NoError(t, Hold("twice-harp"))
	assert.Equal(t, Alive, Inspect("twice-harp").Verdict)
}

// TestRelease_ThenInspect_IsDeadAndTheFileRemains: a graceful end releases
// the lock and leaves the file — an unlocked file IS the dead signal, and
// its pid tells a human who last ran the session. Removing it would make an
// ended session indistinguishable from one that predates the lock.
func TestRelease_ThenInspect_IsDeadAndTheFileRemains(t *testing.T) {
	testsupport.Isolate(t)
	trustEverything(t)
	require.NoError(t, Hold("ended-harp"))
	Release("ended-harp")

	assert.FileExists(t, lockPath(t, "ended-harp"))
	p := Inspect("ended-harp")
	assert.Equal(t, Dead, p.Verdict)
	assert.True(t, p.Verdict.MayReclaim())
	assert.Equal(t, os.Getpid(), p.PID, "the last holder's pid is still readable")

	Release("ended-harp") // a second release is a no-op
	assert.Equal(t, Dead, Inspect("ended-harp").Verdict)
}

// TestInspect_PidContentPlaysNoPart: garbage where the pid should be changes
// what the probe REPORTS (PID 0) and nothing about what it DECIDES. A pid
// that took part in the decision is the design this lock replaced.
func TestInspect_PidContentPlaysNoPart(t *testing.T) {
	testsupport.Isolate(t)
	trustEverything(t)
	require.NoError(t, Hold("garbage-harp"))
	Release("garbage-harp")
	require.NoError(t, os.WriteFile(lockPath(t, "garbage-harp"), []byte("not a pid\n"), 0o600))

	p := Inspect("garbage-harp")
	assert.Equal(t, Dead, p.Verdict, "unlocked is dead regardless of content")
	assert.Equal(t, 0, p.PID)
}

// TestAcquire_Dead_KeepsTheLockUntilReleased is the sweeper's contract: a
// Dead verdict comes WITH the lock, so a session resumed under the same harp
// mid-sweep waits in Hold rather than racing the reclaim. The second
// descriptor is a raw flock on the same path — the exact thing a concurrent
// Hold would do.
func TestAcquire_Dead_KeepsTheLockUntilReleased(t *testing.T) {
	testsupport.Isolate(t)
	trustEverything(t)
	require.NoError(t, Hold("reclaim-harp"))
	Release("reclaim-harp")

	p, release := Acquire("reclaim-harp")
	require.Equal(t, Dead, p.Verdict)

	other := flock.New(lockPath(t, "reclaim-harp"))
	got, err := other.TryLock()
	require.NoError(t, err)
	assert.False(t, got, "while the sweeper holds a Dead harp's lock nobody else can take it")

	release()
	got, err = other.TryLock()
	require.NoError(t, err)
	assert.True(t, got, "release hands the lock back")
	require.NoError(t, other.Unlock())
}

// TestAcquire_Alive_ReleaseIsHarmless: the release returned with a refusal
// holds nothing and must not disturb the live owner's lock.
func TestAcquire_Alive_ReleaseIsHarmless(t *testing.T) {
	testsupport.Isolate(t)
	trustEverything(t)
	require.NoError(t, Hold("live-harp"))
	t.Cleanup(func() { Release("live-harp") })

	p, release := Acquire("live-harp")
	require.Equal(t, Alive, p.Verdict)
	release()
	assert.Equal(t, Alive, Inspect("live-harp").Verdict, "a refusal's release did not unlock the owner")
}

// TestHold_WhenAnotherHolderWins_ErrorsAndLeavesNoUnlockedFile: a Hold that
// cannot take the lock must not leave the file behind, because an existing
// UNLOCKED file reads as Dead — the one outcome worse than no file at all
// (which reads as Indeterminate, a refusal). The competing holder here is a
// raw flock on the same path; holdWait is shortened so the bounded wait is
// measured in milliseconds, not the production seconds.
func TestHold_WhenAnotherHolderWins_ErrorsAndLeavesNoUnlockedFile(t *testing.T) {
	testsupport.Isolate(t)
	trustEverything(t)
	prev := holdWait
	holdWait = 200 * time.Millisecond
	t.Cleanup(func() { holdWait = prev })

	path := lockPath(t, "contested-harp")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	other := flock.New(path)
	got, err := other.TryLock()
	require.NoError(t, err)
	require.True(t, got)
	t.Cleanup(func() { _ = other.Unlock() })

	err = Hold("contested-harp")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrHeldElsewhere), "the failure is typed, not a message")
	assert.NoFileExists(t, path, "the failed Hold removed its unlockable file so the harp reads Indeterminate, never Dead")
	assert.Equal(t, Indeterminate, Inspect("contested-harp").Verdict)
}

// TestHold_WaitsOutABriefSweeperHold: the same contention, but the other
// holder lets go inside the bounded wait — a sweeper mid-reclaim — and Hold
// then succeeds rather than failing fast.
func TestHold_WaitsOutABriefSweeperHold(t *testing.T) {
	testsupport.Isolate(t)
	trustEverything(t)
	prev := holdWait
	holdWait = 2 * time.Second
	t.Cleanup(func() { holdWait = prev })

	path := lockPath(t, "briefly-contested-harp")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	other := flock.New(path)
	got, err := other.TryLock()
	require.NoError(t, err)
	require.True(t, got)
	go func() {
		time.Sleep(150 * time.Millisecond)
		_ = other.Unlock()
	}()

	require.NoError(t, Hold("briefly-contested-harp"))
	t.Cleanup(func() { Release("briefly-contested-harp") })
	assert.Equal(t, Alive, Inspect("briefly-contested-harp").Verdict)
}

// TestInspect_UntrustedFilesystem_RefusesEvenWhenUnlocked is constraint 5:
// on a filesystem whose flock cannot be trusted, an unlocked file proves
// nothing, so the verdict is Indeterminate — never Dead — and the reason
// names the filesystem so an operator can see why nothing is ever reclaimed.
func TestInspect_UntrustedFilesystem_RefusesEvenWhenUnlocked(t *testing.T) {
	testsupport.Isolate(t)
	trustEverything(t)
	require.NoError(t, Hold("nfs-harp"))
	Release("nfs-harp")
	require.Equal(t, Dead, Inspect("nfs-harp").Verdict, "precondition: unlocked and trusted reads Dead")

	fsTrust = func(string) (bool, string, error) { return false, "nfs", nil }
	p := Inspect("nfs-harp")
	assert.Equal(t, Indeterminate, p.Verdict)
	assert.False(t, p.Verdict.MayReclaim())
	assert.Contains(t, p.Reason, "nfs")

	fsTrust = func(string) (bool, string, error) { return false, "", errors.New("statfs: boom") }
	p = Inspect("nfs-harp")
	assert.Equal(t, Indeterminate, p.Verdict, "a filesystem that cannot be identified cannot be trusted")
	assert.False(t, p.Verdict.MayReclaim())
}

// TestHold_UntrustedFilesystem_StillHolds: the trust gate REFUSES reclaims;
// it does not stop a session from holding. Holding on NFS costs nothing and
// keeps the file's pid readable; only the sweeper's answer changes.
func TestHold_UntrustedFilesystem_StillHolds(t *testing.T) {
	testsupport.Isolate(t)
	prev := fsTrust
	fsTrust = func(string) (bool, string, error) { return false, "nfs", nil }
	t.Cleanup(func() { fsTrust = prev })

	require.NoError(t, Hold("nfs-held-harp"))
	t.Cleanup(func() { Release("nfs-held-harp") })
	assert.FileExists(t, lockPath(t, "nfs-held-harp"))
	assert.Equal(t, Indeterminate, Inspect("nfs-held-harp").Verdict, "held or not, an untrusted filesystem refuses")
}

// TestFsTrusted_LocalTempDirIsTrusted runs the real platform gate on the
// test's own temp dir: on every runner this project supports the temp dir is
// local, so this pins that the gate does not refuse ordinary local disks —
// the failure that would silently turn every sweep into a no-op.
func TestFsTrusted_LocalTempDirIsTrusted(t *testing.T) {
	ok, fstype, err := fsTrusted(t.TempDir())
	require.NoError(t, err)
	assert.True(t, ok, "temp dir reported as untrusted filesystem %q", fstype)
}

// TestFsTrusted_MissingDirIsAnError: the gate stats the lock's DIRECTORY, so
// a path that does not exist is an error (→ Indeterminate upstream), not a
// trusted answer about nothing.
func TestFsTrusted_MissingDirIsAnError(t *testing.T) {
	_, _, err := fsTrusted(filepath.Join(t.TempDir(), "absent"))
	assert.Error(t, err)
}

// crashHelperEnv, present, turns the re-executed test binary into a holder
// that takes the lock, announces it on stdout, and blocks until killed.
const crashHelperEnv = "CTXLOOM_SESSIONLOCK_CRASH_HELPER"

// TestInspect_KilledHolder_IsDead is the claim the whole row rests on: a
// holder that dies WITHOUT running any cleanup — SIGKILL, no deferred
// Release — leaves a lock the kernel has already dropped, so the sweeper's
// probe reads Dead. Two processes, not two goroutines, because in-process
// nothing can die without unwinding.
func TestInspect_KilledHolder_IsDead(t *testing.T) {
	if os.Getenv(crashHelperEnv) != "" {
		// Child: HOME is inherited from the parent's isolated fixture, so the
		// lock lands in the same temp sessions root the parent probes.
		if err := Hold(os.Getenv(crashHelperEnv)); err != nil {
			os.Stdout.WriteString("hold failed: " + err.Error() + "\n")
			os.Exit(2)
		}
		os.Stdout.WriteString("held\n")
		select {}
	}

	testsupport.Isolate(t)
	trustEverything(t)
	const harp = "crashed-harp"

	child := exec.Command(os.Args[0], "-test.run=TestInspect_KilledHolder_IsDead", "-test.timeout=120s")
	child.Env = append(os.Environ(), crashHelperEnv+"="+harp)
	stdout, err := child.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, child.Start())
	killed := false
	t.Cleanup(func() {
		if !killed {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	})

	line, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "held", strings.TrimSpace(line), "the child must actually hold before it is killed")

	p := Inspect(harp)
	require.Equal(t, Alive, p.Verdict, "a live child holder reads Alive from the parent")
	assert.Equal(t, child.Process.Pid, p.PID)

	require.NoError(t, child.Process.Kill())
	_ = child.Wait()
	killed = true

	p = Inspect(harp)
	assert.Equal(t, Dead, p.Verdict, "the kernel released the killed holder's lock; no cleanup path ran")
	assert.True(t, p.Verdict.MayReclaim())
	assert.Equal(t, child.Process.Pid, p.PID, "the dead holder's pid is still readable for a human")
}
