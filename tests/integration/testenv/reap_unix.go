//go:build !windows

package testenv

import (
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/ctxloom/ctxloom/internal/shared/procpin"
)

// RunnerChildrenOf snapshots the live direct children of ppid that look like
// a ctxloom runner subprocess (`ctxloom runner <engine>`, what
// isolation.StartHostRunner starts). Deliberately scoped to descendants of
// ONE process this harness itself spawned and is about to hard-kill — never
// a broad process-name sweep across the whole machine, which would risk
// killing a concurrent agent's or suite's own fixtures on a box running
// several test runs at once.
//
// Must be called BEFORE the parent is signaled: once it dies the child is
// reparented to init within the same instant, and a ppid-based lookup can
// no longer identify it as "this session's child" — see KillPids, which
// kills the pids captured here directly rather than re-querying ppid.
func RunnerChildrenOf(ppid int) []int {
	if ppid <= 0 {
		return nil
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		// No /proc (e.g. darwin): nothing to enumerate from here.
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
		if !looksLikeRunner(pid) {
			continue
		}
		pids = append(pids, pid)
	}
	return pids
}

// procPPID reads a process's parent pid through procpin.ReadStat; an
// unreadable stat (the process exited, or no /proc) is -1, never a match.
func procPPID(pid int) int {
	st, err := procpin.ReadStat(pid)
	if err != nil {
		return -1
	}
	return st.PPID
}

// looksLikeRunner reports whether pid's argv is a runner invocation, read
// from /proc/<pid>/cmdline (NUL-separated argv). Best effort: an
// unreadable/vanished cmdline (process exited between the readdir and this
// read) is "not a match", not an error.
//
// This is a SHAPE test, never an identity one: it cannot tell this session's
// runner from another run's. Every caller must independently establish
// ownership of the pid — RunnerChildrenOf does so by ppid. Reusing this for a
// wider scan than one known parent's direct children would kill a concurrent
// suite's fixtures.
func looksLikeRunner(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil || len(data) == 0 {
		return false
	}
	return argvIsRunner(strings.Split(strings.TrimRight(string(data), "\x00"), "\x00"))
}

// KillPids SIGKILLs exactly the pids given — captured by RunnerChildrenOf
// before the parent that owned them was signaled. Best-effort: an
// already-dead target (e.g. the parent's own graceful SIGTERM shutdown beat
// this to it) is the outcome every caller wants either way, not an error.
func KillPids(pids []int) {
	for _, pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// argvIsRunner is looksLikeRunner's matching rule over an already-read argv:
// `runner <engine>` as the subcommand — the first token after the binary and
// any global flags, never a flag's value (`--runtime runner` is not it). This
// result selects SIGKILL targets, so a loose match kills the wrong process.
func argvIsRunner(args []string) bool {
	for i := 1; i+1 < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			continue
		}
		if strings.HasPrefix(args[i-1], "--") && !strings.Contains(args[i-1], "=") && i > 1 {
			// a flag's value
			continue
		}
		return args[i] == "runner"
	}
	return false
}
