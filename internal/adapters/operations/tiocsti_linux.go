//go:build linux

package operations

import "os"

// legacyTIOCSTIPath is where Linux exposes dev.tty.legacy_tiocsti.
const legacyTIOCSTIPath = "/proc/sys/dev/tty/legacy_tiocsti"

// doctorCheckTTYInjection reports whether this host lets a process type into
// the terminal (ttyInjectionCheck).
func doctorCheckTTYInjection() DoctorCheck {
	b, err := os.ReadFile(legacyTIOCSTIPath)
	return ttyInjectionCheck(b, err)
}
