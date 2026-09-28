//go:build linux

package isolation

import (
	"os"
	"strconv"
	"strings"
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
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		// Pin the process BEFORE deciding about it. procSessionID and the kill
		// are two separate steps, and a pid is not a stable identity across
		// them — the target can exit in between and the kernel can hand its
		// number to an unrelated process, which would then take the SIGKILL.
		// The handle names the process that existed at this instant (see
		// procHandle), so a signal can only ever reach that one.
		h, ok := pinProcess(pid)
		if !ok {
			continue // already gone: nothing left to kill
		}
		if procSessionID(pid) != sid {
			h.close()
			continue
		}
		h.kill()
	}
}

// procSessionID reads a process's session id from /proc/<pid>/stat (field 6;
// proc(5)) — the comm field can itself contain parens, so the fields after
// it are located from the LAST ')', matching the approach in procalive's
// isZombie.
func procSessionID(pid int) int {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return -1
	}
	i := strings.LastIndexByte(string(data), ')')
	if i < 0 || i+2 >= len(data) {
		return -1
	}
	// After the comm field: state(1) ppid(2) pgrp(3) session(4) — indices
	// 0..3 in this 0-based slice.
	fields := strings.Fields(string(data[i+2:]))
	if len(fields) < 4 {
		return -1
	}
	sid, err := strconv.Atoi(fields[3])
	if err != nil {
		return -1
	}
	return sid
}
