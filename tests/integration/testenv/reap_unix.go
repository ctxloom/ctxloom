//go:build !windows

package testenv

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// PluginChildrenOf snapshots the live direct children of ppid that look like
// a ctxloom LLM plugin subprocess (argv contains "llm" "serve" — the exact
// shape internal/lm/grpc's NewSelfInvokingClientForLabelEnv self-execs,
// e.g. "ctxloom llm serve mock"). Deliberately scoped to descendants of ONE
// process this harness itself spawned and is about to hard-kill — never a
// broad process-name sweep across the whole machine, which would risk
// killing a concurrent agent's or suite's own fixtures on a box running
// several test runs at once (the sweep approach rejected in
// fix/fix-temp-dir-leak for the identical reason, applied here to
// processes instead of directories).
//
// Must be called BEFORE the parent is signaled: once it dies the child is
// reparented to init within the same instant, and a ppid-based lookup can
// no longer identify it as "this session's child" — see KillPids, which
// kills the pids captured here directly rather than re-querying ppid.
func PluginChildrenOf(ppid int) []int {
	if ppid <= 0 {
		return nil
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		// No /proc (e.g. darwin) — best-effort no-op, same honest gap as
		// internal/lm/grpc's killSession.
		return nil
	}
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if procPPID(pid) != ppid {
			continue
		}
		if !looksLikeLLMServe(pid) {
			continue
		}
		pids = append(pids, pid)
	}
	return pids
}

// procPPID reads a process's parent pid from /proc/<pid>/stat (field 4;
// proc(5)). Mirrors internal/lm/grpc/procsession_unix.go's procSessionID —
// the comm field can itself contain parens, so fields after it are located
// from the LAST ')'.
func procPPID(pid int) int {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return -1
	}
	i := strings.LastIndexByte(string(data), ')')
	if i < 0 || i+2 >= len(data) {
		return -1
	}
	// After the comm field, this 0-based slice is: state[0] ppid[1] pgrp[2]
	// session[3] — index 0 is the state CHAR ("S", "R", ...), not ppid; ppid
	// is index 1. (Matches internal/lm/grpc/procsession_unix.go's
	// procSessionID, which reads session at its correct index, [3].)
	fields := strings.Fields(string(data[i+2:]))
	if len(fields) < 2 {
		return -1
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return -1
	}
	return ppid
}

// looksLikeLLMServe reports whether pid's argv is a "... llm serve <label>"
// invocation, read from /proc/<pid>/cmdline (NUL-separated argv). Best
// effort: an unreadable/vanished cmdline (process exited between the
// readdir and this read) is "not a match", not an error.
func looksLikeLLMServe(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil || len(data) == 0 {
		return false
	}
	args := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
	hasLLM, hasServe := false, false
	for _, a := range args {
		switch a {
		case "llm":
			hasLLM = true
		case "serve":
			hasServe = true
		}
	}
	return hasLLM && hasServe
}

// KillPids SIGKILLs exactly the pids given — captured by PluginChildrenOf
// before the parent that owned them was signaled. Best-effort: an
// already-dead target (e.g. the parent's own graceful SIGTERM shutdown beat
// this to it) is the outcome every caller wants either way, not an error.
func KillPids(pids []int) {
	for _, pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}
