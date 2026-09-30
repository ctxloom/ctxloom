//go:build !windows

package testenv

import (
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/ctxloom/ctxloom/internal/shared/procpin"
)

// PluginChildrenOf snapshots the live direct children of ppid that look like
// a ctxloom LLM plugin subprocess (argv carries "llm" immediately followed by
// "serve" — the exact shape internal/lm/grpc's NewSelfInvokingClientForLabelEnv self-execs,
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
		if !looksLikeLLMServe(pid) {
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

// looksLikeLLMServe reports whether pid's argv is a "... llm serve <label>"
// invocation, read from /proc/<pid>/cmdline (NUL-separated argv). Best
// effort: an unreadable/vanished cmdline (process exited between the
// readdir and this read) is "not a match", not an error.
//
// "serve" must IMMEDIATELY follow "llm": that is the subcommand path, and it
// is the shape internal/lm/grpc's NewSelfInvokingClientForLabelEnv builds
// (argv "llm", "serve", <backend>, optionally "--label", <label>). Matching
// the two tokens independently would match any argv carrying both anywhere —
// a shell wrapper, a flag value, a path component — and this result selects
// SIGKILL targets, so a loose match kills the wrong process. Adjacency rather
// than a fixed index keeps the match correct if a global flag ever precedes
// the subcommand; a silent non-match would leak the very process this exists
// to reap.
//
// This is a SHAPE test, never an identity one: it cannot tell this session's
// plugin from another run's. Every caller must independently establish
// ownership of the pid — PluginChildrenOf does so by ppid. Reusing this for a
// wider scan than one known parent's direct children would kill a concurrent
// suite's fixtures.
func looksLikeLLMServe(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil || len(data) == 0 {
		return false
	}
	return argvIsLLMServe(strings.Split(strings.TrimRight(string(data), "\x00"), "\x00"))
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

// argvIsLLMServe is looksLikeLLMServe's matching rule over an already-read
// argv, split out so the rule is testable without a live process.
func argvIsLLMServe(args []string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "llm" && args[i+1] == "serve" {
			return true
		}
	}
	return argvIsRunner(args)
}

// argvIsRunner is the runner process's shape: `runner <engine>` as the
// subcommand — the first token after the binary and any global flags, never
// a flag's value (`--runtime runner` is not it). It is what
// spawn.StartRunner starts for a host launch.
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
