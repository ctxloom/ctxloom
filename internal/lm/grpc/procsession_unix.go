//go:build !windows

package grpc

import (
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
)

// isolateRunner decides the process attributes of EVERY runner subprocess this
// package launches — the self-invoked `ctxloom llm serve <backend>`
// (dialLLMConnection) and the directly-launched `ctxloom llm host <backend>`
// (StartHostRunner). Two things, for opposite directions of the lifetime:
//
//  1. Setsid — the runner leads a FRESH session (session id == its own pid) so
//     killSession can later reap its entire subtree, including a grandchild the
//     runner itself puts in a SEPARATE process group (internal/acp's setpgid,
//     moral-scorn), as one unit, without touching anything outside that
//     dedicated session. This is the DOWNWARD guarantee: when teardown runs,
//     it reaches everything. See killSession.
//
//  2. Pdeathsig — the kernel signals the runner the instant its host process
//     dies, however it dies. This is the UPWARD guarantee, and it exists
//     because (1) only works while the host is alive to run it:
//     realLLMConnection.Kill and HostRunner.Kill live INSIDE the host, so a
//     SIGKILL, an OOM kill, `go test -timeout`'s escalation, a panic that
//     skipped every defer, a cobra path that called os.Exit, or a killed shell
//     that took its whole process group down leaves the runner with nothing
//     watching it. And the runner cannot notice on its own: go-plugin hands it
//     the HOST's stdin rather than a pipe (go-plugin client.go's
//     `cmd.Stdin = os.Stdin`), so no EOF ever arrives, and (1) has by
//     construction put it out of reach of any process-group signal. After the
//     host is gone the kernel is the only party that still remembers the
//     relationship — hence PR_SET_PDEATHSIG rather than more userspace
//     bookkeeping. Without it, orphaned runners accumulate: `llm serve mock`
//     processes have been found piling up across multiple checkouts, some
//     running unattended for over 36 hours.
//
// The signal is SIGTERM, not SIGKILL, so the runner gets to reap its own
// descendants before it goes — see InstallRunnerTeardown, which is the
// receiving half. Linux-only (see pdeath_other.go); the same honest gap as
// killSession, which needs /proc.
func isolateRunner(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	setRunnerPdeathsig(cmd.SysProcAttr)
}

// killSession SIGKILLs every process whose /proc session id equals sid. A
// go-plugin runner spawned via isolateRunner (above) has sid == its own pid, so
// this reaps the runner's ENTIRE host subtree in one sweep: the runner
// itself plus any descendant that moved into its own process group
// (internal/acp's setpgid'd claude-code-acp, and any worker IT
// double-forks) without ALSO calling setsid(2) — none of them do, so all stay
// tagged with the runner's session id regardless of how many nested
// process groups they create.
//
// This exists because go-plugin's own Kill() (github.com/hashicorp/go-plugin) only
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
func killSession(sid int) { killSessionExcept(sid, 0) }

// killSessionExcept is killSession with one member spared — used by
// ReapRunnerDescendants, where the sweeper is itself inside the session it is
// sweeping and must not SIGKILL itself before it has reached its children
// (/proc enumerates in readdir order, and the session leader's pid is normally
// the lowest one in the session, so an unguarded sweep would kill the sweeper
// first and leave exactly the descendants it was called to collect). except=0
// spares nothing — no real process has pid 0.
func killSessionExcept(sid, except int) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if pid == except {
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

// runnerTerminatedExitCode is the conventional 128+SIGTERM a process reports
// when it dies of a signal it chose to handle rather than let default.
const runnerTerminatedExitCode = 143

// ReapRunnerDescendants SIGKILLs every OTHER member of this process's session.
// It is the runner-side counterpart of the host-side killSession: the host
// sweeps the runner's session from outside while it is alive to do so, and
// this sweeps the same session from inside when the host no longer is.
//
// REFUSES TO ACT unless this process is its own session leader — i.e. it was
// launched through isolateRunner, so "my session" means exactly "the subtree I
// own". A `ctxloom llm serve` started by hand from a shell shares that shell's
// session, and an unguarded sweep there would kill the user's shell, its job
// control, and every sibling command in it. Scoping the blast radius to a
// session this package created is the whole reason isolateRunner calls setsid.
func ReapRunnerDescendants() {
	self := os.Getpid()
	if procSessionID(self) != self {
		return
	}
	killSessionExcept(self, self)
}

// InstallRunnerTeardown makes SIGTERM mean "take your subtree with you" for a
// runner that has no other graceful path — `ctxloom llm serve`, whose body is
// go-plugin's blocking plugin.Serve (`llm host` already unwinds through
// waitForRunnerTermination into standup.teardown, so it must NOT also install
// this: two handlers on one signal race, and the graceful one is better).
//
// SIGTERM is what isolateRunner's Pdeathsig delivers when the host dies. Left
// to its default disposition it would end the runner instantly and strand the
// engine subprocess one level further down — trading an orphaned runner for an
// orphaned engine. This closes that step.
//
// It does NOT run standup.teardown (dial-home close, MCP endpoint close): the
// host it would report to is, by construction, already dead. os.Exit here is
// the same abruptness SIGTERM's default already had, plus the sweep.
func InstallRunnerTeardown() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM)
	go func() {
		<-ch
		ReapRunnerDescendants()
		os.Exit(runnerTerminatedExitCode)
	}()
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
