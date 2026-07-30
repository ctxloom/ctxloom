//go:build !linux

package procsec

// denySameUIDInspection is a no-op on every non-Linux platform: there is no
// /proc/<pid>/environ file for a same-uid peer to read, and no portable
// equivalent of Linux's dumpable flag to clear (see procsec_linux.go).
//
// ReasonNoProcExposure reports the absence of THAT exposure and nothing more.
// It is not a claim that the platform keeps a process's environment private
// from its same-uid peers — darwin's KERN_PROCARGS2 and Windows' process
// handles are separate questions this package does not answer. A single !linux
// file rather than per-GOOS siblings because the answer is identical for all of
// them; a platform that grows a mechanism worth wiring gets its own file then.
func denySameUIDInspection() (bool, string, error) {
	return false, ReasonNoProcExposure, nil
}
