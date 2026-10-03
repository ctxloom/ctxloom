package coord

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/procpin"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// holdOwnerLock takes dir's owner lock through a descriptor of the test's own,
// standing in for another process: flock conflicts between two open file
// descriptions even within one process. The returned func releases it.
func holdOwnerLock(t *testing.T, dir string) func() {
	t.Helper()
	fl := flock.New(filepath.Join(dir, OwnerLockFileName), flock.SetPermissions(0o600))
	got, err := fl.TryLock()
	require.NoError(t, err)
	require.True(t, got)
	var once bool
	release := func() {
		if !once {
			once = true
			_ = fl.Close()
		}
	}
	t.Cleanup(release)
	return release
}

func writeStamp(t *testing.T, dir string, s ownerStamp) {
	t.Helper()
	raw, err := json.Marshal(s)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ownerStampFileName), raw, 0o600))
}

// fakeProc makes judgeOrphan see pid as the given kernel stat.
func fakeProc(t *testing.T, st procpin.Stat, err error) {
	t.Helper()
	prev := readProcStat
	readProcStat = func(int) (procpin.Stat, error) { return st, err }
	t.Cleanup(func() { readProcStat = prev })
}

// fakeEnd records a reclaim instead of signalling, releasing the stand-in
// owner's lock as the orphan's death would.
func fakeEnd(t *testing.T, release func()) *int {
	t.Helper()
	calls := 0
	prev := endOrphan
	endOrphan = func(ownerStamp) error { calls++; release(); return nil }
	t.Cleanup(func() { endOrphan = prev })
	return &calls
}

const orphanTicks = 4242

func interactiveStamp() ownerStamp {
	return ownerStamp{PID: 999001, Harp: "gone-terminal-harp", Mode: OwnerInteractive, Started: time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC), StartTicks: orphanTicks}
}

// The ordinary path: a claim stamps who owns the project, ProbeOwner sees it
// held without claiming it, and release frees it while the lock file stays.
func TestClaimOwner_StampsAndReleases(t *testing.T) {
	dir := t.TempDir()
	release, err := claimOwner(termRep(), dir, newOwnerStamp("owner-harp", OwnerInteractive))
	require.NoError(t, err)

	st, err := ProbeOwner(dir)
	require.NoError(t, err)
	assert.True(t, st.Held)
	assert.Equal(t, os.Getpid(), st.PID)
	assert.Equal(t, "owner-harp", st.Harp)
	assert.Equal(t, OwnerInteractive, st.Mode)
	assert.False(t, st.Orphan, "this process has a parent")

	release()
	st, err = ProbeOwner(dir)
	require.NoError(t, err)
	assert.False(t, st.Held, "release frees the project")
	_, statErr := os.Stat(filepath.Join(dir, OwnerLockFileName))
	assert.NoError(t, statErr, "the lock file persists; only its lock is ownership")
}

// A stamp naming a LIVE pid does not make an unlocked project owned: pid
// reuse can no longer keep a dead owner alive, because the lock is the fact.
func TestClaimOwner_StaleStampOfALivePidDoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, OwnerLockFileName), nil, 0o600))
	writeStamp(t, dir, ownerStamp{PID: os.Getppid(), Harp: "long-dead-harp", Mode: OwnerInteractive})

	release, err := claimOwner(termRep(), dir, newOwnerStamp("new-harp", OwnerInteractive))
	require.NoError(t, err, "an unlocked owner lock is free, whatever pid its stale stamp names")
	t.Cleanup(release)
}

// A live owner is refused by name, and the loser leaves its stamp alone.
func TestClaimOwner_LiveOwnerIsRefusedByName(t *testing.T) {
	dir := t.TempDir()
	holdOwnerLock(t, dir)
	s := interactiveStamp()
	writeStamp(t, dir, s)
	fakeProc(t, procpin.Stat{PPID: 1, TTYNr: 34816, StartTicks: orphanTicks}, nil)
	calls := fakeEnd(t, func() {})

	_, err := claimOwner(termRep(), dir, newOwnerStamp("loser", OwnerInteractive))
	require.ErrorIs(t, err, ErrStateOwned)
	assert.Contains(t, err.Error(), s.Harp)
	assert.Contains(t, err.Error(), strconv.Itoa(s.PID))
	assert.Contains(t, err.Error(), s.Started.Format(time.RFC3339))
	assert.Zero(t, *calls, "a live owner is never ended")
	raw, rerr := os.ReadFile(filepath.Join(dir, ownerStampFileName))
	require.NoError(t, rerr)
	var got ownerStamp
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, s.PID, got.PID, "the loser must not restamp the owner")
}

