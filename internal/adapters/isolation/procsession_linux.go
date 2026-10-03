//go:build linux

package isolation

import (
	"os"
	"strconv"
	"syscall"

	"github.com/ctxloom/ctxloom/internal/shared/procpin"
)

// killSession SIGKILLs every process whose /proc session id equals sid. A
// runner spawned via isolateRunner has sid == its own pid, so this
// reaps the runner's ENTIRE host subtree in one sweep: the runner itself plus
// any descendant that moved into its own process group (an engine the runner
// setpgid's, and any worker IT double-forks) without ALSO calling setsid(2)
// — none of them do, so all stay tagged with the runner's session id
// regardless of how many nested process groups they create.
//
// This exists because a raw kill of the runner's OWN pid never reaches a
// process the runner deliberately isolated into its own group. On a HARD
// kill (any external kill -9 on the runner) the runner never gets a chance
// to run its own cleanup, so that grandchild orphans and keeps running until
// something manually reaps it.
//
// Best-effort: unreadable/vanished /proc entries and already-dead targets
// are not errors — "nothing left to kill" is the outcome every caller wants
// either way. The /proc sweep is this file's; darwin enumerates the same
// session through kern.proc.all instead (procsession_darwin.go).
func killSession(sid int) {
	for _, pid := range sessionSweepPids() {
		// Pin the process BEFORE deciding about it. The session read and the kill
		// are two separate steps, and a pid is not a stable identity across
		// them — the target can exit in between and the kernel can hand its
		// number to an unrelated process, which would then take the SIGKILL.
		// The handle names the process that existed at this instant (see
		// procpin.Handle), so a signal can only ever reach that one.
		h, ok := procpin.Pin(pid)
		if !ok {
			continue // already gone: nothing left to kill
		}
		if st, err := procpin.ReadStat(pid); err == nil && st.Session == sid {
			_ = h.Signal(syscall.SIGKILL)
		}
		h.Close()
	}
}

// sessionSweepPids is the set of pids killSession considers. It is a variable
// because the session-id comparison is the sweep's ONLY guard against
// signalling a stranger, so this package's tests narrow it to their own
// process tree: a mutation test that negates that comparison must not be able
// to SIGKILL the machine (procsession_linux_test.go).
var sessionSweepPids = procPids

// procPids lists every pid in /proc; nil when /proc is unreadable.
func procPids() []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	pids := make([]int, 0, len(entries))
	for _, e := range entries {
		if pid, err := strconv.Atoi(e.Name()); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}
