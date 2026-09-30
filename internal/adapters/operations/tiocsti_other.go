//go:build !linux

package operations

// doctorCheckTTYInjection says the question was not asked: ctxloom reads the
// terminal-injection switch only where it knows one (Linux's
// dev.tty.legacy_tiocsti; see ttyInjectionCheck).
func doctorCheckTTYInjection() DoctorCheck {
	return DoctorCheck{Marker: doctorTTYInjectionMarker, Status: DoctorInfo,
		Detail: "terminal keystroke injection (TIOCSTI) is not checked on this OS"}
}