// An orphan — interactive, reparented to init, no controlling terminal — is
// ended and the project claimed, and the reclaim is reported.
func TestClaimOwner_OrphanIsReclaimed(t *testing.T) {
	dir := t.TempDir()
	release := holdOwnerLock(t, dir)
	s := interactiveStamp()
	writeStamp(t, dir, s)
	fakeProc(t, procpin.Stat{PPID: 1, TTYNr: 0, StartTicks: orphanTicks}, nil)
	calls := fakeEnd(t, release)

	var found report.Findings
	rel, err := claimOwner(report.To(&found), dir, newOwnerStamp("new-owner", OwnerInteractive))
	require.NoError(t, err)
	t.Cleanup(rel)
	assert.Equal(t, 1, *calls)
	require.Len(t, found, 1)
	assert.Contains(t, found[0].Text, s.Harp)
	assert.Contains(t, found[0].Text, strconv.Itoa(s.PID))

	st, err := ProbeOwner(dir)
	require.NoError(t, err)
	assert.Equal(t, "new-owner", st.Harp, "the reclaimer now owns the project")
}

// Every case that is not provably an abandoned interactive session refuses,
// and never ends the owner.
func TestClaimOwner_NonOrphansAreRefused(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*ownerStamp)
		stat    procpin.Stat
		statErr error
		stamp   bool
	}{
		{name: "live owner with a tty", stat: procpin.Stat{PPID: 1, TTYNr: 34816, StartTicks: orphanTicks}, stamp: true},
		{name: "owner still parented", stat: procpin.Stat{PPID: 777, StartTicks: orphanTicks}, stamp: true},
		{name: "non-interactive owner", mutate: func(s *ownerStamp) { s.Mode = OwnerNonInteractive }, stat: procpin.Stat{PPID: 1, StartTicks: orphanTicks}, stamp: true},
		{name: "mode never stamped", mutate: func(s *ownerStamp) { s.Mode = "" }, stat: procpin.Stat{PPID: 1, StartTicks: orphanTicks}, stamp: true},
		{name: "undeterminable: stat unreadable", statErr: procpin.ErrUnsupported, stamp: true},
		{name: "undeterminable: no process identity stamped", mutate: func(s *ownerStamp) { s.StartTicks = 0 }, stat: procpin.Stat{PPID: 1}, stamp: true},
		{name: "undeterminable: pid now another process", stat: procpin.Stat{PPID: 1, StartTicks: orphanTicks + 1}, stamp: true},
		{name: "undeterminable: no stamp", stat: procpin.Stat{PPID: 1, StartTicks: orphanTicks}, stamp: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			release := holdOwnerLock(t, dir)
			s := interactiveStamp()
			if tc.mutate != nil {
				tc.mutate(&s)
			}
			if tc.stamp {
				writeStamp(t, dir, s)
			}
			fakeProc(t, tc.stat, tc.statErr)
			calls := fakeEnd(t, release)

			_, err := claimOwner(termRep(), dir, newOwnerStamp("loser", OwnerInteractive))
			require.ErrorIs(t, err, ErrStateOwned)
			assert.Zero(t, *calls, "a non-orphan is never ended")

			st, perr := ProbeOwner(dir)
			require.NoError(t, perr)
			assert.True(t, st.Held)
			assert.False(t, st.Orphan)
			assert.NotEmpty(t, st.Reason)
		})
	}
}

// ProbeOwner on a dir no claim ever touched reports unowned and creates
// nothing.
func TestProbeOwner_UnclaimedDir(t *testing.T) {
	dir := t.TempDir()
	st, err := ProbeOwner(dir)
	require.NoError(t, err)
	assert.Equal(t, OwnerStatus{}, st)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "a probe never mints a lock file")
}

// The real reclaim re-judges after pinning: a process that no longer proves
// orphaned is left alone.
func TestEndOrphan_RejudgesAfterPinning(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	fakeProc(t, procpin.Stat{PPID: os.Getpid(), StartTicks: orphanTicks}, nil)

	s := interactiveStamp()
	s.PID = cmd.Process.Pid
	err := endOrphan(s)
	require.ErrorIs(t, err, errNoLongerOrphan)
	st, serr := procpin.ReadStat(cmd.Process.Pid)
	require.NoError(t, serr)
	assert.NotEqual(t, byte('Z'), st.State, "the process must not have been signalled")
}

// The real reclaim ends an orphan with SIGTERM and reports success once it has
// exited.
func TestEndOrphan_TerminatesTheProcess(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	exited := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(exited) }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	fakeProc(t, procpin.Stat{PPID: 1, StartTicks: orphanTicks}, nil)

	s := interactiveStamp()
	s.PID = cmd.Process.Pid
	err := endOrphan(s)
	if errors.Is(err, procpin.ErrUnsupported) {
		t.Skip("no process pinning on this platform")
	}
	require.NoError(t, err)
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the orphan was not ended")
	}
}
