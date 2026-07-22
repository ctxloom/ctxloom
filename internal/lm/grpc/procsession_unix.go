//go:build !windows

package grpc

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// setsid makes cmd the leader of a FRESH session (session id == its own pid)
// so killSession can later reap the runner's entire host subtree — including
// a grandchild the runner itself puts in a SEPARATE process group (e.g. via
// internal/acp's own setpgid, moral-scorn) — as one unit, without touching
// anything outside this runner's dedicated session. See killSession.
func setsid(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}

// killSession SIGKILLs every process whose /proc session id equals sid. A
// go-plugin runner spawned via setsid (above) has sid == its own pid, so
// this reaps the runner's ENTIRE host subtree in one sweep: the runner
// itself plus any descendant that moved into its own process group
// (internal/acp's setpgid'd claude-code-acp, and any worker IT
// double-forks) without ALSO calling setsid — none of them do, so all stay
// tagged with the runner's session id regardless of how many nested
// process groups they create.
//
// This exists because go-plugin's own Kill() (third_party/go-plugin) only
// ever targets the runner's OWN pid (graceful RPC close, or a raw
// cmd.Process.Kill() fallback) — neither reaches a process the runner
// deliberately isolated into its own group. On a HARD kill (the fallback,
// or any external kill -9 on the runner) the runner never gets a chance to
// run its own cleanup (moral-scorn's killProcessGroup lives INSIDE the
// runner process and can't run once it's dead), so that grandchild orphans
// and keeps running until something manually reaps it (damp-pupil 3).
//
// Best-effort: unreadable/vanished /proc entries and already-dead targets
// are not errors — "nothing left to kill" is the outcome every caller wants
// either way. Linux-only (/proc); the windows build has no equivalent, the
// same honest gap as internal/acp/procgroup_windows.go.
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
		if procSessionID(pid) != sid {
			continue
		}
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// procSessionID reads a process's session id from /proc/<pid>/stat (field 6;
// proc(5)) — the comm field can itself contain parens, so the fields after
// it are located from the LAST ')', matching the approach in
// internal/acp/procgroup_unix_test.go's isZombie.
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
