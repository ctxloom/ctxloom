// Package procsec raises the cost of lifting a secret out of a ctxloom
// process's exec-time environment.
//
// THE HAZARD: /proc/<pid>/environ is a snapshot the kernel took at execve, and
// os.Unsetenv does not scrub it. A credential that a process consumes and
// unsets in its first few lines therefore stays readable in that file for the
// process's entire lifetime. The file is mode 400 owned by the invoking user,
// which means readable by every OTHER process running as that same user — so
// any same-uid process can lift the coordinator credential with a plain file
// read, and that credential is identity.
//
// THIS IS BAR-RAISING, NOT A BOUNDARY. Denying the file read does not stop a
// determined same-uid process: where the kernel's ptrace scope permits it,
// that process can still attach and read the same bytes out of memory.
// Nothing in this package is a sandbox, and nothing in it isolates one ctxloom
// process from another. The isolation boundary is a container. The package is
// named for the narrow thing it does for exactly this reason.
package procsec

import (
	"fmt"
	"os"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// EnvAllowProcessInspection disables the hardening when set to "1". It is
// named for what it ENABLES rather than what it switches off, so the setting
// reads truthfully wherever it appears: it allows inspection of this process.
//
// It exists because a hardened process also refuses same-uid ptrace, so
// delve, gdb, perf and core dumps cannot reach it — debugging is the reason to
// set this, and the only one.
const EnvAllowProcessInspection = "CTXLOOM_ALLOW_PROCESS_INSPECTION"

// Reasons returned by HardenAgainstSameUIDInspection. They exist as values
// rather than prose because the caller's reporting policy is a function of
// WHICH outcome occurred (see Diagnostic), not of whether hardening happened:
// two of these outcomes leave nothing to report and two leave a live exposure.
const (
	// ReasonHardened: the mechanism ran. applied is true.
	ReasonHardened = "dumpable-cleared"
	// ReasonBypassed: EnvAllowProcessInspection asked for no hardening. Not an
	// error, and never silent.
	ReasonBypassed = "inspection-allowed-by-env"
	// ReasonNoProcExposure: this platform has no /proc/<pid>/environ file for a
	// same-uid peer to read, so there is nothing here to deny. It does NOT
	// assert that the platform keeps a process environment private by some
	// other route.
	ReasonNoProcExposure = "no-proc-environ-on-platform"
	// ReasonMechanismFailed: the mechanism was attempted and the kernel
	// refused. Reported with a non-nil error, and never fatal — hardening is
	// defence in depth, not a precondition for running.
	ReasonMechanismFailed = "mechanism-failed"
)

// credEnvName is the environment variable whose exposure motivates this
// package. Spelled out rather than imported from the coordinator package that
// owns it: this is a leaf, and a diagnostic string is not worth an import edge
// pointing up the stack.
const credEnvName = "CTXLOOM_COORD_CRED"

// HardenAgainstSameUIDInspection makes this process's /proc entry unreadable
// by same-uid peers. It affects THIS process only and, because the underlying
// flag is reset on execve, is not inherited by children — every ctxloom
// process calls it for itself.
//
// applied is false with a nil error when hardening was deliberately skipped
// (EnvAllowProcessInspection) or when the platform has no such exposure; a
// non-nil error means the mechanism was tried and refused. Callers report the
// outcome (see Diagnostic) and carry on either way.
func HardenAgainstSameUIDInspection() (applied bool, reason string, err error) {
	if os.Getenv(EnvAllowProcessInspection) == "1" {
		return false, ReasonBypassed, nil
	}
	return denySameUIDInspection()
}

// Diagnostic returns the message body a caller must report for an outcome of
// HardenAgainstSameUIDInspection, or "" when that outcome warrants silence.
//
// Silence is correct for exactly two outcomes: hardening applied, and a
// platform with no /proc-style environ file to deny — a warning that fires on
// every run everywhere is one nobody reads. Every other outcome leaves this
// process's exec-time environment readable by its same-uid peers and MUST be
// reported, because a bypass that prints nothing is indistinguishable from
// hardening that silently failed. An unrecognized reason is silent rather than
// guessed at; the outcomes that carry an exposure are enumerated here.
func Diagnostic(reason string, err error) string {
	exposure := fmt.Sprintf("/proc/%d/environ is readable by any process of this uid and may contain %s",
		os.Getpid(), credEnvName)
	switch reason {
	case ReasonBypassed:
		return fmt.Sprintf("process inspection allowed (%s=1): %s", EnvAllowProcessInspection, exposure)
	case ReasonMechanismFailed:
		return fmt.Sprintf("could not deny same-uid process inspection (%v): %s", err, exposure)
	default:
		return ""
	}
}

// HardenAtStartup is the whole startup policy in one call: harden, then report
// on stderr through the family's clidiag convention whenever the process is
// left inspectable. It is a single call rather than a documented two-step so
// that no caller can apply the hardening and forget the report. It never
// fails; prog is the binary name clidiag stamps on the warning.
func HardenAtStartup(prog string) {
	_, reason, err := HardenAgainstSameUIDInspection()
	if msg := Diagnostic(reason, err); msg != "" {
		clidiag.Warn(prog, "%s", msg)
	}
}
