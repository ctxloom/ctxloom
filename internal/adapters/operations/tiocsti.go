package operations

import (
	"errors"
	"io/fs"
	"strings"
)

// doctorTTYInjectionMarker is the DOCTOR-CHECK-* entry for terminal
// keystroke injection — see ttyInjectionCheck.
const doctorTTYInjectionMarker = "DOCTOR-CHECK-TTY-INJECTION-m3"

// doctorTTYInjectionRemedy turns legacy TIOCSTI off.
const doctorTTYInjectionRemedy = "sysctl -w dev.tty.legacy_tiocsti=0, and persist it in /etc/sysctl.d"

// ttyInjectionCheck judges the kernel's dev.tty.legacy_tiocsti setting (its
// content, or the error reading it).
//
// The approval modal treats presence at the originator's terminal as the
// human's authority, and its focus locks stop a key typed for the engine from
// deciding an approval. They do not stop a process that can TYPE into the
// terminal: TIOCSTI lets any process running as the same user push keys into
// it, and such a process can wait out the arming and send Tab + Enter. Linux
// 6.2 made that switchable (legacy_tiocsti=0 refuses it); an older kernel has
// no switch and always allows it.
func ttyInjectionCheck(content []byte, err error) DoctorCheck {
	c := DoctorCheck{Marker: doctorTTYInjectionMarker, Status: DoctorWarn}
	switch v := strings.TrimSpace(string(content)); {
	case errors.Is(err, fs.ErrNotExist):
		c.Detail = "this kernel has no dev.tty.legacy_tiocsti (it predates 6.2), so any process running as you can type into your terminal — approvals included"
		c.Remedy = "run on Linux 6.2 or later with dev.tty.legacy_tiocsti=0"
	case err != nil:
		c.Detail = "cannot read dev.tty.legacy_tiocsti: " + err.Error()
	case v == "0":
		c.Status, c.Detail = DoctorOK, "terminal keystroke injection (TIOCSTI) is off: dev.tty.legacy_tiocsti=0"
	case v == "1":
		c.Detail = "terminal keystroke injection (TIOCSTI) is on: dev.tty.legacy_tiocsti=1 lets any process running as you type into your terminal — approvals included"
		c.Remedy = doctorTTYInjectionRemedy
	default:
		c.Detail = `dev.tty.legacy_tiocsti reads "` + v + `", which is neither 0 nor 1`
	}
	return c
}
