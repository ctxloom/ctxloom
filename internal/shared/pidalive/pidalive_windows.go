//go:build windows

package pidalive

import (
	"errors"
	"math"

	"golang.org/x/sys/windows"
)

// Probe reports pid's liveness on Windows. There is no signal-0 probe (POSIX
// kill(pid,0) has no Windows equivalent), so the probe opens the process and
// asks whether it has exited.
//
// Opening alone is NOT liveness: a process object outlives its process for as
// long as anyone holds a handle to it, so OpenProcess succeeds on an exited
// process and the pid stays reserved for it. The verdict therefore comes from
// a zero-timeout wait on the handle — a process handle is signalled exactly
// when the process has exited.
//
// Errors map in the non-destructive direction: ERROR_ACCESS_DENIED is a live
// process this token cannot open, so Alive; ERROR_INVALID_PARAMETER is what
// OpenProcess returns for a pid with no process object, so Dead; anything
// else is Unsure rather than a guess either way.
func Probe(pid int) State {
	if !pidNamesOneProcess(pid) || pid > math.MaxUint32 {
		return Unsure
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return Alive
		}
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return Dead
		}
		return Unsure
	}
	// The heaviest caller polls on a timer, so a handle left open per probe
	// is a steady leak for the life of a long-running watchdog.
	defer func() { _ = windows.CloseHandle(h) }()
	switch ev, _ := windows.WaitForSingleObject(h, 0); ev {
	case windows.WAIT_OBJECT_0:
		return Dead
	case uint32(windows.WAIT_TIMEOUT):
		return Alive
	default:
		return Unsure
	}
}
