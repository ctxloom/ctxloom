//go:build windows

package pidalive

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// Probe reports pid's liveness on Windows. There is no signal-0 probe (POSIX
// kill(pid,0) has no Windows equivalent), so os.FindProcess's outcome is
// used instead — on Windows it actually calls OpenProcess and so can fail
// for a real, live-but-inaccessible process, not just a genuinely-dead one.
//
// U114-F02: the previous implementation's doc comment claimed this errs
// toward "alive" for exactly this reason ("a stale record on Windows errs
// toward alive"), but the code returned false (Dead) for EVERY FindProcess
// error, including ERROR_ACCESS_DENIED — a live process this token simply
// cannot open. Doc and code genuinely disagreed; this makes the code match
// the documented, non-destructive intent: ERROR_ACCESS_DENIED is Alive, the
// well-documented "no such process" outcome (ERROR_INVALID_PARAMETER, what
// OpenProcess returns for a pid with no matching process) is Dead, and
// anything else this probe cannot interpret is Unsure rather than a guess in
// either direction. golang.org/x/sys/windows (already a dependency —
// internal/shared/ptyrunner/prepare_windows.go) carries the named errno
// constants the standard syscall package for windows does not expose.
func Probe(pid int) State {
	if !pidNamesOneProcess(pid) {
		return Unsure
	}
	p, err := os.FindProcess(pid)
	if err == nil {
		// os.FindProcess is a real OpenProcess here, so a successful probe
		// hands back an OS handle. Dropping the *os.Process on the floor
		// leaves that handle open until the GC finalizer happens to run, and
		// the heaviest caller polls on a timer — a steady handle leak for the
		// life of a long-running watchdog. Release it as soon as the answer is
		// known; nothing else here needs the process.
		_ = p.Release()
		return Alive
	}
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return Alive
	}
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return Dead
	}
	return Unsure
}
