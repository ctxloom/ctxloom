// Package procpin is how ctxloom acts on ANOTHER process by pid without the
// pid's reuse turning the act on a stranger: a Handle pins the process that
// existed when it was taken, and a signal sent through it reaches that process
// or nothing. ReadStat is the kernel's own account of a process — its parent,
// session, controlling terminal and start time — for deciding whether to act.
//
// Decide AFTER pinning: take the Handle first, then ReadStat, then signal. The
// read may then describe a newer holder of the pid, but the signal cannot
// reach it, so the worst outcome is a skipped or no-op signal.
//
// Linux is the only platform with both halves (pidfd, /proc). Elsewhere
// ReadStat returns ErrUnsupported, so a caller that needs the stat to decide
// never acts there.
package procpin

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrUnsupported reports a platform where the kernel's per-process stat is not
// readable, so nothing about another process can be established.
var ErrUnsupported = errors.New("procpin: process stat is not readable on this platform")

// Stat is the part of proc(5)'s /proc/<pid>/stat callers decide on.
type Stat struct {
	// State is the one-letter process state (R, S, D, Z, ...).
	State byte
	// PPID is the parent's pid: 1 once the process has been reparented to
	// init.
	PPID int
	// Session is the session id.
	Session int
	// TTYNr is the controlling terminal's device number; 0 means none.
	TTYNr int
	// StartTicks is the process's start time in clock ticks since boot. With
	// the pid it identifies one process across the machine's uptime, which
	// the pid alone does not.
	StartTicks uint64
}

// parseStat parses one /proc/<pid>/stat line. The comm field is parenthesized
// and may itself contain spaces and parentheses, so every later field is
// located from the LAST ')'.
func parseStat(data []byte) (Stat, error) {
	line := string(data)
	i := strings.LastIndexByte(line, ')')
	if i < 0 || i+2 >= len(line) {
		return Stat{}, fmt.Errorf("procpin: malformed stat %q", line)
	}
	// After comm: state(3) ppid(4) pgrp(5) session(6) tty_nr(7) ... starttime(22),
	// numbered as proc(5) numbers them; index = number - 3.
	f := strings.Fields(line[i+2:])
	const startIdx = 22 - 3
	if len(f) <= startIdx || len(f[0]) != 1 {
		return Stat{}, fmt.Errorf("procpin: short stat %q", line)
	}
	ppid, err1 := strconv.Atoi(f[1])
	sid, err2 := strconv.Atoi(f[3])
	tty, err3 := strconv.Atoi(f[4])
	start, err4 := strconv.ParseUint(f[startIdx], 10, 64)
	if err := errors.Join(err1, err2, err3, err4); err != nil {
		return Stat{}, fmt.Errorf("procpin: stat fields: %w", err)
	}
	return Stat{State: f[0][0], PPID: ppid, Session: sid, TTYNr: tty, StartTicks: start}, nil
}
